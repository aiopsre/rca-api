package policy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadPolicy_DefaultWhenPathEmpty(t *testing.T) {
	cfg, source, err := Load("", false)
	require.NoError(t, err)
	require.Equal(t, PolicyActiveSourceDefault, source)
	require.Equal(t, DefaultPolicyConfig(), cfg)
	require.False(t, cfg.Triggers.OnIngest.Rules[0].Action.Run)
	require.False(t, cfg.Triggers.OnEscalation.Rules[0].Action.Run)
	require.False(t, cfg.Triggers.Scheduled.Rules[0].Action.Run)
}

func TestResolveLoadInput_CLIOverridesYAML(t *testing.T) {
	in := ResolveLoadInput(ExternalPolicyOptions{
		Enabled:      true,
		Path:         "/tmp/cli-policy.yaml",
		Strict:       true,
		PathSetByCLI: true,
	})
	require.Equal(t, "/tmp/cli-policy.yaml", in.Path)
	require.Equal(t, RuleSourceCLI, in.Source)
	require.True(t, in.Strict)
}

func TestLoadPolicy_ParseErrorStrictFalseFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid-policy.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: [\n"), 0o600))

	cfg, source, err := Load(path, false)
	require.Error(t, err)
	require.Equal(t, PolicyActiveSourceDefault, source)
	require.Equal(t, DefaultPolicyConfig(), cfg)
}

func TestLoadPolicy_ParseErrorStrictTrueFail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid-policy.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: [\n"), 0o600))

	_, source, err := Load(path, true)
	require.Error(t, err)
	require.Equal(t, PolicyActiveSourceDefault, source)
}
