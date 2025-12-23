//nolint:gocognit
package silenceutil

import (
	"encoding/json"
	"strings"
)

const (
	DefaultNamespace    = "default"
	DefaultWorkloadKind = "Deployment"
	MatcherOpEqual      = "="
)

// Matcher is the persisted matcher shape used in silence.matchers_json.
type Matcher struct {
	Key   string `json:"key"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

var allowedMatcherKeySet = map[string]struct{}{
	"fingerprint":  {},
	"service":      {},
	"workloadkind": {},
	"workloadname": {},
	"severity":     {},
}

func NormalizeNamespace(ns string) string {
	ns = strings.TrimSpace(ns)
	if ns == "" {
		return DefaultNamespace
	}
	return ns
}

func NormalizeMatcherKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

func NormalizeMatcherOp(op string) string {
	return strings.TrimSpace(op)
}

func IsAllowedMatcherKey(key string) bool {
	_, ok := allowedMatcherKeySet[NormalizeMatcherKey(key)]
	return ok
}

func EncodeMatchers(matchers []Matcher) (string, error) {
	//nolint:errchkjson // Matcher struct is fully controlled and JSON-safe.
	buf, err := json.Marshal(matchers)
	if err != nil {
		return "", err
	}
	return string(buf), nil
}

func DecodeMatchers(raw string) ([]Matcher, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return []Matcher{}, nil
	}
	var out []Matcher
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// MatchesAll checks one silence's matcher list against alert attributes.
// P1-1 semantics:
// - matcher list is AND
// - only "=" is supported
func MatchesAll(matchers []Matcher, attrs map[string]string) bool {
	for _, matcher := range matchers {
		key := NormalizeMatcherKey(matcher.Key)
		if !IsAllowedMatcherKey(key) {
			return false
		}
		if NormalizeMatcherOp(matcher.Op) != MatcherOpEqual {
			return false
		}
		expected := strings.TrimSpace(matcher.Value)
		if expected == "" {
			return false
		}
		actual, ok := attrs[key]
		if !ok {
			return false
		}
		if strings.TrimSpace(actual) != expected {
			return false
		}
	}
	return true
}
