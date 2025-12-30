package notice

import (
	"bytes"
	"context"
	"crypto/hmac"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var errEmptyEndpointURL = errors.New("empty endpoint_url")
var (
	errNilRequest   = errors.New("nil request")
	errNonceTooLong = errors.New("nonce too long")
	errEmptyNonce   = errors.New("empty nonce")
)

const (
	noticeHeaderSignature  = "X-Rca-Signature"
	noticeHeaderTimestamp  = "X-Rca-Timestamp"
	noticeHeaderNonce      = "X-Rca-Nonce"
	noticeHeaderDeliveryID = "X-Rca-Delivery-Id"
	noticeHeaderEventType  = "X-Rca-Event-Type"
	noticeSignatureVersion = "v1"
	noticeSignaturePrefix  = "sha256="
	nonceRandomBytes       = 16
	nonceMaxLength         = 128
)

type webhookSendConfig struct {
	EndpointURL string
	TimeoutMs   int64
	HeadersJSON *string
	Secret      *string
}

func sendWebhook(
	ctx context.Context,
	cfg webhookSendConfig,
	eventType string,
	deliveryID string,
	idempotencyKey string,
	payloadRaw []byte,
) (*int32, string, int64, error) {

	started := time.Now()

	endpoint := strings.TrimSpace(cfg.EndpointURL)
	if endpoint == "" {
		return nil, "", 0, errEmptyEndpointURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payloadRaw))
	if err != nil {
		return nil, "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(noticeHeaderEventType, strings.ToLower(strings.TrimSpace(eventType)))
	req.Header.Set(noticeHeaderDeliveryID, strings.TrimSpace(deliveryID))
	req.Header.Set("Idempotency-Key", strings.TrimSpace(idempotencyKey))

	for key, value := range parseHeaders(cfg.HeadersJSON) {
		req.Header.Set(key, value)
	}
	// P2-3 compatibility mode: empty secret keeps unsigned delivery behavior.
	if secret := strings.TrimSpace(derefString(cfg.Secret)); secret != "" {
		if err := applySignatureHeaders(req, secret, payloadRaw); err != nil {
			return nil, "", time.Since(started).Milliseconds(), err
		}
	}

	client := &http.Client{Timeout: time.Duration(clampTimeoutMs(cfg.TimeoutMs)) * time.Millisecond}
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

func applySignatureHeaders(req *http.Request, secret string, body []byte) error {
	if req == nil {
		return errNilRequest
	}
	timestamp := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	if _, err := strconv.ParseInt(timestamp, 10, 64); err != nil {
		return fmt.Errorf("invalid timestamp: %w", err)
	}
	nonce, err := newAttemptNonce()
	if err != nil {
		return err
	}
	if len(nonce) > nonceMaxLength {
		return fmt.Errorf("%w: %d", errNonceTooLong, len(nonce))
	}
	path := "/"
	if req.URL != nil && req.URL.EscapedPath() != "" {
		path = req.URL.EscapedPath()
	}
	signingString := buildSigningStringV1(timestamp, nonce, req.Method, path, sha256Hex(body))
	req.Header.Set(noticeHeaderTimestamp, timestamp)
	req.Header.Set(noticeHeaderNonce, nonce)
	req.Header.Set(noticeHeaderSignature, noticeSignaturePrefix+signPayload(secret, []byte(signingString)))
	return nil
}

func signPayload(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func newAttemptNonce() (string, error) {
	buf := make([]byte, nonceRandomBytes)
	if _, err := crand.Read(buf); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	out := hex.EncodeToString(buf)
	if strings.TrimSpace(out) == "" {
		return "", errEmptyNonce
	}
	return out, nil
}

func buildSigningStringV1(timestamp string, nonce string, method string, path string, bodySHA256Hex string) string {
	return strings.Join([]string{
		noticeSignatureVersion,
		strings.TrimSpace(timestamp),
		strings.TrimSpace(nonce),
		strings.ToUpper(strings.TrimSpace(method)),
		path,
		strings.ToLower(strings.TrimSpace(bodySHA256Hex)),
	}, "\n")
}

func sha256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
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
