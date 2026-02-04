package incident

import (
	"context"
	"testing"

	sessionbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/session"
	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCreateIncident_BestEffortEnsureSession(t *testing.T) {
	store.ResetForTest()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.IncidentM{}, &model.SessionContextM{}))

	s := store.NewStore(db)
	biz := New(s)

	createResp, err := biz.Create(context.Background(), newCreateIncidentRequestForSessionTest())
	require.NoError(t, err)
	require.NotEmpty(t, createResp.GetIncidentID())

	sessionObj, err := s.SessionContext().GetByIncidentID(context.Background(), createResp.GetIncidentID())
	require.NoError(t, err)
	require.NotNil(t, sessionObj)
	require.Equal(t, sessionbiz.SessionTypeIncident, sessionObj.SessionType)
	require.Equal(t, createResp.GetIncidentID(), sessionObj.BusinessKey)
	require.NotNil(t, sessionObj.IncidentID)
	require.Equal(t, createResp.GetIncidentID(), *sessionObj.IncidentID)
}

func TestCreateIncident_SessionTableMissingDoesNotBreak(t *testing.T) {
	store.ResetForTest()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	// Intentionally migrate incidents only to validate best-effort behavior.
	require.NoError(t, db.AutoMigrate(&model.IncidentM{}))

	s := store.NewStore(db)
	biz := New(s)

	createResp, err := biz.Create(context.Background(), newCreateIncidentRequestForSessionTest())
	require.NoError(t, err)
	require.NotEmpty(t, createResp.GetIncidentID())
}

func newCreateIncidentRequestForSessionTest() *v1.CreateIncidentRequest {
	return &v1.CreateIncidentRequest{
		Namespace:    "prod",
		WorkloadKind: "Deployment",
		WorkloadName: "checkout-api",
		Service:      "checkout",
		Severity:     "P2",
		Source:       strPtrForTest("api"),
	}
}
