package notice

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"zk8s.com/rca-api/internal/apiserver/model"
)

var errEmptyEndpointURL = errors.New("empty endpoint_url")

func sendWebhook(
	ctx context.Context,
	channel *model.NoticeChannelM,
	eventType string,
	idempotencyKey string,
	payloadRaw []byte,
) (*int32, string, int64, error) {

	started := time.Now()

	endpoint := strings.TrimSpace(channel.EndpointURL)
	if endpoint == "" {
		return nil, "", 0, errEmptyEndpointURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payloadRaw))
	if err != nil {
		return nil, "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Rca-Event-Type", strings.ToLower(strings.TrimSpace(eventType)))
	req.Header.Set("Idempotency-Key", strings.TrimSpace(idempotencyKey))

	for key, value := range parseHeaders(channel.HeadersJSON) {
		req.Header.Set(key, value)
	}
	if secret := strings.TrimSpace(derefString(channel.Secret)); secret != "" {
		req.Header.Set("X-Rca-Signature", signPayload(secret, payloadRaw))
	}

	client := &http.Client{Timeout: time.Duration(clampTimeoutMs(channel.TimeoutMs)) * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", time.Since(started).Milliseconds(), err
	}
	defer resp.Body.Close()

	code := int32(resp.StatusCode)
	bodyRaw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxHTTPReadBytes))
	if readErr != nil {
		return nil, "", time.Since(started).Milliseconds(), readErr
	}
	return &code, string(bodyRaw), time.Since(started).Milliseconds(), nil
}

func parseHeaders(raw *string) map[string]string {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return map[string]string{}
	}
	out := make(map[string]string)
	if err := json.Unmarshal([]byte(*raw), &out); err != nil {
		return map[string]string{}
	}
	return out
}

func signPayload(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func clampTimeoutMs(in int64) int64 {
	switch {
	case in == 0:
		return defaultTimeoutMs
	case in < timeoutMsMin:
		return timeoutMsMin
	case in > timeoutMsMax:
		return timeoutMsMax
	default:
		return in
	}
}

func truncateString(in string, maxBytes int) string {
	if len(in) <= maxBytes {
		return in
	}
	return in[:maxBytes]
}

func strPtrOrNil(v string) *string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	return &v
}

func derefString(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

func normalizeStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, item := range in {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}
