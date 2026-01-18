package policy

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	errReadAlertingPolicyFile       = errors.New("read alerting policy file failed")
	errParseAlertingPolicyFile      = errors.New("parse alerting policy file failed")
	errUnsupportedAlertingPolicyVer = errors.New("unsupported alerting policy version")
)

// Load loads external alerting trigger policy from file path with default fallback.
//
// Returned source is "file" on successful file load, otherwise "default".
// When strict=false, any read/parse/version error returns default config with non-nil err.
// When strict=true, errors are still returned and should be treated as startup blocking.
func Load(path string, strict bool) (PolicyConfig, string, error) {
	defaultCfg := DefaultPolicyConfig()
	cleanPath := strings.TrimSpace(path)
	if cleanPath == "" {
		return defaultCfg, PolicyActiveSourceDefault, nil
	}

	raw, err := os.ReadFile(cleanPath)
	if err != nil {
		return fallback(defaultCfg, strict, fmt.Errorf("%w: path=%q err=%v", errReadAlertingPolicyFile, cleanPath, err))
	}

	var cfg PolicyConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return fallback(defaultCfg, strict, fmt.Errorf("%w: path=%q err=%v", errParseAlertingPolicyFile, cleanPath, err))
	}

	if cfg.Version != PolicyVersion1 {
		return fallback(defaultCfg, strict, fmt.Errorf("%w: version=%d path=%q", errUnsupportedAlertingPolicyVer, cfg.Version, cleanPath))
	}

	cfg.applyDefaults()
	return cfg, PolicyActiveSourceFile, nil
}

func fallback(defaultCfg PolicyConfig, _ bool, err error) (PolicyConfig, string, error) {
	defaultCfg.applyDefaults()
	return defaultCfg, PolicyActiveSourceDefault, err
}
