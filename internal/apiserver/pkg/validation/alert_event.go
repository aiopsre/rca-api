package validation

import (
	"context"
	"strings"

	"github.com/onexstack/onexstack/pkg/errorsx"
	genericvalidation "github.com/onexstack/onexstack/pkg/validation"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

const maxAlertEventListLimit = int64(200)

func (v *Validator) ValidateAlertEventRules() genericvalidation.Rules {
	return genericvalidation.Rules{}
}

func (v *Validator) ValidateIngestAlertEventRequest(ctx context.Context, rq *v1.IngestAlertEventRequest) error {
	if err := genericvalidation.ValidateAllFields(rq, v.ValidateAlertEventRules()); err != nil {
		return err
	}
	if strings.TrimSpace(rq.GetStatus()) == "" {
		return errorsx.ErrInvalidArgument
	}
	return validateTimeRangePair(rq.StartsAt, rq.EndsAt)
}

func (v *Validator) ValidateListCurrentAlertEventsRequest(ctx context.Context, rq *v1.ListCurrentAlertEventsRequest) error {
	if err := genericvalidation.ValidateSelectedFields(rq, v.ValidateAlertEventRules(), "Offset", "Limit"); err != nil {
		return err
	}
	if rq.GetLimit() > maxAlertEventListLimit {
		return errorsx.ErrInvalidArgument
	}
	return validateTimeRangePair(rq.LastSeenStart, rq.LastSeenEnd)
}

func (v *Validator) ValidateListHistoryAlertEventsRequest(ctx context.Context, rq *v1.ListHistoryAlertEventsRequest) error {
	if err := genericvalidation.ValidateSelectedFields(rq, v.ValidateAlertEventRules(), "Offset", "Limit"); err != nil {
		return err
	}
	if rq.GetLimit() > maxAlertEventListLimit {
		return errorsx.ErrInvalidArgument
	}
	return validateTimeRangePair(rq.LastSeenStart, rq.LastSeenEnd)
}

func (v *Validator) ValidateAckAlertEventRequest(ctx context.Context, rq *v1.AckAlertEventRequest) error {
	if err := genericvalidation.ValidateAllFields(rq, v.ValidateAlertEventRules()); err != nil {
		return err
	}
	if strings.TrimSpace(rq.GetEventID()) == "" {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func validateTimeRangePair(start *timestamppb.Timestamp, end *timestamppb.Timestamp) error {
	if start == nil || end == nil {
		return nil
	}
	if start.AsTime().After(end.AsTime()) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}
