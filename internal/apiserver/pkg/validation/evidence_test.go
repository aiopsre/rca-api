package validation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "zk8s.com/rca-api/pkg/api/apiserver/v1"
)

func TestValidateQueryLogsRequest_Guardrails(t *testing.T) {
	val := &Validator{}
	now := time.Now().UTC()

	err := val.ValidateQueryLogsRequest(context.Background(), &v1.QueryLogsRequest{
		DatasourceID:   "ds-1",
		QueryText:      "error",
		TimeRangeStart: timestamppb.New(now.Add(-7 * time.Hour)),
		TimeRangeEnd:   timestamppb.New(now),
		Limit:          ptrValidationInt64(10),
	})
	require.Error(t, err)

	err = val.ValidateQueryLogsRequest(context.Background(), &v1.QueryLogsRequest{
		DatasourceID:   "ds-1",
		QueryText:      "error",
		TimeRangeStart: timestamppb.New(now.Add(-30 * time.Minute)),
		TimeRangeEnd:   timestamppb.New(now),
		Limit:          ptrValidationInt64(999),
	})
	require.Error(t, err)
}

func TestValidateQueryMetricsRequest_MaxStep(t *testing.T) {
	val := &Validator{}
	now := time.Now().UTC()

	err := val.ValidateQueryMetricsRequest(context.Background(), &v1.QueryMetricsRequest{
		DatasourceID:   "ds-1",
		Promql:         "up",
		TimeRangeStart: timestamppb.New(now.Add(-10 * time.Minute)),
		TimeRangeEnd:   timestamppb.New(now),
		StepSeconds:    ptrValidationInt64(999),
	})
	require.Error(t, err)
}

func TestValidateListIncidentEvidence_DefaultLimit(t *testing.T) {
	val := &Validator{}
	req := &v1.ListIncidentEvidenceRequest{
		IncidentID: "incident-1",
		Offset:     0,
		Limit:      0,
	}

	err := val.ValidateListIncidentEvidenceRequest(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, int64(20), req.Limit)
}

func ptrValidationInt64(v int64) *int64 { return &v }
