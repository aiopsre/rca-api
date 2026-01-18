package alert_event

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	alertingpolicy "github.com/aiopsre/rca-api/internal/apiserver/pkg/alerting/policy"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
)

func (b *alertEventBiz) maybeTriggerOnIngestAIJob(
	ctx context.Context,
	incidentID string,
	in *ingestInput,
	mergeResult string,
	silenced bool,
	suppressIncident bool,
) {

	if b == nil || b.runAIJobBiz == nil || in == nil {
		return
	}
	incidentID = strings.TrimSpace(incidentID)
	if incidentID == "" {
		return
	}

	alertTime := in.lastSeenAt.UTC()
	plan, err := alertingpolicy.Evaluate(ctx, alertingpolicy.EvaluateInput{
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
		return
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
		return
	}

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

	runResp, err := b.runAIJobBiz.Run(ctx, runReq)
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

	slog.InfoContext(ctx, "on_ingest run plan triggered",
		"request_id", contextx.RequestID(ctx),
		"incident_id", incidentID,
		"job_id", runResp.GetJobID(),
		"trigger", plan.Trigger,
		"decision", plan.Decision,
		"rule", plan.RuleName,
		"policy_source", plan.PolicySource,
	)
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
	defaultCreatedBy                     = "system"
	alertingpolicyDecisionAlreadyRunning = "already_running"
	alertingpolicyDecisionError          = "error"
)
