package silence

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

func TestSilenceBiz_CRUDAndFilters(t *testing.T) {
	db := newSilenceTestDB(t)
	s := store.NewStore(db)
	biz := New(s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	createResp, err := biz.Create(ctx, &v1.CreateSilenceRequest{
		Namespace: ptrSilenceString("default"),
		Enabled:   ptrSilenceBool(true),
		StartsAt:  timestamppb.New(now.Add(-5 * time.Minute)),
		EndsAt:    timestamppb.New(now.Add(55 * time.Minute)),
		Reason:    ptrSilenceString("maintenance"),
		Matchers: []*v1.SilenceMatcher{
			{Key: "fingerprint", Op: "=", Value: "fp-silence-1"},
			{Key: "severity", Op: "=", Value: "warning"},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, createResp.GetSilence())
	require.True(t, strings.HasPrefix(createResp.GetSilence().GetSilenceID(), "silence-"))

	silenceID := createResp.GetSilence().GetSilenceID()

	getResp, err := biz.Get(ctx, &v1.GetSilenceRequest{SilenceID: silenceID})
	require.NoError(t, err)
	require.Equal(t, "default", getResp.GetSilence().GetNamespace())
	require.Equal(t, int32(2), int32(len(getResp.GetSilence().GetMatchers())))

	listActiveResp, err := biz.List(ctx, &v1.ListSilencesRequest{
		Namespace: ptrSilenceString("default"),
		Enabled:   ptrSilenceBool(true),
		Active:    ptrSilenceBool(true),
		Offset:    0,
		Limit:     20,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, listActiveResp.GetTotalCount(), int64(1))

	_, err = biz.Patch(ctx, &v1.PatchSilenceRequest{
		SilenceID: silenceID,
		Enabled:   ptrSilenceBool(false),
		Reason:    ptrSilenceString("window done"),
	})
	require.NoError(t, err)

	getAfterPatch, err := biz.Get(ctx, &v1.GetSilenceRequest{SilenceID: silenceID})
	require.NoError(t, err)
	require.False(t, getAfterPatch.GetSilence().GetEnabled())
	require.Equal(t, "window done", getAfterPatch.GetSilence().GetReason())

	_, err = biz.Delete(ctx, &v1.DeleteSilenceRequest{SilenceID: silenceID})
	require.NoError(t, err)

	listInactiveResp, err := biz.List(ctx, &v1.ListSilencesRequest{
		Namespace: ptrSilenceString("default"),
		Active:    ptrSilenceBool(false),
		Offset:    0,
		Limit:     20,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, listInactiveResp.GetTotalCount(), int64(1))
}

func newSilenceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.SilenceM{}))
	return db
}

func ptrSilenceString(v string) *string { return &v }

func ptrSilenceBool(v bool) *bool { return &v }
