package orchestratorregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

const (
	instanceKeyPrefix = "rca:orchestrator:templates:instance:"
	templateTTL       = 120 * time.Second
	scanMatchPattern  = instanceKeyPrefix + "*"
	scanCount         = int64(200)
)

var errRegistryUnavailable = errors.New("orchestrator template registry backend is not configured")

// Backend defines minimal redis operations used by template registry.
// It is exported so tests can inject deterministic fake backends.
type Backend interface {
	Set(ctx context.Context, key string, value string, expiration time.Duration) error
	Get(ctx context.Context, key string) (string, error)
	Scan(ctx context.Context, cursor uint64, match string, count int64) ([]string, uint64, error)
}

type redisBackend struct {
	client *redis.Client
}

func (b *redisBackend) Set(ctx context.Context, key string, value string, expiration time.Duration) error {
	return b.client.Set(ctx, key, value, expiration).Err()
}

func (b *redisBackend) Get(ctx context.Context, key string) (string, error) {
	return b.client.Get(ctx, key).Result()
}

func (b *redisBackend) Scan(ctx context.Context, cursor uint64, match string, count int64) ([]string, uint64, error) {
	return b.client.Scan(ctx, cursor, match, count).Result()
}

var (
	backendMu sync.RWMutex
	backend   Backend
)

// ConfigureRedisClient binds a redis client as template registry backend.
func ConfigureRedisClient(client *redis.Client) error {
	if client == nil {
		return errRegistryUnavailable
	}
	backendMu.Lock()
	backend = &redisBackend{client: client}
	backendMu.Unlock()
	return nil
}

// SetBackendForTest injects test backend and returns a restore function.
func SetBackendForTest(testBackend Backend) func() {
	backendMu.Lock()
	previous := backend
	backend = testBackend
	backendMu.Unlock()
	return func() {
		backendMu.Lock()
		backend = previous
		backendMu.Unlock()
	}
}

func getBackend() (Backend, error) {
	backendMu.RLock()
	current := backend
	backendMu.RUnlock()
	if current == nil {
		return nil, errRegistryUnavailable
	}
	return current, nil
}

type templateValue struct {
	TemplateID string `json:"template_id"`
	Version    string `json:"version,omitempty"`
}

type instanceValue struct {
	InstanceID string          `json:"instance_id"`
	Templates  []templateValue `json:"templates"`
}

// Register stores one orchestrator instance template set with TTL.
func Register(ctx context.Context, instanceID string, templates []*v1.OrchestratorTemplate) error {
	store, err := getBackend()
	if err != nil {
		return err
	}

	normalizedInstanceID := strings.TrimSpace(instanceID)
	if normalizedInstanceID == "" {
		return fmt.Errorf("instance_id is required")
	}
	normalizedTemplates := normalizeTemplates(templates)
	if len(normalizedTemplates) == 0 {
		return fmt.Errorf("templates is empty")
	}

	payload := instanceValue{
		InstanceID: normalizedInstanceID,
		Templates:  normalizedTemplates,
	}
	raw, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return fmt.Errorf("marshal template registry payload failed: %w", marshalErr)
	}

	key := instanceKeyPrefix + normalizedInstanceID
	if setErr := store.Set(ctx, key, string(raw), templateTTL); setErr != nil {
		return fmt.Errorf("redis set failed: %w", setErr)
	}
	return nil
}

// List aggregates all alive instance registrations into template entries.
func List(ctx context.Context) ([]*v1.OrchestratorTemplateEntry, error) {
	store, err := getBackend()
	if err != nil {
		return nil, err
	}

	keys, listErr := listInstanceKeys(ctx, store)
	if listErr != nil {
		return nil, listErr
	}

	entries := make(map[string]*templateEntryAggregate)
	for _, key := range keys {
		raw, getErr := store.Get(ctx, key)
		if getErr != nil {
			if errors.Is(getErr, redis.Nil) {
				continue
			}
			return nil, fmt.Errorf("redis get failed: key=%s err=%w", key, getErr)
		}

		var payload instanceValue
		if decodeErr := json.Unmarshal([]byte(raw), &payload); decodeErr != nil {
			return nil, fmt.Errorf("decode template registry payload failed: key=%s err=%w", key, decodeErr)
		}

		instanceID := strings.TrimSpace(payload.InstanceID)
		if instanceID == "" {
			instanceID = extractInstanceIDFromKey(key)
		}
		if instanceID == "" {
			continue
		}

		for _, item := range payload.Templates {
			templateID := strings.TrimSpace(item.TemplateID)
			if templateID == "" {
				continue
			}
			version := strings.TrimSpace(item.Version)
			aggKey := aggregateKey(templateID, version)
			agg, exists := entries[aggKey]
			if !exists {
				agg = &templateEntryAggregate{templateID: templateID, version: version, instances: make(map[string]struct{})}
				entries[aggKey] = agg
			}
			agg.instances[instanceID] = struct{}{}
		}
	}

	return materializeEntries(entries), nil
}

func listInstanceKeys(ctx context.Context, store Backend) ([]string, error) {
	keys := make([]string, 0)
	cursor := uint64(0)
	for {
		matched, nextCursor, err := store.Scan(ctx, cursor, scanMatchPattern, scanCount)
		if err != nil {
			return nil, fmt.Errorf("redis scan failed: %w", err)
		}
		if len(matched) > 0 {
			keys = append(keys, matched...)
		}
		if nextCursor == 0 {
			break
		}
		cursor = nextCursor
	}
	sort.Strings(keys)
	return keys, nil
}

func normalizeTemplates(templates []*v1.OrchestratorTemplate) []templateValue {
	normalized := make([]templateValue, 0, len(templates))
	indexByTemplateID := make(map[string]int, len(templates))

	for _, item := range templates {
		if item == nil {
			continue
		}
		templateID := strings.TrimSpace(item.GetTemplateID())
		if templateID == "" {
			continue
		}
		version := strings.TrimSpace(item.GetVersion())

		if index, exists := indexByTemplateID[templateID]; exists {
			if version != "" {
				normalized[index].Version = version
			}
			continue
		}

		indexByTemplateID[templateID] = len(normalized)
		normalized = append(normalized, templateValue{TemplateID: templateID, Version: version})
	}

	return normalized
}

func extractInstanceIDFromKey(key string) string {
	if !strings.HasPrefix(key, instanceKeyPrefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(key, instanceKeyPrefix))
}

func aggregateKey(templateID string, version string) string {
	return templateID + "\x00" + version
}

type templateEntryAggregate struct {
	templateID string
	version    string
	instances  map[string]struct{}
}

func materializeEntries(entries map[string]*templateEntryAggregate) []*v1.OrchestratorTemplateEntry {
	if len(entries) == 0 {
		return []*v1.OrchestratorTemplateEntry{}
	}

	aggregated := make([]*templateEntryAggregate, 0, len(entries))
	for _, item := range entries {
		aggregated = append(aggregated, item)
	}
	sort.SliceStable(aggregated, func(i, j int) bool {
		if aggregated[i].templateID == aggregated[j].templateID {
			return aggregated[i].version < aggregated[j].version
		}
		return aggregated[i].templateID < aggregated[j].templateID
	})

	out := make([]*v1.OrchestratorTemplateEntry, 0, len(aggregated))
	for _, item := range aggregated {
		instances := make([]string, 0, len(item.instances))
		for instanceID := range item.instances {
			instances = append(instances, instanceID)
		}
		sort.Strings(instances)
		out = append(out, &v1.OrchestratorTemplateEntry{
			TemplateID: item.templateID,
			Version:    item.version,
			Instances:  instances,
		})
	}

	return out
}
