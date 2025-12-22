package validation

import (
	"context"
	"strings"
	"time"

	"github.com/onexstack/onexstack/pkg/errorsx"

	v1 "zk8s.com/rca-api/pkg/api/apiserver/v1"
)

const (
	defaultAIJobQueueListLimit      = int64(10)
	maxAIJobQueueListLimit          = int64(50)
	defaultAIJobQueueWaitSecondsMax = int64(30)
	defaultAIJobListLimit           = int64(20)
	maxAIJobListLimit               = int64(200)
	defaultToolCallList             = int64(50)
	maxToolCallList                 = int64(200)
	maxAIHintsLength                = 16384
	maxAIErrorMessage               = 8192
	maxAIPipelineLength             = 64
	maxAITriggerLength              = 64
	maxToolCallJSONSize             = 4 * 1024 * 1024
	maxToolCallRefLength            = 1024
	maxAIIdempotencyLength          = 128
	maxAIWindowRange                = 24 * time.Hour
)

var (
	allowedAIJobStatusTransitionTerminal = map[string]struct{}{
		"succeeded": {},
		"failed":    {},
		"canceled":  {},
	}
	allowedAIJobTriggers = map[string]struct{}{
		"manual":        {},
		"on_ingest":     {},
		"on_escalation": {},
		"scheduled":     {},
	}
	allowedAIJobQueueStatus = map[string]struct{}{
		"queued": {},
	}
	allowedAIToolCallStatus = map[string]struct{}{
		"ok":       {},
		"error":    {},
		"timeout":  {},
		"canceled": {},
	}
)

func (v *Validator) ValidateRunAIJobRequest(ctx context.Context, rq *v1.RunAIJobRequest) error {
	_ = ctx
	if strings.TrimSpace(rq.GetIncidentID()) == "" {
		return errorsx.ErrInvalidArgument
	}
	if rq.IdempotencyKey != nil && len(strings.TrimSpace(rq.GetIdempotencyKey())) > maxAIIdempotencyLength {
		return errorsx.ErrInvalidArgument
	}
	if rq.Pipeline != nil && len(strings.TrimSpace(rq.GetPipeline())) > maxAIPipelineLength {
		return errorsx.ErrInvalidArgument
	}
	if rq.Trigger != nil {
		trigger := strings.ToLower(strings.TrimSpace(rq.GetTrigger()))
		if len(trigger) > maxAITriggerLength {
			return errorsx.ErrInvalidArgument
		}
		if _, ok := allowedAIJobTriggers[trigger]; !ok {
			return errorsx.ErrInvalidArgument
		}
	}
	if rq.InputHintsJSON != nil && len(strings.TrimSpace(rq.GetInputHintsJSON())) > maxAIHintsLength {
		return errorsx.ErrInvalidArgument
	}
	start := rq.GetTimeRangeStart().AsTime()
	end := rq.GetTimeRangeEnd().AsTime()
	if err := validateRange(start, end, maxAIWindowRange); err != nil {
		return err
	}
	return nil
}

func (v *Validator) ValidateGetAIJobRequest(ctx context.Context, rq *v1.GetAIJobRequest) error {
	_ = ctx
	if strings.TrimSpace(rq.GetJobID()) == "" {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func (v *Validator) ValidateListAIJobsRequest(ctx context.Context, rq *v1.ListAIJobsRequest) error {
	_ = ctx
	status := strings.ToLower(strings.TrimSpace(rq.GetStatus()))
	if status == "" {
		status = "queued"
	}
	if _, ok := allowedAIJobQueueStatus[status]; !ok {
		return errorsx.ErrInvalidArgument
	}
	rq.Status = status

	if rq.GetOffset() < 0 {
		return errorsx.ErrInvalidArgument
	}
	if rq.GetLimit() <= 0 {
		rq.Limit = defaultAIJobQueueListLimit
	}
	if rq.GetLimit() > maxAIJobQueueListLimit {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func (v *Validator) ValidateAIJobQueueWaitSeconds(ctx context.Context, waitSeconds int64) error {
	_ = ctx
	if waitSeconds < 0 {
		return errorsx.ErrInvalidArgument
	}
	maxWaitSeconds := v.maxAIJobQueueWaitSeconds
	if maxWaitSeconds <= 0 {
		maxWaitSeconds = defaultAIJobQueueWaitSecondsMax
	}
	if waitSeconds > maxWaitSeconds {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func (v *Validator) ValidateListIncidentAIJobsRequest(ctx context.Context, rq *v1.ListIncidentAIJobsRequest) error {
	_ = ctx
	if strings.TrimSpace(rq.GetIncidentID()) == "" {
		return errorsx.ErrInvalidArgument
	}
	if rq.GetOffset() < 0 {
		return errorsx.ErrInvalidArgument
	}
	if rq.GetLimit() <= 0 {
		rq.Limit = defaultAIJobListLimit
	}
	if rq.GetLimit() > maxAIJobListLimit {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func (v *Validator) ValidateStartAIJobRequest(ctx context.Context, rq *v1.StartAIJobRequest) error {
	_ = ctx
	if strings.TrimSpace(rq.GetJobID()) == "" {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func (v *Validator) ValidateCancelAIJobRequest(ctx context.Context, rq *v1.CancelAIJobRequest) error {
	_ = ctx
	if strings.TrimSpace(rq.GetJobID()) == "" {
		return errorsx.ErrInvalidArgument
	}
	if rq.Reason != nil && len(strings.TrimSpace(rq.GetReason())) > maxAIErrorMessage {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func (v *Validator) ValidateFinalizeAIJobRequest(ctx context.Context, rq *v1.FinalizeAIJobRequest) error {
	_ = ctx
	if strings.TrimSpace(rq.GetJobID()) == "" {
		return errorsx.ErrInvalidArgument
	}
	status := strings.ToLower(strings.TrimSpace(rq.GetStatus()))
	if _, ok := allowedAIJobStatusTransitionTerminal[status]; !ok {
		return errorsx.ErrInvalidArgument
	}
	if rq.OutputSummary != nil && len(strings.TrimSpace(rq.GetOutputSummary())) > maxAIHintsLength {
		return errorsx.ErrInvalidArgument
	}
	if rq.ErrorMessage != nil && len(strings.TrimSpace(rq.GetErrorMessage())) > maxAIErrorMessage {
		return errorsx.ErrInvalidArgument
	}
	if status == "succeeded" && strings.TrimSpace(rq.GetDiagnosisJSON()) == "" {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func (v *Validator) ValidateCreateAIToolCallRequest(ctx context.Context, rq *v1.CreateAIToolCallRequest) error {
	_ = ctx
	if strings.TrimSpace(rq.GetJobID()) == "" {
		return errorsx.ErrInvalidArgument
	}
	if rq.GetSeq() <= 0 {
		return errorsx.ErrInvalidArgument
	}
	if strings.TrimSpace(rq.GetNodeName()) == "" || strings.TrimSpace(rq.GetToolName()) == "" {
		return errorsx.ErrInvalidArgument
	}
	if strings.TrimSpace(rq.GetRequestJSON()) == "" {
		return errorsx.ErrInvalidArgument
	}
	if len(rq.GetRequestJSON()) > maxToolCallJSONSize {
		return errorsx.ErrInvalidArgument
	}
	if rq.ResponseJSON != nil && len(rq.GetResponseJSON()) > maxToolCallJSONSize {
		return errorsx.ErrInvalidArgument
	}
	if rq.ResponseRef != nil && len(strings.TrimSpace(rq.GetResponseRef())) > maxToolCallRefLength {
		return errorsx.ErrInvalidArgument
	}
	if rq.GetLatencyMs() < 0 {
		return errorsx.ErrInvalidArgument
	}
	status := strings.ToLower(strings.TrimSpace(rq.GetStatus()))
	if _, ok := allowedAIToolCallStatus[status]; !ok {
		return errorsx.ErrInvalidArgument
	}
	if rq.ErrorMessage != nil && len(strings.TrimSpace(rq.GetErrorMessage())) > maxAIErrorMessage {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func (v *Validator) ValidateListAIToolCallsRequest(ctx context.Context, rq *v1.ListAIToolCallsRequest) error {
	_ = ctx
	if strings.TrimSpace(rq.GetJobID()) == "" {
		return errorsx.ErrInvalidArgument
	}
	if rq.GetOffset() < 0 {
		return errorsx.ErrInvalidArgument
	}
	if rq.GetLimit() <= 0 {
		rq.Limit = defaultToolCallList
	}
	if rq.GetLimit() > maxToolCallList {
		return errorsx.ErrInvalidArgument
	}
	if rq.Seq != nil && rq.GetSeq() <= 0 {
		return errorsx.ErrInvalidArgument
	}
	return nil
}
