package ingest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeBackend struct {
	name      string
	deduped   bool
	burstCnt  int64
	dedupErr  error
	burstErr  error
	dedupCall int
	burstCall int
}

func (f *fakeBackend) Name() string {
	return f.name
}

func (f *fakeBackend) Dedup(_ context.Context, _ string, _ time.Time, _ time.Duration) (bool, error) {
	f.dedupCall++
	if f.dedupErr != nil {
		return false, f.dedupErr
	}
	return f.deduped, nil
}

func (f *fakeBackend) Burst(_ context.Context, _ string, _ time.Time, _ time.Duration) (int64, error) {
	f.burstCall++
	if f.burstErr != nil {
		return 0, f.burstErr
	}
	return f.burstCnt, nil
}

func TestPipeline_DefaultCompatibleWhenPolicyDisabled(t *testing.T) {
	pipeline := NewPipeline(DefaultPolicyConfig(), nil, &fakeBackend{name: backendMySQL}, true)
	decision, err := pipeline.Evaluate(context.Background(), EvaluateInput{
		Fingerprint: "fp-default",
		Status:      "firing",
		LastSeenAt:  time.Now().UTC(),
	})
	require.NoError(t, err)
	require.Equal(t, DecisionMerged, decision.Decision)
	require.False(t, decision.SuppressIncident)
	require.False(t, decision.SuppressTimeline)
	require.False(t, decision.Silenced)
}

func TestPipeline_SilenceOverridesAllPolicies(t *testing.T) {
	primary := &fakeBackend{name: backendRedis, deduped: true}
	cfg := DefaultPolicyConfig()
	cfg.DedupWindowSeconds = 30
	pipeline := NewPipeline(cfg, primary, &fakeBackend{name: backendMySQL}, true)

	decision, err := pipeline.Evaluate(context.Background(), EvaluateInput{
		Fingerprint: "fp-silenced",
		Status:      "firing",
		LastSeenAt:  time.Now().UTC(),
		SilenceID:   "silence-1",
	})
	require.NoError(t, err)
	require.Equal(t, DecisionSilenced, decision.Decision)
	require.True(t, decision.Silenced)
	require.Equal(t, "silence-1", decision.SilenceID)
	require.True(t, decision.SuppressIncident)
	require.Equal(t, 0, primary.dedupCall)
}

func TestPipeline_DedupWindowWithFakeBackend(t *testing.T) {
	primary := &fakeBackend{name: backendRedis, deduped: true}
	fallback := &fakeBackend{name: backendMySQL}
	cfg := DefaultPolicyConfig()
	cfg.DedupWindowSeconds = 60
	pipeline := NewPipeline(cfg, primary, fallback, true)

	decision, err := pipeline.Evaluate(context.Background(), EvaluateInput{
		Fingerprint: "fp-dedup",
		Status:      "firing",
		LastSeenAt:  time.Now().UTC(),
	})
	require.NoError(t, err)
	require.Equal(t, DecisionDeduped, decision.Decision)
	require.True(t, decision.Deduped)
	require.True(t, decision.SuppressIncident)
	require.Equal(t, backendRedis, decision.Backend)
	require.Equal(t, 1, primary.dedupCall)
	require.Equal(t, 0, fallback.dedupCall)
}

func TestPipeline_RedisErrorFallsBackToMySQLWhenFailOpen(t *testing.T) {
	primary := &fakeBackend{name: backendRedis, dedupErr: errors.New("redis down")}
	fallback := &fakeBackend{name: backendMySQL, deduped: true}
	cfg := DefaultPolicyConfig()
	cfg.DedupWindowSeconds = 30
	pipeline := NewPipeline(cfg, primary, fallback, true)

	decision, err := pipeline.Evaluate(context.Background(), EvaluateInput{
		Fingerprint: "fp-fallback",
		Status:      "firing",
		LastSeenAt:  time.Now().UTC(),
	})
	require.NoError(t, err)
	require.Equal(t, DecisionDeduped, decision.Decision)
	require.Equal(t, backendMySQL, decision.Backend)
	require.Equal(t, 1, primary.dedupCall)
	require.Equal(t, 1, fallback.dedupCall)
}
