package incident

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/onexstack/onexstack/pkg/errorsx"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	alertingpolicy "github.com/aiopsre/rca-api/internal/apiserver/pkg/alerting/policy"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

const (
	triggerDecisionAlreadyRunning = "already_running"
	triggerDecisionError          = "error"
)

// TriggerScheduledRunRequest represents the scheduled trigger entrypoint request.
type TriggerScheduledRunRequest struct {
	IncidentID     string  `json:"incidentID" uri:"incidentID"`
	SchedulerName  *string `json:"schedulerName,omitempty"`
	IdempotencyKey *string `json:"idempotencyKey,omitempty"`
	InputHintsJSON *string `json:"inputHintsJSON,omitempty"`
	CreatedBy      *string `json:"createdBy,omitempty"`
}

// TriggerScheduledRunResponse represents the scheduled trigger decision/result.
type TriggerScheduledRunResponse struct {
	ShouldRun      bool                   `json:"shouldRun"`
	Decision       string                 `json:"decision"`
	Trigger        string                 `json:"trigger"`
	Pipeline       string                 `json:"pipeline"`
	CreatedBy      string                 `json:"createdBy"`
	RuleName       string                 `json:"ruleName,omitempty"`
	PolicySource   string                 `json:"policySource,omitempty"`
	JobID          *string                `json:"jobID,omitempty"`
	IdempotencyKey *string                `json:"idempotencyKey,omitempty"`
	TimeRangeStart *timestamppb.Timestamp `json:"timeRangeStart,omitempty"`
	TimeRangeEnd   *timestamppb.Timestamp `json:"timeRangeEnd,omitempty"`
}

func (b *incidentBiz) maybeTriggerOnEscalationAIJob(ctx context.Context, incident *model.IncidentM, oldSeverity string) {
	if b == nil || b.runAIJobBiz == nil || incident == nil {
		return
	}
	if !isSeverityEscalated(oldSeverity, incident.Severity) {
		return
	}

	plan, err := b.evaluateRunPlan(ctx, alertingpolicy.TriggerOnEscalation, incident, runPlanOverrides{})
	if err != nil {
		slog.WarnContext(ctx, "on_escalation run plan evaluate failed",
			"request_id", contextx.RequestID(ctx),
			"incident_id", incident.IncidentID,
			"trigger", alertingpolicy.TriggerOnEscalation,
			"decision", triggerDecisionError,
			"error", err,
		)
		return
	}
	if !plan.ShouldRun {
		slog.InfoContext(ctx, "on_escalation run plan skipped",
			"request_id", contextx.RequestID(ctx),
			"incident_id", incident.IncidentID,
			"trigger", plan.Trigger,
			"decision", plan.Decision,
			"rule", plan.RuleName,
			"policy_source", plan.PolicySource,
		)
		return
	}

	runReq, err := plan.ToRunAIJobRequest(incident.IncidentID)
	if err != nil {
		slog.WarnContext(ctx, "on_escalation run plan build request failed",
			"request_id", contextx.RequestID(ctx),
			"incident_id", incident.IncidentID,
			"trigger", plan.Trigger,
			"decision", triggerDecisionError,
			"rule", plan.RuleName,
			"policy_source", plan.PolicySource,
			"error", err,
		)
		return
	}

	runResp, err := b.runAIJobBiz.Run(ctx, runReq)
	if err != nil {
		if errors.Is(err, errno.ErrAIJobAlreadyRunning) {
			slog.InfoContext(ctx, "on_escalation run plan already running",
				"request_id", contextx.RequestID(ctx),
				"incident_id", incident.IncidentID,
				"trigger", plan.Trigger,
				"decision", triggerDecisionAlreadyRunning,
				"rule", plan.RuleName,
				"policy_source", plan.PolicySource,
			)
			return
		}
		slog.WarnContext(ctx, "on_escalation run plan trigger failed",
			"request_id", contextx.RequestID(ctx),
			"incident_id", incident.IncidentID,
			"trigger", plan.Trigger,
			"decision", triggerDecisionError,
			"rule", plan.RuleName,
			"policy_source", plan.PolicySource,
			"error", err,
		)
		return
	}

	slog.InfoContext(ctx, "on_escalation run plan triggered",
		"request_id", contextx.RequestID(ctx),
		"incident_id", incident.IncidentID,
		"job_id", runResp.GetJobID(),
		"trigger", plan.Trigger,
		"decision", plan.Decision,
		"rule", plan.RuleName,
		"policy_source", plan.PolicySource,
	)
}

// TriggerScheduledRun triggers one scheduled RunPlan evaluation and optional AIJob creation.
func (b *incidentBiz) TriggerScheduledRun(
	ctx context.Context,
	rq *TriggerScheduledRunRequest,
) (*TriggerScheduledRunResponse, error) {

	incidentID, err := validateScheduledRunRequest(b, rq)
	if err != nil {
		return nil, err
	}

	incident, err := b.getIncidentByID(ctx, incidentID)
	if err != nil {
		return nil, err
	}

	plan, err := b.evaluateScheduledRunPlan(ctx, incident, rq)
	if err != nil {
		return nil, errorsx.ErrInvalidArgument
	}

	resp := triggerScheduledResponseFromPlan(plan)
	if !plan.ShouldRun {
		return resp, nil
	}
	return b.runScheduledPlan(ctx, incidentID, plan, resp)
}

type runPlanOverrides struct {
	SchedulerName  string
	IdempotencyKey string
	InputHintsJSON string
	CreatedBy      string
}

func (b *incidentBiz) evaluateRunPlan(
	ctx context.Context,
	trigger string,
	incident *model.IncidentM,
	overrides runPlanOverrides,
) (alertingpolicy.RunPlan, error) {

	if incident == nil {
		return alertingpolicy.RunPlan{}, errorsx.ErrInvalidArgument
	}
	plan, err := alertingpolicy.Evaluate(ctx, alertingpolicy.EvaluateInput{
		Trigger:          trigger,
		IncidentID:       incident.IncidentID,
		IncidentSeverity: incident.Severity,
		AlertName:        derefString(incident.AlertName),
		Labels:           parseIncidentLabelsJSON(incident.LabelsJSON),
		SchedulerName:    overrides.SchedulerName,
		IdempotencyKey:   overrides.IdempotencyKey,
		InputHintsJSON:   overrides.InputHintsJSON,
		CreatedBy:        overrides.CreatedBy,
	})
	if err != nil {
		return alertingpolicy.RunPlan{}, err
	}
	return plan, nil
}

func validateScheduledRunRequest(b *incidentBiz, rq *TriggerScheduledRunRequest) (string, error) {
	if b == nil || b.runAIJobBiz == nil || rq == nil {
		return "", errorsx.ErrInvalidArgument
	}
	incidentID := strings.TrimSpace(rq.IncidentID)
	if incidentID == "" {
		return "", errorsx.ErrInvalidArgument
	}
	return incidentID, nil
}

func (b *incidentBiz) getIncidentByID(ctx context.Context, incidentID string) (*model.IncidentM, error) {
	incident, err := b.store.Incident().Get(ctx, where.T(ctx).F("incident_id", incidentID))
	if err == nil {
		return incident, nil
	}
	if errorsx.Is(err, gorm.ErrRecordNotFound) {
		return nil, errno.ErrIncidentNotFound
	}
	return nil, errno.ErrIncidentGetFailed
}

func (b *incidentBiz) evaluateScheduledRunPlan(
	ctx context.Context,
	incident *model.IncidentM,
	rq *TriggerScheduledRunRequest,
) (alertingpolicy.RunPlan, error) {

	return b.evaluateRunPlan(ctx, alertingpolicy.TriggerScheduled, incident, runPlanOverrides{
		SchedulerName:  trimOptionalString(rq.SchedulerName),
		IdempotencyKey: trimOptionalString(rq.IdempotencyKey),
		InputHintsJSON: trimOptionalString(rq.InputHintsJSON),
		CreatedBy:      trimOptionalString(rq.CreatedBy),
	})
}

func (b *incidentBiz) runScheduledPlan(
	ctx context.Context,
	incidentID string,
	plan alertingpolicy.RunPlan,
	resp *TriggerScheduledRunResponse,
) (*TriggerScheduledRunResponse, error) {

	runReq, err := plan.ToRunAIJobRequest(incidentID)
	if err != nil {
		return nil, errorsx.ErrInvalidArgument
	}
	runResp, err := b.runAIJobBiz.Run(ctx, runReq)
	if err != nil {
		if errors.Is(err, errno.ErrAIJobAlreadyRunning) {
			resp.Decision = triggerDecisionAlreadyRunning
			return resp, nil
		}
		return nil, err
	}
	resp.JobID = strPtr(runResp.GetJobID())
	return resp, nil
}

func triggerScheduledResponseFromPlan(plan alertingpolicy.RunPlan) *TriggerScheduledRunResponse {
	resp := &TriggerScheduledRunResponse{
		ShouldRun:      plan.ShouldRun,
		Decision:       plan.Decision,
		Trigger:        plan.Trigger,
		Pipeline:       plan.Pipeline,
		CreatedBy:      plan.CreatedBy,
		RuleName:       plan.RuleName,
		PolicySource:   plan.PolicySource,
		IdempotencyKey: plan.IdempotencyKey,
	}
	if !plan.TimeRangeStart.IsZero() {
		resp.TimeRangeStart = timestamppb.New(plan.TimeRangeStart.UTC())
	}
	if !plan.TimeRangeEnd.IsZero() {
		resp.TimeRangeEnd = timestamppb.New(plan.TimeRangeEnd.UTC())
	}
	return resp
}

func parseIncidentLabelsJSON(raw *string) map[string]string {
	trimmed := strings.TrimSpace(derefString(raw))
	if trimmed == "" {
		return nil
	}
	payload := map[string]any{}
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return nil
	}
	out := make(map[string]string, len(payload))
	for key, value := range payload {
		cleanKey := strings.TrimSpace(key)
		if cleanKey == "" || value == nil {
			continue
		}
		out[cleanKey] = strings.TrimSpace(fmt.Sprint(value))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func isSeverityEscalated(oldSeverity string, newSeverity string) bool {
	return severityRank(newSeverity) > severityRank(oldSeverity)
}

func severityRank(severity string) int {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "p0", "critical", "sev0":
		return 400
	case "p1", "high", "sev1":
		return 300
	case "p2", "warn", "warning", "sev2":
		return 200
	case "p3", "low", "info", "sev3":
		return 100
	default:
		return 0
	}
}

func strPtr(v string) *string {
	clean := strings.TrimSpace(v)
	if clean == "" {
		return nil
	}
	value := clean
	return &value
}

func derefString(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}
