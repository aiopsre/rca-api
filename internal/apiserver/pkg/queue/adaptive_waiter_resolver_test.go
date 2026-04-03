package queue

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResolveAdaptiveWaiterOptions_DefaultsUnchanged(t *testing.T) {
	clearAdaptiveWaiterEnv(t)

	got := ResolveAdaptiveWaiterOptions(
		AdaptiveWaiterOptions{},
		AdaptiveWaiterOptionSet{},
		AdaptiveWaiterOptions{},
		AdaptiveWaiterOptionSet{},
	)

	require.Equal(t, DefaultAdaptiveWaiterOptions(), got)
}

func TestResolveAdaptiveWaiterOptions_YAMLOverridesEnv(t *testing.T) {
	clearAdaptiveWaiterEnv(t)
	t.Setenv("RCA_AI_JOB_LONGPOLL_MAX_POLLING_WAITERS", "111")
	t.Setenv("RCA_AI_JOB_LONGPOLL_DB_ERROR_WINDOW", "33")

	yamlOpts := AdaptiveWaiterOptions{
		PollInterval:      2 * time.Second,
		MaxPollingWaiters: 222,
	}
	yamlSet := AdaptiveWaiterOptionSet{
		PollInterval:      true,
		MaxPollingWaiters: true,
	}

	got := ResolveAdaptiveWaiterOptions(yamlOpts, yamlSet, AdaptiveWaiterOptions{}, AdaptiveWaiterOptionSet{})

	require.Equal(t, 2*time.Second, got.PollInterval)
	require.Equal(t, int64(222), got.MaxPollingWaiters)
	require.Equal(t, 33, got.DBErrorWindow)
}

func TestResolveAdaptiveWaiterOptions_CLIOverridesYAML(t *testing.T) {
	clearAdaptiveWaiterEnv(t)
	t.Setenv("RCA_AI_JOB_LONGPOLL_POLL_INTERVAL_MS", "5000")

	yamlOpts := AdaptiveWaiterOptions{
		PollInterval:      2 * time.Second,
		MaxPollingWaiters: 333,
	}
	yamlSet := AdaptiveWaiterOptionSet{
		PollInterval:      true,
		MaxPollingWaiters: true,
	}
	cliOpts := AdaptiveWaiterOptions{
		PollInterval: 3 * time.Second,
	}
	cliSet := AdaptiveWaiterOptionSet{
		PollInterval: true,
	}

	got := ResolveAdaptiveWaiterOptions(yamlOpts, yamlSet, cliOpts, cliSet)

	require.Equal(t, 3*time.Second, got.PollInterval)
	require.Equal(t, int64(333), got.MaxPollingWaiters)
}

func clearAdaptiveWaiterEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"RCA_AI_JOB_LONGPOLL_MAX_POLLING_WAITERS",
		"RCA_AI_JOB_LONGPOLL_DB_ERROR_WINDOW",
		"RCA_AI_JOB_LONGPOLL_DB_ERROR_MIN_SAMPLES",
		"RCA_AI_JOB_LONGPOLL_DB_ERROR_RATE_THRESHOLD",
		"RCA_AI_JOB_LONGPOLL_POLL_INTERVAL_MS",
		"RCA_AI_JOB_LONGPOLL_CACHE_TTL_MS",
	} {
		t.Setenv(key, "")
	}
}
