package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	reDigits      = regexp.MustCompile(`\b\d+\b`)
	reHex         = regexp.MustCompile(`\b(?:0x)?[a-fA-F0-9]{8,}\b`)
	reUUID        = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}\b`)
	reIPv4        = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	reWhitespaces = regexp.MustCompile(`\s+`)
	errTickFailed = errors.New("tick failed")
)

type alertCluster struct {
	Key         string
	GroupValues map[string]string
	Count       int
	FirstSeen   string
	LastSeen    string
	TraceIDs    []string
	RequestIDs  []string
	MsgExamples []string
}

type clusterAggregate struct {
	GroupValues    map[string]string
	Count          int
	FirstSeenTime  time.Time
	LastSeenTime   time.Time
	TraceCounter   map[string]int
	RequestCounter map[string]int
	ExampleCounter map[string]int
}

type jobRunner struct {
	cfg     config
	es      *esClient
	webhook *webhookClient
	metrics *jobMetrics
	logger  *slog.Logger

	cooldownMu sync.Mutex
	cooldown   map[string]time.Time
	nowFn      func() time.Time
}

func newJobRunner(cfg config, es *esClient, webhook *webhookClient, metrics *jobMetrics, logger *slog.Logger) *jobRunner {
	return &jobRunner{
		cfg:      cfg,
		es:       es,
		webhook:  webhook,
		metrics:  metrics,
		logger:   logger,
		cooldown: make(map[string]time.Time),
		nowFn:    func() time.Time { return time.Now().UTC() },
	}
}

//nolint:gocognit
func (r *jobRunner) run(ctx context.Context, opts runtimeOptions) error {
	tickInterval := time.Duration(r.cfg.Job.TickSeconds) * time.Second
	if opts.TickSeconds > 0 {
		tickInterval = time.Duration(opts.TickSeconds) * time.Second
	}
	if tickInterval <= 0 {
		tickInterval = time.Duration(defaultTickSeconds) * time.Second
	}

	tickCount := 0
	for {
		if err := r.runTick(ctx); err != nil {
			r.logger.WarnContext(ctx, "tick completed with rule failures", slog.String("error", err.Error()))
		}
		tickCount++
		if opts.Once {
			return nil
		}
		if opts.MaxTicks > 0 && tickCount >= opts.MaxTicks {
			return nil
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(tickInterval):
		}
	}
}

//nolint:gocognit
func (r *jobRunner) runTick(ctx context.Context) error {
	result := "ok"
	hasFailure := false
	defer func() {
		r.metrics.recordTick(result)
	}()

	enabledRules := sortedEnabledRules(r.cfg.Rules)
	for _, rule := range enabledRules {
		indexPattern := strings.TrimSpace(r.cfg.Indices[rule.IndexRef])
		window := time.Duration(rule.WindowSeconds) * time.Second
		if window <= 0 {
			window = time.Duration(defaultWindowSeconds) * time.Second
		}

		limit := r.cfg.Job.MaxDocsPerRule
		if limit <= 0 {
			limit = defaultMaxDocsPerRule
		}

		documents, code, err := r.es.search(
			ctx,
			rule.ID,
			indexPattern,
			rule.Selector.QueryString,
			r.cfg.Fields.Timestamp,
			window,
			limit,
		)
		r.metrics.recordRuleQuery(rule.ID, code)
		if err != nil {
			hasFailure = true
			r.logger.WarnContext(ctx, "rule query failed",
				slog.String("rule_id", rule.ID),
				slog.String("error", err.Error()),
				slog.String("index_pattern", indexPattern),
			)
			continue
		}

		clusters := r.buildClusters(rule, documents)
		r.metrics.recordClusters(rule.ID, len(clusters))
		for _, cluster := range clusters {
			if cluster.Count < rule.Trigger.Value {
				continue
			}
			if r.inCooldown(rule, cluster.Key) {
				r.metrics.recordCooldownSuppressed(rule.ID)
				continue
			}

			payload := buildWebhookPayload(r.cfg, rule, cluster, indexPattern)
			statusCode, latency, sendErr := r.webhook.send(ctx, payload)
			webhookCode := classifyWebhookCode(statusCode, sendErr)
			r.metrics.recordWebhook(rule.ID, webhookCode, latency)
			if sendErr != nil {
				hasFailure = true
				r.logger.WarnContext(ctx, "webhook fire failed",
					slog.String("rule_id", rule.ID),
					slog.String("fingerprint", payload.Fingerprint),
					slog.String("error", sendErr.Error()),
				)
				continue
			}

			r.markCooldown(rule, cluster.Key)
			r.metrics.recordFire(rule.ID)
			r.logger.InfoContext(ctx, "cluster fired",
				slog.String("rule_id", rule.ID),
				slog.String("fingerprint", payload.Fingerprint),
				slog.Int("count", cluster.Count),
			)
		}
	}

	if hasFailure {
		result = "failed"
		return fmt.Errorf("%w: at least one rule query or webhook failed", errTickFailed)
	}

	return nil
}

func sortedEnabledRules(rules []ruleConfig) []ruleConfig {
	enabledRules := make([]ruleConfig, 0, len(rules))
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		enabledRules = append(enabledRules, rule)
	}
	sort.Slice(enabledRules, func(left int, right int) bool {
		return enabledRules[left].ID < enabledRules[right].ID
	})
	return enabledRules
}

func classifyWebhookCode(statusCode int, err error) string {
	if statusCode > 0 {
		return strconv.Itoa(statusCode)
	}
	if err != nil {
		return "request_error"
	}
	return "unknown"
}

func (r *jobRunner) inCooldown(rule ruleConfig, clusterKey string) bool {
	if rule.CooldownSeconds <= 0 {
		return false
	}

	r.cooldownMu.Lock()
	defer r.cooldownMu.Unlock()

	key := r.cooldownKey(rule.ID, clusterKey)
	lastFireTime, ok := r.cooldown[key]
	if !ok {
		return false
	}

	cooldownWindow := time.Duration(rule.CooldownSeconds) * time.Second
	return r.nowFn().Sub(lastFireTime) < cooldownWindow
}

func (r *jobRunner) markCooldown(rule ruleConfig, clusterKey string) {
	if rule.CooldownSeconds <= 0 {
		return
	}

	r.cooldownMu.Lock()
	defer r.cooldownMu.Unlock()

	r.cooldown[r.cooldownKey(rule.ID, clusterKey)] = r.nowFn()
}

func (r *jobRunner) cooldownKey(ruleID string, clusterKey string) string {
	return strings.TrimSpace(ruleID) + "|" + strings.TrimSpace(clusterKey)
}

//nolint:gocognit,gocyclo
func (r *jobRunner) buildClusters(rule ruleConfig, documents []map[string]any) []alertCluster {
	aggregates := make(map[string]*clusterAggregate)
	sampleN := rule.Samples
	if sampleN <= 0 {
		sampleN = defaultSamples
	}

	for _, source := range documents {
		if source == nil {
			continue
		}

		messageValue := firstNonEmpty(
			readFieldAsString(source, r.cfg.Fields.Msg),
			readFieldAsString(source, r.cfg.Fields.Message),
		)

		groupValues := make(map[string]string, len(rule.GroupBy))
		for _, field := range rule.GroupBy {
			normalizedField := strings.TrimSpace(field)
			if normalizedField == "" {
				continue
			}
			if normalizedField == "msg_template_hash" {
				groupValues[normalizedField] = buildMessageTemplateHash(messageValue)
				continue
			}
			groupValues[normalizedField] = readFieldAsString(source, normalizedField)
		}

		groupKey := buildGroupKey(rule.GroupBy, groupValues)
		if strings.TrimSpace(groupKey) == "" {
			continue
		}

		agg, exists := aggregates[groupKey]
		if !exists {
			agg = &clusterAggregate{
				GroupValues:    groupValues,
				TraceCounter:   map[string]int{},
				RequestCounter: map[string]int{},
				ExampleCounter: map[string]int{},
			}
			aggregates[groupKey] = agg
		}

		seenTime := parseDocumentTime(readFieldAsString(source, r.cfg.Fields.Timestamp), r.nowFn())
		if agg.Count == 0 || seenTime.Before(agg.FirstSeenTime) {
			agg.FirstSeenTime = seenTime
		}
		if agg.Count == 0 || seenTime.After(agg.LastSeenTime) {
			agg.LastSeenTime = seenTime
		}
		agg.Count++

		traceID := readFieldAsString(source, r.cfg.Fields.TraceID)
		if traceID != "" {
			agg.TraceCounter[traceID]++
		}

		if rule.Kind == ruleKindIngress5XX || rule.Kind == ruleKindIngressSlow {
			requestID := readFieldAsString(source, r.cfg.Fields.RequestID)
			if requestID != "" {
				agg.RequestCounter[requestID]++
			}
		}
		if rule.Kind == ruleKindMicrosvcError {
			example := truncateString(strings.ReplaceAll(messageValue, "\n", " "), maxMapValueLength)
			if example != "" {
				agg.ExampleCounter[example]++
			}
		}
	}

	keys := make([]string, 0, len(aggregates))
	for key := range aggregates {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	clusters := make([]alertCluster, 0, len(keys))
	for _, key := range keys {
		agg := aggregates[key]
		cluster := alertCluster{
			Key:         key,
			GroupValues: agg.GroupValues,
			Count:       agg.Count,
			FirstSeen:   agg.FirstSeenTime.UTC().Format(time.RFC3339),
			LastSeen:    agg.LastSeenTime.UTC().Format(time.RFC3339),
			TraceIDs:    topKeysByCount(agg.TraceCounter, sampleN),
			RequestIDs:  topKeysByCount(agg.RequestCounter, sampleN),
			MsgExamples: topKeysByCount(agg.ExampleCounter, sampleN),
		}
		clusters = append(clusters, cluster)
	}

	return clusters
}

func buildGroupKey(groupBy []string, values map[string]string) string {
	parts := make([]string, 0, len(groupBy))
	for _, field := range groupBy {
		trimmedField := strings.TrimSpace(field)
		if trimmedField == "" {
			continue
		}
		value := strings.TrimSpace(values[trimmedField])
		if value == "" {
			value = "-"
		}
		parts = append(parts, fmt.Sprintf("%s=%s", trimmedField, value))
	}
	return strings.Join(parts, "|")
}

func topKeysByCount(counter map[string]int, limit int) []string {
	if len(counter) == 0 {
		return []string{}
	}
	if limit <= 0 {
		limit = defaultSamples
	}

	type pair struct {
		Key   string
		Count int
	}
	pairs := make([]pair, 0, len(counter))
	for key, count := range counter {
		pairs = append(pairs, pair{Key: key, Count: count})
	}
	sort.Slice(pairs, func(left int, right int) bool {
		if pairs[left].Count == pairs[right].Count {
			return pairs[left].Key < pairs[right].Key
		}
		return pairs[left].Count > pairs[right].Count
	})

	n := minInt(limit, len(pairs))
	results := make([]string, 0, n)
	for idx := range n {
		results = append(results, pairs[idx].Key)
	}
	return results
}

func buildMessageTemplateHash(message string) string {
	normalized := strings.ToLower(strings.TrimSpace(message))
	if normalized == "" {
		return stableShortHash("empty")
	}

	normalized = reUUID.ReplaceAllString(normalized, "<uuid>")
	normalized = reIPv4.ReplaceAllString(normalized, "<ip>")
	normalized = reHex.ReplaceAllString(normalized, "<hex>")
	normalized = reDigits.ReplaceAllString(normalized, "<num>")
	normalized = reWhitespaces.ReplaceAllString(normalized, " ")
	return stableShortHash(strings.TrimSpace(normalized))
}

func readFieldAsString(source map[string]any, path string) string {
	trimmedPath := strings.TrimSpace(path)
	if trimmedPath == "" {
		return ""
	}
	if directValue, ok := source[trimmedPath]; ok {
		return stringifyValue(directValue)
	}

	parts := strings.Split(trimmedPath, ".")
	var current any = source
	for _, part := range parts {
		normalizedPart := strings.TrimSpace(part)
		if normalizedPart == "" {
			return ""
		}
		currentMap, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		next, exists := currentMap[normalizedPart]
		if !exists {
			return ""
		}
		current = next
	}

	return stringifyValue(current)
}

//nolint:gocyclo
func stringifyValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32)
	case int:
		return strconv.Itoa(typed)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case uint32:
		return strconv.FormatUint(uint64(typed), 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	case bool:
		return strconv.FormatBool(typed)
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(raw))
	}
}

func parseDocumentTime(value string, fallback time.Time) time.Time {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fallback.UTC()
	}

	layouts := []string{time.RFC3339Nano, time.RFC3339}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, trimmed)
		if err == nil {
			return parsed.UTC()
		}
	}

	if unixSeconds, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		return time.Unix(unixSeconds, 0).UTC()
	}

	return fallback.UTC()
}
