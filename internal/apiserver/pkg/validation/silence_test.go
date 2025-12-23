package validation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "zk8s.com/rca-api/pkg/api/apiserver/v1"
)

func TestValidateCreateSilenceRequest_Guardrails(t *testing.T) {
	val := &Validator{}
	now := time.Now().UTC().Truncate(time.Second)

	err := val.ValidateCreateSilenceRequest(context.Background(), &v1.CreateSilenceRequest{
		Namespace: strPtrValidationSilence("default"),
		StartsAt:  timestamppb.New(now),
		EndsAt:    timestamppb.New(now.Add(time.Hour)),
		Matchers:  []*v1.SilenceMatcher{},
	})
	require.Error(t, err)

	err = val.ValidateCreateSilenceRequest(context.Background(), &v1.CreateSilenceRequest{
		Namespace: strPtrValidationSilence("default"),
		StartsAt:  timestamppb.New(now),
		EndsAt:    timestamppb.New(now),
		Matchers: []*v1.SilenceMatcher{
			{Key: "fingerprint", Op: "=", Value: "fp-1"},
		},
	})
	require.Error(t, err)

	err = val.ValidateCreateSilenceRequest(context.Background(), &v1.CreateSilenceRequest{
		Namespace: strPtrValidationSilence("default"),
		StartsAt:  timestamppb.New(now),
		EndsAt:    timestamppb.New(now.Add(time.Hour)),
		Matchers: []*v1.SilenceMatcher{
			{Key: "pod", Op: "=", Value: "pod-1"},
		},
	})
	require.Error(t, err)

	err = val.ValidateCreateSilenceRequest(context.Background(), &v1.CreateSilenceRequest{
		Namespace: strPtrValidationSilence("default"),
		StartsAt:  timestamppb.New(now),
		EndsAt:    timestamppb.New(now.Add(time.Hour)),
		Matchers: []*v1.SilenceMatcher{
			{Key: "service", Op: "!=", Value: "checkout"},
		},
	})
	require.Error(t, err)

	err = val.ValidateCreateSilenceRequest(context.Background(), &v1.CreateSilenceRequest{
		Namespace: strPtrValidationSilence("default"),
		StartsAt:  timestamppb.New(now),
		EndsAt:    timestamppb.New(now.Add(time.Hour)),
		Matchers: []*v1.SilenceMatcher{
			{Key: "fingerprint", Op: "=", Value: "fp-1"},
		},
	})
	require.NoError(t, err)
}

func TestValidateListSilencesRequest_DefaultAndLimit(t *testing.T) {
	val := &Validator{}

	rq := &v1.ListSilencesRequest{}
	err := val.ValidateListSilencesRequest(context.Background(), rq)
	require.NoError(t, err)
	require.Equal(t, int64(20), rq.GetLimit())

	err = val.ValidateListSilencesRequest(context.Background(), &v1.ListSilencesRequest{
		Offset: -1,
		Limit:  10,
	})
	require.Error(t, err)

	err = val.ValidateListSilencesRequest(context.Background(), &v1.ListSilencesRequest{
		Offset: 0,
		Limit:  201,
	})
	require.Error(t, err)
}

func TestValidatePatchSilenceRequest_RequireFields(t *testing.T) {
	val := &Validator{}

	err := val.ValidatePatchSilenceRequest(context.Background(), &v1.PatchSilenceRequest{
		SilenceID: "silence-1",
	})
	require.Error(t, err)

	err = val.ValidatePatchSilenceRequest(context.Background(), &v1.PatchSilenceRequest{
		SilenceID: "silence-1",
		Enabled:   boolPtrValidationSilence(false),
	})
	require.NoError(t, err)
}

func strPtrValidationSilence(v string) *string { return &v }

func boolPtrValidationSilence(v bool) *bool { return &v }
