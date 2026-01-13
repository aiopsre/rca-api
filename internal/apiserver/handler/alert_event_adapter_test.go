package handler

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBuildAdapterIngestRequest_GenericV1StableFingerprint(t *testing.T) {
	now := time.Date(2026, 2, 12, 10, 0, 0, 0, time.UTC)
	basePayload := map[string]any{
		"namespace":  "shop",
		"service":    "checkout",
		"severity":   "critical",
		"alertname":  "HTTP5xxHigh",
		"summary":    "error ratio high",
		"event_time": "2026-02-12T10:00:00Z",
		"labels": map[string]any{
			"alertname": "HTTP5xxHigh",
			"service":   "checkout",
			"namespace": "shop",
			"pod":       "checkout-abc-1",
			"ip":        "10.0.0.1",
		},
		"annotations": map[string]any{
			"summary":       "error ratio high",
			"authorization": "Bearer test-token",
		},
	}

	req1, err := buildAdapterIngestRequest(adapterGenericV1, basePayload, now)
	require.NoError(t, err)
	require.NotNil(t, req1)

	payload2 := cloneAnyMap(basePayload)
	payload2Labels := toAnyMap(payload2["labels"])
	payload2Labels["pod"] = "checkout-abc-2"
	payload2Labels["ip"] = "10.0.0.2"
	payload2["labels"] = payload2Labels

	req2, err := buildAdapterIngestRequest(adapterGenericV1, payload2, now)
	require.NoError(t, err)
	require.NotNil(t, req2)

	require.Equal(t, req1.GetFingerprint(), req2.GetFingerprint())
	require.Contains(t, req1.GetFingerprint(), adapterFingerprintPrefix)
	require.Equal(t, "shop", req1.GetNamespace())
	require.Equal(t, "checkout", req1.GetService())
	require.Equal(t, "critical", req1.GetSeverity())

	annotations := decodeStringMap(t, req1.GetAnnotationsJSON())
	require.NotContains(t, annotations, "authorization")
}

func TestBuildAdapterIngestRequest_AlertmanagerSensitiveFilter(t *testing.T) {
	now := time.Date(2026, 2, 12, 10, 0, 0, 0, time.UTC)
	payload := map[string]any{
		"status": "firing",
		"alerts": []any{
			map[string]any{
				"status":      "firing",
				"startsAt":    "2026-02-12T10:00:00Z",
				"fingerprint": "am-fp-1",
				"labels": map[string]any{
					"alertname":     "PodCrashLooping",
					"namespace":     "trial-ns",
					"service":       "billing",
					"severity":      "warning",
					"authorization": "Bearer sensitive",
				},
				"annotations": map[string]any{
					"summary": "token leaked",
					"secret":  "secret-value",
				},
			},
		},
		"commonLabels": map[string]any{
			"cluster": "prod-a",
		},
	}

	req, err := buildAdapterIngestRequest(adapterPrometheusAlertmanager, payload, now)
	require.NoError(t, err)
	require.NotNil(t, req)

	require.Equal(t, adapterPrometheusAlertmanager, req.GetSource())
	require.Equal(t, "trial-ns", req.GetNamespace())
	require.Equal(t, "billing", req.GetService())
	require.Equal(t, "warning", req.GetSeverity())
	require.Equal(t, "firing", req.GetStatus())
	require.Equal(t, "am-fp-1", req.GetFingerprint())

	labels := decodeStringMap(t, req.GetLabelsJSON())
	require.Equal(t, "prod-a", labels["cluster"])
	require.NotContains(t, labels, "authorization")

	annotations := decodeStringMap(t, req.GetAnnotationsJSON())
	require.NotContains(t, annotations, "secret")
	require.Equal(t, "[redacted]", annotations["summary"])
}

func decodeStringMap(t *testing.T, raw string) map[string]string {
	t.Helper()
	out := map[string]string{}
	if raw == "" {
		return out
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &out))
	return out
}

func cloneAnyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
