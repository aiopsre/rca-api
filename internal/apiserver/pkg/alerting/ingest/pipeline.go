package ingest

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/metrics"
)

var errMissingFingerprint = errors.New("missing fingerprint")

// EvaluateInput is the policy input for one alert ingest request.
type EvaluateInput struct {
	Fingerprint string
	Status      string
	LastSeenAt  time.Time
	SilenceID   string
}

// Pipeline evaluates alert ingest policy decisions.
type Pipeline struct {
	cfg      PolicyConfig
	primary  Backend
	fallback Backend
	failOpen bool
}

// NewPipeline constructs a policy pipeline from explicit backends.
func NewPipeline(cfg PolicyConfig, primary Backend, fallback Backend, failOpen bool) *Pipeline {
	cfg.ApplyDefaults()
	return &Pipeline{
		cfg:      cfg,
		primary:  primary,
		fallback: fallback,
		failOpen: failOpen,
	}
}

// NewDefaultPipeline builds a policy pipeline from runtime config and mysql lookup fallback.
func NewDefaultPipeline(cfg RuntimeConfig, mysqlLookup CurrentAlertLookup) *Pipeline {
	cfg.Policy.ApplyDefaults()
	cfg.Redis.ApplyDefaults()

	fallback := NewMySQLBackend(mysqlLookup)
	var primary Backend
	if cfg.Policy.RedisBackend.Enabled && cfg.Redis.Enabled {
		primary = NewRedisBackend(RedisBackendOptions{
			Addr:      cfg.Redis.Addr,
			Password:  cfg.Redis.Password,
			DB:        cfg.Redis.DB,
			KeyPrefix: cfg.Policy.RedisBackend.KeyPrefix,
		})
	}

	return NewPipeline(cfg.Policy, primary, fallback, cfg.Redis.FailOpen)
}

// Evaluate returns a policy decision for this ingest.
func (p *Pipeline) Evaluate(ctx context.Context, in EvaluateInput) (Decision, error) {
	decision := Decision{
		Decision: DecisionNormal,
		Backend:  p.defaultBackendName(),
	}

	if silenceID := strings.TrimSpace(in.SilenceID); silenceID != "" {
		decision.Decision = DecisionSilenced
		decision.Silenced = true
		decision.SilenceID = silenceID
		decision.SuppressIncident = true
		decision.SuppressTimeline = true
		p.recordDecision(decision)
		return decision, nil
	}

	fingerprint := strings.TrimSpace(in.Fingerprint)
	if fingerprint == "" {
		if p.failOpen {
			p.recordDecision(decision)
			return decision, nil
		}
		return decision, errMissingFingerprint
	}

	status := strings.ToLower(strings.TrimSpace(in.Status))
	if status != "firing" {
		p.recordDecision(decision)
		return decision, nil
	}

	decision.Decision = DecisionMerged
	decision, err := p.evaluateFiringPolicies(ctx, decision, fingerprint, in.LastSeenAt.UTC())
	if err != nil {
		return decision, err
	}

	p.recordDecision(decision)
	return decision, nil
}

func (p *Pipeline) evaluateFiringPolicies(ctx context.Context, decision Decision, fingerprint string, at time.Time) (Decision, error) {
	decision, err := p.evaluateDedup(ctx, decision, fingerprint, at)
	if err != nil {
		return decision, err
	}
	if decision.Deduped {
		return decision, nil
	}

	decision, err = p.evaluateBurst(ctx, decision, fingerprint, at)
	if err != nil {
		return decision, err
	}
	return decision, nil
}

func (p *Pipeline) evaluateDedup(ctx context.Context, decision Decision, fingerprint string, at time.Time) (Decision, error) {
	window := time.Duration(p.cfg.DedupWindowSeconds) * time.Second
	if window <= 0 {
		return decision, nil
	}

	deduped, backend, err := p.tryDedup(ctx, fingerprint, at, window)
	if backend != "" {
		decision.Backend = backend
	}
	if err != nil {
		return p.handlePolicyError(decision, err)
	}
	if deduped {
		decision.Decision = DecisionDeduped
		decision.Deduped = true
		decision.SuppressIncident = true
		decision.SuppressTimeline = true
	}
	return decision, nil
}

func (p *Pipeline) evaluateBurst(ctx context.Context, decision Decision, fingerprint string, at time.Time) (Decision, error) {
	if p.cfg.Burst.WindowSeconds <= 0 || p.cfg.Burst.Threshold <= 0 {
		return decision, nil
	}

	window := time.Duration(p.cfg.Burst.WindowSeconds) * time.Second
	count, backend, err := p.tryBurst(ctx, fingerprint, at, window)
	if backend != "" {
		decision.Backend = backend
	}
	if err != nil {
		return p.handlePolicyError(decision, err)
	}
	if count > int64(p.cfg.Burst.Threshold) {
		decision.Decision = DecisionDeduped
		decision.BurstSuppressed = true
		decision.SuppressIncident = true
		decision.SuppressTimeline = true
	}
	return decision, nil
}

func (p *Pipeline) handlePolicyError(decision Decision, err error) (Decision, error) {
	if p.failOpen {
		return decision, nil
	}
	return decision, err
}

func (p *Pipeline) tryDedup(ctx context.Context, fingerprint string, at time.Time, window time.Duration) (bool, string, error) {
	if p.primary != nil {
		deduped, err := p.primary.Dedup(ctx, fingerprint, at, window)
		if err == nil {
			return deduped, p.primary.Name(), nil
		}
		p.recordBackendError(p.primary.Name(), "dedup")
		slog.WarnContext(ctx, "alert ingest policy backend dedup failed",
			"backend", p.primary.Name(),
			"error", err,
		)
		if !p.failOpen || p.fallback == nil {
			return false, p.primary.Name(), err
		}
	}

	if p.fallback == nil {
		return false, "", nil
	}
	deduped, err := p.fallback.Dedup(ctx, fingerprint, at, window)
	if err != nil {
		p.recordBackendError(p.fallback.Name(), "dedup")
		slog.WarnContext(ctx, "alert ingest policy fallback dedup failed",
			"backend", p.fallback.Name(),
			"error", err,
		)
		if p.failOpen {
			return false, p.fallback.Name(), nil
		}
		return false, p.fallback.Name(), err
	}
	return deduped, p.fallback.Name(), nil
}

func (p *Pipeline) tryBurst(ctx context.Context, fingerprint string, at time.Time, window time.Duration) (int64, string, error) {
	if p.primary != nil {
		count, err := p.primary.Burst(ctx, fingerprint, at, window)
		if err == nil {
			return count, p.primary.Name(), nil
		}
		p.recordBackendError(p.primary.Name(), "burst")
		slog.WarnContext(ctx, "alert ingest policy backend burst failed",
			"backend", p.primary.Name(),
			"error", err,
		)
		if !p.failOpen || p.fallback == nil {
			return 0, p.primary.Name(), err
		}
	}

	if p.fallback == nil {
		return 0, "", nil
	}
	count, err := p.fallback.Burst(ctx, fingerprint, at, window)
	if err != nil {
		p.recordBackendError(p.fallback.Name(), "burst")
		slog.WarnContext(ctx, "alert ingest policy fallback burst failed",
			"backend", p.fallback.Name(),
			"error", err,
		)
		if p.failOpen {
			return 0, p.fallback.Name(), nil
		}
		return 0, p.fallback.Name(), err
	}
	return count, p.fallback.Name(), nil
}

func (p *Pipeline) defaultBackendName() string {
	if p.primary != nil {
		return p.primary.Name()
	}
	if p.fallback != nil {
		return p.fallback.Name()
	}
	return backendMySQL
}

func (p *Pipeline) recordDecision(decision Decision) {
	if metrics.M == nil {
		return
	}
	metrics.M.RecordAlertIngestPolicyDecision(decision.Decision, decision.Backend)
}

func (p *Pipeline) recordBackendError(backend string, op string) {
	if metrics.M == nil {
		return
	}
	metrics.M.RecordAlertIngestPolicyBackendError(backend, op)
}
