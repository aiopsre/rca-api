package policy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Evaluate evaluates runtime policy rules for one trigger entrypoint and returns RunPlan.
func Evaluate(_ context.Context, in EvaluateInput) (RunPlan, error) {
	trigger, err := normalizeAutoTrigger(in.Trigger)
	if err != nil {
		return RunPlan{}, err
	}

	runtimeCfg := CurrentRuntimeConfig()
	policyCfg := runtimeCfg.Policy
	policyCfg.applyDefaults()

	now := resolveNowUTC(in.Now)
	plan := newDefaultPlan(trigger, runtimeCfg.Source, in)
	defaultCfg, triggerRules := selectTriggerConfig(policyCfg, trigger)
	action := applyRuleDecision(&plan, defaultCfg, triggerRules.Rules, in)
	applyPipeline(&plan, action)
	bucketStart, bucketSeconds := applyTimeRange(&plan, trigger, now, in.AlertTime, action)
	applyScheduledIdempotency(&plan, trigger, in, bucketStart, bucketSeconds)

	return plan, nil
}

func resolveNowUTC(now time.Time) time.Time {
	now = now.UTC()
	if now.IsZero() {
		return time.Now().UTC()
	}
	return now
}

func newDefaultPlan(trigger string, source string, in EvaluateInput) RunPlan {
	return RunPlan{
		ShouldRun:      false,
		Decision:       planDecisionSkipDefault,
		Trigger:        trigger,
		Pipeline:       defaultPipeline,
		CreatedBy:      resolveCreatedBy(trigger, in.CreatedBy, in.SchedulerName),
		PolicySource:   source,
		InputHintsJSON: normalizeOptionalStringPtr(in.InputHintsJSON),
		IdempotencyKey: normalizeOptionalStringPtr(in.IdempotencyKey),
	}
}

func applyRuleDecision(plan *RunPlan, defaults TriggerDefaults, rules []TriggerRule, in EvaluateInput) TriggerAction {
	matchedRule, matched := firstMatchedRule(rules, in)
	if !matched {
		if defaults.Enabled {
			plan.ShouldRun = true
			plan.Decision = planDecisionRun
		}
		return TriggerAction{}
	}

	plan.RuleName = matchedRule.Name
	action := matchedRule.Action
	plan.ShouldRun = action.Run
	if plan.ShouldRun {
		plan.Decision = planDecisionRun
		return action
	}
	plan.Decision = planDecisionSkipRule
	return action
}

func applyPipeline(plan *RunPlan, action TriggerAction) {
	pipeline := strings.TrimSpace(action.Pipeline)
	if pipeline != "" {
		plan.Pipeline = pipeline
	}
	if strings.TrimSpace(plan.Pipeline) == "" {
		plan.Pipeline = defaultPipeline
	}
}

func applyTimeRange(
	plan *RunPlan,
	trigger string,
	now time.Time,
	alertTime *time.Time,
	action TriggerAction,
) (time.Time, int) {

	windowSeconds := resolveWindowSeconds(trigger, action.WindowSeconds)
	start, end, bucketStart, bucketSeconds := resolveTimeRange(
		trigger,
		now,
		alertTime,
		windowSeconds,
		action.IdempotencyBucketSeconds,
	)
	plan.TimeRangeStart = start
	plan.TimeRangeEnd = end
	plan.IdempotencyBucketSecs = bucketSeconds
	if !bucketStart.IsZero() {
		plan.IdempotencyBucketStart = &bucketStart
	}
	return bucketStart, bucketSeconds
}

func applyScheduledIdempotency(
	plan *RunPlan,
	trigger string,
	in EvaluateInput,
	bucketStart time.Time,
	bucketSeconds int,
) {

	if trigger != TriggerScheduled {
		return
	}
	key := buildScheduledIdempotencyKey(
		strings.TrimSpace(in.IncidentID),
		strings.TrimSpace(in.SchedulerName),
		plan.Pipeline,
		plan.RuleName,
		bucketStart,
		bucketSeconds,
	)
	plan.IdempotencyKey = strPtr(key)
}

func resolveCreatedBy(trigger string, createdBy string, schedulerName string) string {
	createdBy = strings.TrimSpace(createdBy)
	if createdBy != "" {
		return createdBy
	}
	return defaultCreatedByForTrigger(trigger, schedulerName)
}

func selectTriggerConfig(policyCfg PolicyConfig, trigger string) (TriggerDefaults, TriggerRules) {
	switch trigger {
	case TriggerOnIngest:
		return policyCfg.Defaults.OnIngest, policyCfg.Triggers.OnIngest
	case TriggerOnEscalation:
		return policyCfg.Defaults.OnEscalation, policyCfg.Triggers.OnEscalation
	case TriggerScheduled:
		return policyCfg.Defaults.Scheduled, policyCfg.Triggers.Scheduled
	default:
		return TriggerDefaults{}, TriggerRules{}
	}
}

func firstMatchedRule(rules []TriggerRule, in EvaluateInput) (TriggerRule, bool) {
	for _, rule := range rules {
		if ruleMatches(rule.Match, in) {
			return rule, true
		}
	}
	return TriggerRule{}, false
}

func ruleMatches(match RuleMatch, in EvaluateInput) bool {
	alertName := strings.TrimSpace(in.AlertName)
	if v := strings.TrimSpace(match.AlertName); v != "" && alertName != v {
		return false
	}

	if expr := strings.TrimSpace(match.AlertNameRegex); expr != "" {
		re, err := regexp.Compile(expr)
		if err != nil {
			return false
		}
		if !re.MatchString(alertName) {
			return false
		}
	}

	if !labelsMatch(match.Labels, in.Labels) {
		return false
	}
	if !severityMatch(match.IncidentSeverity, in.IncidentSeverity) {
		return false
	}
	return true
}

func labelsMatch(expect map[string][]string, actual map[string]string) bool {
	if len(expect) == 0 {
		return true
	}
	for key, values := range expect {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		got := ""
		if actual != nil {
			got = strings.TrimSpace(actual[key])
		}
		if got == "" {
			return false
		}
		if !containsMatchValue(values, got) {
			return false
		}
	}
	return true
}

func containsMatchValue(values []string, got string) bool {
	if len(values) == 0 {
		return true
	}
	for _, value := range values {
		if strings.TrimSpace(value) == got {
			return true
		}
	}
	return false
}

func severityMatch(expect []string, got string) bool {
	if len(expect) == 0 {
		return true
	}
	got = strings.ToLower(strings.TrimSpace(got))
	if got == "" {
		return false
	}
	for _, item := range expect {
		if strings.ToLower(strings.TrimSpace(item)) == got {
			return true
		}
	}
	return false
}

func resolveWindowSeconds(trigger string, override int) int {
	if override > 0 {
		return override
	}
	switch trigger {
	case TriggerOnIngest:
		return defaultOnIngestWindowSeconds
	case TriggerOnEscalation:
		return defaultOnEscalationWindowSeconds
	case TriggerScheduled:
		return defaultScheduledWindowSeconds
	default:
		return defaultOnIngestWindowSeconds
	}
}

func resolveTimeRange(
	trigger string,
	now time.Time,
	alertTime *time.Time,
	windowSeconds int,
	idempotencyBucketSeconds int,
) (time.Time, time.Time, time.Time, int) {

	windowSeconds = ensureWindowSeconds(trigger, windowSeconds)
	switch trigger {
	case TriggerOnIngest:
		return resolveOnIngestTimeRange(now, alertTime, windowSeconds)
	case TriggerOnEscalation:
		return resolveSimpleTimeRange(now, windowSeconds)
	case TriggerScheduled:
		return resolveScheduledTimeRange(now, windowSeconds, idempotencyBucketSeconds)
	default:
		return resolveSimpleTimeRange(now, windowSeconds)
	}
}

func ensureWindowSeconds(trigger string, windowSeconds int) int {
	if windowSeconds > 0 {
		return windowSeconds
	}
	return resolveWindowSeconds(trigger, 0)
}

func resolveSimpleTimeRange(now time.Time, windowSeconds int) (time.Time, time.Time, time.Time, int) {
	window := time.Duration(windowSeconds) * time.Second
	end := now
	return end.Add(-window), end, time.Time{}, 0
}

func resolveOnIngestTimeRange(now time.Time, alertTime *time.Time, windowSeconds int) (time.Time, time.Time, time.Time, int) {
	window := time.Duration(windowSeconds) * time.Second
	end := now
	if alertTime != nil {
		at := alertTime.UTC()
		if !at.IsZero() {
			end = at
		}
	}
	return end.Add(-window), end, time.Time{}, 0
}

func resolveScheduledTimeRange(
	now time.Time,
	windowSeconds int,
	idempotencyBucketSeconds int,
) (time.Time, time.Time, time.Time, int) {

	bucketSeconds := normalizeBucketSeconds(windowSeconds, idempotencyBucketSeconds)
	windowSeconds = alignWindowSeconds(windowSeconds, bucketSeconds)
	window := time.Duration(windowSeconds) * time.Second
	bucketDuration := time.Duration(bucketSeconds) * time.Second
	end := alignTimeDown(now, bucketDuration)
	start := end.Add(-window)
	return start, end, end, bucketSeconds
}

func normalizeBucketSeconds(windowSeconds int, idempotencyBucketSeconds int) int {
	if idempotencyBucketSeconds > 0 {
		return idempotencyBucketSeconds
	}
	if windowSeconds > 0 {
		return windowSeconds
	}
	return defaultScheduledWindowSeconds
}

func alignWindowSeconds(windowSeconds int, bucketSeconds int) int {
	if windowSeconds <= 0 {
		return bucketSeconds
	}
	if bucketSeconds <= 0 {
		return windowSeconds
	}
	if windowSeconds%bucketSeconds == 0 {
		return windowSeconds
	}
	return bucketSeconds
}

func alignTimeDown(ts time.Time, step time.Duration) time.Time {
	if step <= 0 {
		return ts.UTC()
	}
	unix := ts.UTC().Unix()
	span := int64(step / time.Second)
	if span <= 0 {
		return ts.UTC()
	}
	aligned := unix - (unix % span)
	return time.Unix(aligned, 0).UTC()
}

func buildScheduledIdempotencyKey(
	incidentID string,
	schedulerName string,
	pipeline string,
	ruleName string,
	bucketStart time.Time,
	bucketSeconds int,
) string {

	if pipeline == "" {
		pipeline = defaultPipeline
	}
	raw := fmt.Sprintf(
		"incident=%s|scheduler=%s|pipeline=%s|rule=%s|bucket_start=%d|bucket_seconds=%d",
		incidentID,
		schedulerName,
		pipeline,
		ruleName,
		bucketStart.UTC().Unix(),
		bucketSeconds,
	)
	sum := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("auto-scheduled-%d-%s", bucketStart.UTC().Unix(), hex.EncodeToString(sum[:8]))
}
