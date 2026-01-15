package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	maxWebhookResponseBodyBytes = 2048
)

var errWebhookStatus = errors.New("webhook request failed")

type webhookClient struct {
	baseURL    string
	timeout    time.Duration
	httpClient *http.Client
}

func newWebhookClient(cfg rcaConfig) *webhookClient {
	timeout := durationFromMillis(cfg.TimeoutMS)
	if timeout <= 0 {
		timeout = durationFromMillis(defaultRCARequestTimeMS)
	}

	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		baseURL = "http://127.0.0.1:5655"
	}

	return &webhookClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		timeout:    timeout,
		httpClient: &http.Client{Timeout: timeout},
	}
}

func (c *webhookClient) send(ctx context.Context, payload genericWebhookPayload) (int, time.Duration, error) {
	startTime := time.Now()
	body, _ := json.Marshal(payload)

	url := fmt.Sprintf("%s/v1/alerts/ingest/generic_v1", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, 0, fmt.Errorf("build webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, time.Since(startTime), fmt.Errorf("send webhook request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	latency := time.Since(startTime)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxWebhookResponseBodyBytes))
		if readErr != nil {
			respBody = []byte("<failed to read webhook response body>")
		}
		return resp.StatusCode,
			latency,
			fmt.Errorf("%w: status=%d body=%s", errWebhookStatus, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxWebhookResponseBodyBytes))
	return resp.StatusCode, latency, nil
}
