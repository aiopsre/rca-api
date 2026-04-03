package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const (
	maxResponseBodyForError = 2048
)

var errESRequest = errors.New("es request failed")

type esClient struct {
	urls       []string
	username   string
	password   string
	timeout    time.Duration
	maxRetries int
	httpClient *http.Client
	metrics    *jobMetrics
	logger     *slog.Logger

	roundRobinIndex atomic.Uint64
}

type esSearchResponse struct {
	Hits esHits `json:"hits"`
}

type esHits struct {
	Hits []esHit `json:"hits"`
}

type esHit struct {
	Source map[string]any `json:"_source"`
}

func newESClient(cfg esConfig, metrics *jobMetrics, logger *slog.Logger) *esClient {
	urls := make([]string, 0, len(cfg.URLs))
	for _, rawURL := range cfg.URLs {
		normalized := strings.TrimSpace(rawURL)
		if normalized == "" {
			continue
		}
		urls = append(urls, strings.TrimRight(normalized, "/"))
	}

	timeout := durationFromMillis(cfg.TimeoutMS)
	if timeout <= 0 {
		timeout = durationFromMillis(defaultESRequestTimeout)
	}

	maxRetries := cfg.MaxRetries
	if maxRetries < 0 {
		maxRetries = defaultESMaxRetries
	}

	return &esClient{
		urls:       urls,
		username:   strings.TrimSpace(cfg.Username),
		password:   cfg.Password,
		timeout:    timeout,
		maxRetries: maxRetries,
		httpClient: &http.Client{Timeout: timeout},
		metrics:    metrics,
		logger:     logger,
	}
}

//nolint:gocognit,gocyclo
func (c *esClient) search(
	ctx context.Context,
	ruleID string,
	indexPattern string,
	queryString string,
	timestampField string,
	window time.Duration,
	limit int,
) ([]map[string]any, string, error) {

	requestBody, err := c.buildSearchBody(queryString, timestampField, window, limit)
	if err != nil {
		return nil, "build_request_failed", err
	}

	maxAttempts := c.maxRetries + 1
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	if maxAttempts > len(c.urls) {
		maxAttempts = len(c.urls)
	}
	if maxAttempts == 0 {
		return nil, "no_es_url", errNoESURLs
	}

	startIndex := int(c.roundRobinIndex.Add(1)-1) % len(c.urls)
	var lastErr error
	lastCode := "es_failed"

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		selectedURL := c.urls[(startIndex+attempt-1)%len(c.urls)]
		if attempt > 1 {
			c.metrics.recordESFailover()
		}

		documents, code, retryable, attemptErr := c.trySearchOnce(
			ctx,
			selectedURL,
			indexPattern,
			requestBody,
		)
		lastCode = code
		if attemptErr == nil {
			return documents, code, nil
		}

		lastErr = attemptErr
		c.logger.WarnContext(ctx, "es request failed",
			slog.String("selected_url", selectedURL),
			slog.Int("attempt", attempt),
			slog.String("error", attemptErr.Error()),
			slog.String("rule_id", ruleID),
		)
		if !retryable {
			break
		}
	}

	if lastErr == nil {
		lastErr = esRequestErrf("rule=%s query exhausted retries", ruleID)
	}
	return nil, lastCode, lastErr
}

func (c *esClient) buildSearchBody(queryString string, timestampField string, window time.Duration, limit int) ([]byte, error) {
	if limit <= 0 {
		limit = defaultMaxDocsPerRule
	}

	query := map[string]any{
		"size": limit,
		"sort": []map[string]any{
			{
				timestampField: map[string]any{
					"order": "desc",
				},
			},
		},
		"query": map[string]any{
			"bool": map[string]any{
				"filter": []map[string]any{
					{
						"query_string": map[string]any{
							"query": queryString,
						},
					},
					{
						"range": map[string]any{
							timestampField: map[string]any{
								"gte": fmt.Sprintf("now-%ds", int(window.Seconds())),
								"lte": "now",
							},
						},
					},
				},
			},
		},
	}

	encoded, err := json.Marshal(query)
	if err != nil {
		return nil, fmt.Errorf("marshal search request: %w", err)
	}

	return encoded, nil
}

//nolint:gocognit,gocyclo
func (c *esClient) trySearchOnce(
	ctx context.Context,
	selectedURL string,
	indexPattern string,
	body []byte,
) ([]map[string]any, string, bool, error) {

	endpoint := fmt.Sprintf("%s/%s/_search", strings.TrimRight(selectedURL, "/"), strings.TrimLeft(indexPattern, "/"))
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, "build_request_failed", false, fmt.Errorf("build request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if c.username != "" {
		request.SetBasicAuth(c.username, c.password)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		c.metrics.recordESRequestCode("request_error")
		return nil, "request_error", true, fmt.Errorf("execute request: %w", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()

	c.metrics.recordESRequest(response.StatusCode)

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		bodySample, bodyErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBodyForError))
		if bodyErr != nil {
			bodySample = []byte("<failed to read response body>")
		}
		retryable := response.StatusCode >= http.StatusInternalServerError || response.StatusCode == http.StatusTooManyRequests
		return nil,
			fmt.Sprintf("%d", response.StatusCode),
			retryable,
			esRequestErrf("status=%d body=%s", response.StatusCode, strings.TrimSpace(string(bodySample)))
	}

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, "read_body_failed", true, fmt.Errorf("read response body: %w", err)
	}
	parsed := esSearchResponse{}
	if err = json.Unmarshal(responseBody, &parsed); err != nil {
		return nil, "decode_failed", true, fmt.Errorf("decode response body: %w", err)
	}

	documents := make([]map[string]any, 0, len(parsed.Hits.Hits))
	for _, hit := range parsed.Hits.Hits {
		if hit.Source == nil {
			continue
		}
		documents = append(documents, hit.Source)
	}

	return documents, fmt.Sprintf("%d", response.StatusCode), false, nil
}

func esRequestErrf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errESRequest, fmt.Sprintf(format, args...))
}
