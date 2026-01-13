package alert_event

import (
	"testing"

	"github.com/stretchr/testify/require"

	alertingingest "github.com/aiopsre/rca-api/internal/apiserver/pkg/alerting/ingest"
)

func TestRolloutAllowMatched(t *testing.T) {
	t.Run("empty allow list means allow all", func(t *testing.T) {
		cfg := alertingingest.RolloutConfig{}
		require.True(t, rolloutAllowMatched(cfg, "default", "checkout"))
	})

	t.Run("namespace and service both must match when configured", func(t *testing.T) {
		cfg := alertingingest.RolloutConfig{
			AllowedNamespaces: []string{"prod-a", "prod-b"},
			AllowedServices:   []string{"checkout"},
		}
		require.True(t, rolloutAllowMatched(cfg, "prod-a", "checkout"))
		require.False(t, rolloutAllowMatched(cfg, "prod-c", "checkout"))
		require.False(t, rolloutAllowMatched(cfg, "prod-a", "payment"))
	})
}

func TestEvaluateRolloutDecision(t *testing.T) {
	in := &ingestInput{
		namespace: "trial-ns",
		service:   "svc-a",
	}

	t.Run("disabled rollout keeps progression", func(t *testing.T) {
		b := &alertEventBiz{
			rolloutConfig: alertingingest.RolloutConfig{
				Enabled: false,
				Mode:    alertingingest.RolloutModeObserve,
			},
		}
		got := b.evaluateRolloutDecision(in, ingestOptions{applyRollout: true})
		require.True(t, got.allowed)
		require.True(t, got.shouldProgress)
		require.Empty(t, got.dropReason)
	})

	t.Run("observe mode always blocks progression", func(t *testing.T) {
		b := &alertEventBiz{
			rolloutConfig: alertingingest.RolloutConfig{
				Enabled:           true,
				Mode:              alertingingest.RolloutModeObserve,
				AllowedNamespaces: []string{"allowed-ns"},
				AllowedServices:   []string{"svc-a"},
			},
		}
		got := b.evaluateRolloutDecision(in, ingestOptions{applyRollout: true})
		require.False(t, got.allowed)
		require.False(t, got.shouldProgress)
		require.Equal(t, "observe_mode", got.dropReason)
	})

	t.Run("enforce mode blocks not-allowed scope", func(t *testing.T) {
		b := &alertEventBiz{
			rolloutConfig: alertingingest.RolloutConfig{
				Enabled:           true,
				Mode:              alertingingest.RolloutModeEnforce,
				AllowedNamespaces: []string{"allowed-ns"},
				AllowedServices:   []string{"svc-a"},
			},
		}
		got := b.evaluateRolloutDecision(in, ingestOptions{applyRollout: true})
		require.False(t, got.allowed)
		require.False(t, got.shouldProgress)
		require.Equal(t, "not_allowed", got.dropReason)
	})

	t.Run("enforce mode allows matched scope", func(t *testing.T) {
		b := &alertEventBiz{
			rolloutConfig: alertingingest.RolloutConfig{
				Enabled:           true,
				Mode:              alertingingest.RolloutModeEnforce,
				AllowedNamespaces: []string{"trial-ns"},
				AllowedServices:   []string{"svc-a"},
			},
		}
		got := b.evaluateRolloutDecision(in, ingestOptions{applyRollout: true})
		require.True(t, got.allowed)
		require.True(t, got.shouldProgress)
		require.Empty(t, got.dropReason)
	})
}
