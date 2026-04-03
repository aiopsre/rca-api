package policy

import (
	"errors"
	"fmt"
	"strings"
	"time"

	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	TriggerManual       = "manual"
	TriggerOnIngest     = "on_ingest"
	TriggerOnEscalation = "on_escalation"
	TriggerScheduled    = "scheduled"
)

const (
	planDecisionRun            = "run"
	planDecisionSkipRule       = "skip_rule"
	planDecisionSkipDefault    = "skip_default"
	planDecisionAlreadyRunning = "already_running"
	planDecisionError          = "error"
)

const (
	defaultOnIngestWindowSeconds     = 60 * 60
	defaultOnEscalationWindowSeconds = 120 * 60
	defaultScheduledWindowSeconds    = 60 * 60
	defaultCreatedBySystem           = "system"
)

var (
	errInvalidAutoTrigger = errors.New("invalid auto trigger")
	errInvalidIncidentID  = errors.New("invalid incident id")
	errInvalidTimeRange   = errors.New("invalid run plan time range")
)

// RunPlan is the evaluated upstream plan for deciding whether to run one AIJob.
type RunPlan struct {
	ShouldRun    bool
	Decision     string
	Trigger      string
	Pipeline     string
	CreatedBy    string
	RuleName     string
	PolicySource string

	TimeRangeStart time.Time
	TimeRangeEnd   time.Time

	IdempotencyKey         *string
	IdempotencyBucketStart *time.Time
	IdempotencyBucketSecs  int
	InputHintsJSON         *string
}

// EvaluateInput is policy evaluation input from one trigger entrypoint.
type EvaluateInput struct {
	Trigger string

	IncidentID       string
	IncidentSeverity string
	AlertName        string
	Labels           map[string]string
	AlertTime        *time.Time

	SchedulerName string
	CreatedBy     string

	IdempotencyKey string
	InputHintsJSON string

	Now time.Time
}

// ToRunAIJobRequest converts one RunPlan to RunAIJobRequest used by AIJob.Run().
func (p RunPlan) ToRunAIJobRequest(incidentID string) (*v1.RunAIJobRequest, error) {
	trigger, err := normalizeAutoTrigger(p.Trigger)
	if err != nil {
		return nil, err
	}
	incidentID = strings.TrimSpace(incidentID)
	if incidentID == "" {
		return nil, fmt.Errorf("%w: missing incident id", errInvalidIncidentID)
	}
	start := p.TimeRangeStart.UTC()
	end := p.TimeRangeEnd.UTC()
	if start.IsZero() || end.IsZero() || start.After(end) {
		return nil, errInvalidTimeRange
	}

	pipeline := strings.TrimSpace(p.Pipeline)
	if pipeline == "" {
		pipeline = defaultPipeline
	}
	createdBy := strings.TrimSpace(p.CreatedBy)
	if createdBy == "" {
		createdBy = defaultCreatedByForTrigger(trigger, "")
	}

	req := &v1.RunAIJobRequest{
		IncidentID:     incidentID,
		Pipeline:       strPtr(pipeline),
		Trigger:        strPtr(trigger),
		CreatedBy:      strPtr(createdBy),
		TimeRangeStart: timestamppb.New(start),
		TimeRangeEnd:   timestamppb.New(end),
		IdempotencyKey: normalizeOptionalStringPtrValue(p.IdempotencyKey),
		InputHintsJSON: normalizeOptionalStringPtrValue(p.InputHintsJSON),
	}
	return req, nil
}

func defaultCreatedByForTrigger(trigger string, schedulerName string) string {
	trigger = strings.ToLower(strings.TrimSpace(trigger))
	if trigger != TriggerScheduled {
		return defaultCreatedBySystem
	}
	schedulerName = strings.TrimSpace(schedulerName)
	if schedulerName == "" {
		return defaultCreatedBySystem
	}
	return "scheduler:" + schedulerName
}

func normalizeAutoTrigger(trigger string) (string, error) {
	trigger = strings.ToLower(strings.TrimSpace(trigger))
	switch trigger {
	case TriggerOnIngest:
		return TriggerOnIngest, nil
	case TriggerOnEscalation:
		return TriggerOnEscalation, nil
	case TriggerScheduled:
		return TriggerScheduled, nil
	default:
		return "", fmt.Errorf("%w: %q", errInvalidAutoTrigger, trigger)
	}
}

func normalizeOptionalStringPtrValue(raw *string) *string {
	if raw == nil {
		return nil
	}
	clean := strings.TrimSpace(*raw)
	if clean == "" {
		return nil
	}
	return strPtr(clean)
}

func normalizeOptionalStringPtr(raw string) *string {
	clean := strings.TrimSpace(raw)
	if clean == "" {
		return nil
	}
	return strPtr(clean)
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	value := s
	return &value
}
