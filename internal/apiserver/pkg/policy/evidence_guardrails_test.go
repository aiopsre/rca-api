package policy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClampDatasourceTimeout(t *testing.T) {
	g := DefaultEvidenceGuardrails()

	require.Equal(t, g.DefaultDatasourceTimeout, g.ClampDatasourceTimeout(0))
	require.Equal(t, g.MinDatasourceTimeout, g.ClampDatasourceTimeout(1))
	require.Equal(t, 5*time.Second, g.ClampDatasourceTimeout(5000))
	require.Equal(t, g.MaxDatasourceTimeout, g.ClampDatasourceTimeout(999999))
}

func TestDatasourceRateLimiter(t *testing.T) {
	g := DefaultEvidenceGuardrails()
	g.QueryRatePerSecond = 0.1
	g.QueryRateBurst = 1

	limiter := NewDatasourceRateLimiter(g)
	require.True(t, limiter.Allow("ds-1"))
	require.False(t, limiter.Allow("ds-1"))
	require.False(t, limiter.Allow(""))
}
