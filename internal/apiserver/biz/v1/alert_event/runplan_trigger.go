package alert_event

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	triggerbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/trigger"
	alertingpolicy "github.com/aiopsre/rca-api/internal/apiserver/pkg/alerting/policy"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/metrics"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
)

var evaluateOnIngestRunPlan = alertingpolicy.Evaluate

func (b *alertEventBiz) maybeTriggerOnIngestAIJob(
	ctx context.Context,
	incidentID string,
	in *ingestInput,
	mergeResult string,
	silenced bool,
	suppressIncident bool,
) {

	if b == nil || in == nil {
		return
	}

	incidentID = strings.TrimSpace(incidentID)
	if blockByIngestPolicy(ctx, incidentID, mergeResult, silenced, suppressIncident) {
		return
	}
	if b.triggerBiz == nil || incidentID == "" {
		return
	}

	plan, ok := evaluateOnIngestPlan(ctx, incidentID, in, mergeResult, silenced, suppressIncident)
	if !ok {
		return
	}
	b.triggerOnIngestAIJob(ctx, incidentID, plan)
}

func evaluateOnIngestPlan(
	ctx context.Context,
	incidentID string,
	in *ingestInput,
	mergeResult string,
	silenced bool,
	suppressIncident bool,
) (alertingpolicy.RunPlan, bool) {

	alertTime := in.lastSeenAt.UTC()
	plan, err := evaluateOnIngestRunPlan(ctx, alertingpolicy.EvaluateInput{
		Trigger:          alertingpolicy.TriggerOnIngest,
		IncidentID:       incidentID,
		IncidentSeverity: in.severity,
		AlertName:        in.alertName,
		Labels:           parseMatchLabelsJSON(in.labelsJSON),
		AlertTime:        &alertTime,
		CreatedBy:        defaultCreatedBy,
		IdempotencyKey:   in.idempotencyKey,
	})
	if err != nil {
		slog.WarnContext(ctx, "on_ingest run plan evaluate failed",
			"request_id", contextx.RequestID(ctx),
			"incident_id", incidentID,
			"trigger", alertingpolicy.TriggerOnIngest,
			"decision", alertingpolicyDecisionError,
			"merge_result", mergeResult,
			"silenced", silenced,
			"suppress_incident", suppressIncident,
			"error", err,
		)
		return alertingpolicy.RunPlan{}, false
	}
	if !plan.ShouldRun {
		slog.InfoContext(ctx, "on_ingest run plan skipped",
			"request_id", contextx.RequestID(ctx),
			"incident_id", incidentID,
			"trigger", plan.Trigger,
			"decision", plan.Decision,
			"rule", plan.RuleName,
			"policy_source", plan.PolicySource,
		)
		return alertingpolicy.RunPlan{}, false
	}
	return plan, true
}

func (b *alertEventBiz) triggerOnIngestAIJob(ctx context.Context, incidentID string, plan alertingpolicy.RunPlan) {
	runReq, err := plan.ToRunAIJobRequest(incidentID)
	if err != nil {
		slog.WarnContext(ctx, "on_ingest run plan build request failed",
			"request_id", contextx.RequestID(ctx),
			"incident_id", incidentID,
			"trigger", plan.Trigger,
			"decision", alertingpolicyDecisionError,
			"rule", plan.RuleName,
			"policy_source", plan.PolicySource,
			"error", err,
		)
		return
	}

	triggerResp, err := b.triggerBiz.Dispatch(ctx, &triggerbiz.TriggerRequest{
		TriggerType: triggerbiz.TriggerTypeAlert,
		Source:      "alert_ingest",
		BusinessKey: strings.TrimSpace(incidentID),
		IncidentHint: &triggerbiz.IncidentHint{
			IncidentID: incidentID,
		},
		Payload: map[string]any{
			"trigger":       plan.Trigger,
			"decision":      plan.Decision,
			"rule_name":     plan.RuleName,
			"policy_source": plan.PolicySource,
		},
		DesiredPipeline: runReq.Pipeline,
		TimeRange: &triggerbiz.TriggerTimeRange{
			Start: plan.TimeRangeStart,
			End:   plan.TimeRangeEnd,
		},
		RunRequest: runReq,
	})
	if err != nil {
		if errors.Is(err, errno.ErrAIJobAlreadyRunning) {
			slog.InfoContext(ctx, "on_ingest run plan already running",
				"request_id", contextx.RequestID(ctx),
				"incident_id", incidentID,
				"trigger", plan.Trigger,
				"decision", alertingpolicyDecisionAlreadyRunning,
				"rule", plan.RuleName,
				"policy_source", plan.PolicySource,
			)
			return
		}
		slog.WarnContext(ctx, "on_ingest run plan trigger failed",
			"request_id", contextx.RequestID(ctx),
			"incident_id", incidentID,
			"trigger", plan.Trigger,
			"decision", alertingpolicyDecisionError,
			"rule", plan.RuleName,
			"policy_source", plan.PolicySource,
			"error", err,
		)
		return
	}
	if triggerResp == nil {
		slog.WarnContext(ctx, "on_ingest run plan trigger returned empty response",
			"request_id", contextx.RequestID(ctx),
			"incident_id", incidentID,
			"trigger", plan.Trigger,
			"rule", plan.RuleName,
			"policy_source", plan.PolicySource,
		)
		return
	}

	slog.InfoContext(ctx, "on_ingest run plan triggered",
		"request_id", contextx.RequestID(ctx),
		"incident_id", incidentID,
		"job_id", triggerResp.JobID,
		"trigger", plan.Trigger,
		"decision", plan.Decision,
		"rule", plan.RuleName,
		"policy_source", plan.PolicySource,
	)
}

func blockByIngestPolicy(
	ctx context.Context,
	incidentID string,
	mergeResult string,
	silenced bool,
	suppressIncident bool,
) bool {

	decision, blocked := resolveOnIngestBlockedDecision(silenced, suppressIncident)
	if !blocked {
		return false
	}

	recordOnIngestTriggerDecision(decision)
	slog.InfoContext(ctx, "on_ingest run plan blocked",
		"request_id", contextx.RequestID(ctx),
		"incident_id", incidentID,
		"trigger", alertingpolicy.TriggerOnIngest,
		"decision", decision,
		"merge_result", mergeResult,
		"silenced", silenced,
		"suppress_incident", suppressIncident,
	)
	return true
}

func resolveOnIngestBlockedDecision(silenced bool, suppressIncident bool) (string, bool) {
	if silenced {
		return alertingpolicyDecisionBlockedSilenced, true
	}
	if suppressIncident {
		return alertingpolicyDecisionBlockedSuppressIncident, true
	}
	return "", false
}

func recordOnIngestTriggerDecision(decision string) {
	if metrics.M == nil {
		return
	}
	metrics.M.RecordAlertIngestPolicyDecision(decision, "on_ingest_trigger")
}

func parseMatchLabelsJSON(raw *string) map[string]string {
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

const (
	defaultCreatedBy                              = "system"
	alertingpolicyDecisionAlreadyRunning          = "already_running"
	alertingpolicyDecisionBlockedSilenced         = "blocked_silenced"
	alertingpolicyDecisionBlockedSuppressIncident = "blocked_suppress_incident"
	alertingpolicyDecisionError                   = "error"
)
