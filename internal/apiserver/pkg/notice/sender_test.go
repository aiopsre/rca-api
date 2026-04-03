package notice

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type webhookRequestCapture struct {
	Method         string
	Path           string
	Body           string
	Signature      string
	Timestamp      string
	Nonce          string
	DeliveryID     string
	EventType      string
	IdempotencyKey string
}

func TestSendWebhook_SignsWithV1Headers(t *testing.T) {
	var mu sync.Mutex
	captured := webhookRequestCapture{}

	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, r.Body.Close())

		mu.Lock()
		captured = webhookRequestCapture{
			Method:         r.Method,
			Path:           r.URL.Path,
			Body:           string(body),
			Signature:      r.Header.Get(noticeHeaderSignature),
			Timestamp:      r.Header.Get(noticeHeaderTimestamp),
			Nonce:          r.Header.Get(noticeHeaderNonce),
			DeliveryID:     r.Header.Get(noticeHeaderDeliveryID),
			EventType:      r.Header.Get(noticeHeaderEventType),
			IdempotencyKey: r.Header.Get("Idempotency-Key"),
		}
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer mockSrv.Close()

	secret := "sender-test-secret"
	payload := []byte(`{"hello":"world"}`)
	code, responseBody, _, err := sendWebhook(context.Background(), webhookSendConfig{
		EndpointURL: mockSrv.URL + "/webhook/notice?debug=1",
		TimeoutMs:   1000,
		Secret:      &secret,
	}, "Incident_Created", "notice-delivery-test-1", "idem-test-1", payload)
	require.NoError(t, err)
	require.NotNil(t, code)
	require.Equal(t, int32(http.StatusOK), *code)
	require.Equal(t, `{"ok":true}`, responseBody)

	mu.Lock()
	got := captured
	mu.Unlock()

	require.Equal(t, http.MethodPost, got.Method)
	require.Equal(t, "/webhook/notice", got.Path)
	require.Equal(t, "incident_created", got.EventType)
	require.Equal(t, "notice-delivery-test-1", got.DeliveryID)
	require.Equal(t, "idem-test-1", got.IdempotencyKey)
	require.Equal(t, string(payload), got.Body)
	require.NotEmpty(t, got.Timestamp)
	require.True(t, regexp.MustCompile(`^[0-9]+$`).MatchString(got.Timestamp))
	require.NotEmpty(t, got.Nonce)
	require.LessOrEqual(t, len(got.Nonce), nonceMaxLength)
	require.NotEmpty(t, got.Signature)
	require.Contains(t, got.Signature, noticeSignaturePrefix)

	signing := buildSigningStringV1(got.Timestamp, got.Nonce, got.Method, got.Path, sha256Hex(payload))
	require.Equal(t, noticeSignaturePrefix+signPayload(secret, []byte(signing)), got.Signature)
}

func TestSendWebhook_EmptySecretSkipsSignatureHeaders(t *testing.T) {
	var mu sync.Mutex
	captured := webhookRequestCapture{}

	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, r.Body.Close())

		mu.Lock()
		captured = webhookRequestCapture{
			Body:           string(body),
			Signature:      r.Header.Get(noticeHeaderSignature),
			Timestamp:      r.Header.Get(noticeHeaderTimestamp),
			Nonce:          r.Header.Get(noticeHeaderNonce),
			DeliveryID:     r.Header.Get(noticeHeaderDeliveryID),
			EventType:      r.Header.Get(noticeHeaderEventType),
			IdempotencyKey: r.Header.Get("Idempotency-Key"),
		}
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer mockSrv.Close()

	secret := "   "
	code, _, _, err := sendWebhook(context.Background(), webhookSendConfig{
		EndpointURL: mockSrv.URL + "/webhook",
		TimeoutMs:   1000,
		Secret:      &secret,
	}, EventTypeIncidentCreated, "notice-delivery-test-2", "idem-test-2", []byte(`{"mode":"compat"}`))
	require.NoError(t, err)
	require.NotNil(t, code)
	require.Equal(t, int32(http.StatusOK), *code)

	mu.Lock()
	got := captured
	mu.Unlock()

	require.Empty(t, got.Signature)
	require.Empty(t, got.Timestamp)
	require.Empty(t, got.Nonce)
	require.Equal(t, EventTypeIncidentCreated, got.EventType)
	require.Equal(t, "notice-delivery-test-2", got.DeliveryID)
	require.Equal(t, "idem-test-2", got.IdempotencyKey)
	require.Equal(t, `{"mode":"compat"}`, got.Body)
}

func TestSendWebhook_NonceChangesPerAttempt(t *testing.T) {
	var mu sync.Mutex
	records := make([]webhookRequestCapture, 0, 2)

	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, r.Body.Close())

		record := webhookRequestCapture{
			Method:     r.Method,
			Path:       r.URL.Path,
			Body:       string(body),
			Signature:  r.Header.Get(noticeHeaderSignature),
			Timestamp:  r.Header.Get(noticeHeaderTimestamp),
			Nonce:      r.Header.Get(noticeHeaderNonce),
			DeliveryID: r.Header.Get(noticeHeaderDeliveryID),
			EventType:  r.Header.Get(noticeHeaderEventType),
		}
		mu.Lock()
		records = append(records, record)
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer mockSrv.Close()

	secret := "sender-test-secret-nonce"
	cfg := webhookSendConfig{
		EndpointURL: mockSrv.URL + "/webhook",
		TimeoutMs:   1000,
		Secret:      &secret,
	}
	payload := []byte(`{"case":"nonce-change"}`)
	for i := 0; i < 2; i++ {
		code, _, _, err := sendWebhook(context.Background(), cfg, EventTypeIncidentCreated, "notice-delivery-test-3", "idem-test-3", payload)
		require.NoError(t, err)
		require.NotNil(t, code)
		require.Equal(t, int32(http.StatusOK), *code)
	}

	mu.Lock()
	got := append([]webhookRequestCapture(nil), records...)
	mu.Unlock()
	require.Len(t, got, 2)
	require.NotEqual(t, got[0].Nonce, got[1].Nonce)
	require.NotEqual(t, got[0].Signature, got[1].Signature)

	for _, one := range got {
		require.NotEmpty(t, one.Timestamp)
		require.True(t, regexp.MustCompile(`^[0-9]+$`).MatchString(one.Timestamp))
		require.NotEmpty(t, one.Nonce)
		require.LessOrEqual(t, len(one.Nonce), nonceMaxLength)
		signing := buildSigningStringV1(one.Timestamp, one.Nonce, one.Method, one.Path, sha256Hex([]byte(one.Body)))
		require.Equal(t, noticeSignaturePrefix+signPayload(secret, []byte(signing)), one.Signature)
	}
}
