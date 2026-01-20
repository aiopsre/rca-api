package queue

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPubSubHealth_HysteresisAndHealthyTTL(t *testing.T) {
	opts := PubSubHealthOptions{
		HealthyTTL:     2 * time.Second,
		FailuresToDown: 3,
		SuccessesToUp:  2,
	}
	health := NewPubSubHealth(opts)
	base := time.Unix(1700000000, 0).UTC()

	require.False(t, health.Ready(base))

	health.MarkSuccess(base.Add(100 * time.Millisecond))
	require.False(t, health.Ready(base.Add(100*time.Millisecond)))

	health.MarkSuccess(base.Add(200 * time.Millisecond))
	require.True(t, health.Ready(base.Add(200*time.Millisecond)))

	health.MarkFailure(base.Add(300 * time.Millisecond))
	require.True(t, health.Ready(base.Add(300*time.Millisecond)))

	health.MarkFailure(base.Add(400 * time.Millisecond))
	require.True(t, health.Ready(base.Add(400*time.Millisecond)))

	health.MarkFailure(base.Add(500 * time.Millisecond))
	require.False(t, health.Ready(base.Add(500*time.Millisecond)))

	health.MarkSuccess(base.Add(600 * time.Millisecond))
	require.False(t, health.Ready(base.Add(600*time.Millisecond)))

	health.MarkSuccess(base.Add(700 * time.Millisecond))
	require.True(t, health.Ready(base.Add(700*time.Millisecond)))

	require.False(t, health.Ready(base.Add(3*time.Second)))
}
