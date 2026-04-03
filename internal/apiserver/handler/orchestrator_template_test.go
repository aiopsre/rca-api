package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/orchestratorregistry"
)

func TestRegisterAndListOrchestratorTemplates_Success(t *testing.T) {
	backend := newFakeTemplateRegistryBackend()
	restore := orchestratorregistry.SetBackendForTest(backend)
	defer restore()

	baseURL, cleanup, _, client := newTestServer(t)
	defer cleanup()

	registerOne := []byte(`{
	  "instanceID": "orc-1",
	  "templates": [
	    {"templateID": "basic_rca", "version": "v1"},
	    {"templateID": "fast_path", "version": "v2"}
	  ]
	}`)
	status, body, err := doJSONRequest(client, http.MethodPost, baseURL+"/v1/orchestrator/templates/register", registerOne)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, string(body))
	respData := extractDataContainer(body)
	require.EqualValues(t, 2, respData["count"])

	registerTwo := []byte(`{
	  "instanceID": "orc-2",
	  "templates": [
	    {"templateID": "basic_rca", "version": "v1"}
	  ]
	}`)
	status, body, err = doJSONRequest(client, http.MethodPost, baseURL+"/v1/orchestrator/templates/register", registerTwo)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, string(body))

	status, body, err = doJSONRequest(client, http.MethodGet, baseURL+"/v1/orchestrator/templates", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, string(body))

	data := extractDataContainer(body)
	rawTemplates, ok := data["templates"].([]any)
	require.True(t, ok)
	require.Len(t, rawTemplates, 2)

	entries := make(map[string]map[string]any, len(rawTemplates))
	for _, item := range rawTemplates {
		obj, ok := item.(map[string]any)
		require.True(t, ok)
		entryKey := fmt.Sprintf("%s@%s", extractString(obj, "templateID", "templateId", "template_id"), extractString(obj, "version"))
		entries[entryKey] = obj
	}

	basic := entries["basic_rca@v1"]
	require.NotNil(t, basic)
	basicInstances := extractStringSlice(basic["instances"])
	sort.Strings(basicInstances)
	require.Equal(t, []string{"orc-1", "orc-2"}, basicInstances)

	fast := entries["fast_path@v2"]
	require.NotNil(t, fast)
	require.Equal(t, []string{"orc-1"}, extractStringSlice(fast["instances"]))
}

func TestRegisterOrchestratorTemplates_InvalidRequest(t *testing.T) {
	backend := newFakeTemplateRegistryBackend()
	restore := orchestratorregistry.SetBackendForTest(backend)
	defer restore()

	baseURL, cleanup, _, client := newTestServer(t)
	defer cleanup()

	invalidPayload := []byte(`{"instanceID":"","templates":[{"templateID":"basic_rca"}]}`)
	status, _, err := doJSONRequest(client, http.MethodPost, baseURL+"/v1/orchestrator/templates/register", invalidPayload)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status)
}

func TestRegisterOrchestratorTemplates_RedisUnavailable(t *testing.T) {
	restore := orchestratorregistry.SetBackendForTest(nil)
	defer restore()

	baseURL, cleanup, _, client := newTestServer(t)
	defer cleanup()

	payload := []byte(`{"instanceID":"orc-1","templates":[{"templateID":"basic_rca","version":"v1"}]}`)
	status, _, err := doJSONRequest(client, http.MethodPost, baseURL+"/v1/orchestrator/templates/register", payload)
	require.NoError(t, err)
	require.Equal(t, http.StatusInternalServerError, status)
}

func newFakeTemplateRegistryBackend() *fakeTemplateRegistryBackend {
	return &fakeTemplateRegistryBackend{
		values: make(map[string]string),
	}
}

type fakeTemplateRegistryBackend struct {
	mu      sync.Mutex
	values  map[string]string
	setErr  error
	getErr  error
	scanErr error
}

func (f *fakeTemplateRegistryBackend) Set(_ context.Context, key string, value string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return f.setErr
	}
	f.values[key] = value
	return nil
}

func (f *fakeTemplateRegistryBackend) Get(_ context.Context, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return "", f.getErr
	}
	value, ok := f.values[key]
	if !ok {
		return "", redis.Nil
	}
	return value, nil
}

func (f *fakeTemplateRegistryBackend) Scan(_ context.Context, _ uint64, match string, _ int64) ([]string, uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.scanErr != nil {
		return nil, 0, f.scanErr
	}
	prefix := strings.TrimSuffix(match, "*")
	keys := make([]string, 0, len(f.values))
	for key := range f.values {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys, 0, nil
}

func extractStringSlice(raw any) []string {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if value, ok := item.(string); ok {
			trimmed := strings.TrimSpace(value)
			if trimmed != "" {
				out = append(out, trimmed)
			}
		}
	}
	return out
}

// compile-time check that fake backend still conforms to orchestration registry backend.
var _ orchestratorregistry.Backend = (*fakeTemplateRegistryBackend)(nil)

func TestListOrchestratorTemplates_RedisUnavailable(t *testing.T) {
	restore := orchestratorregistry.SetBackendForTest(nil)
	defer restore()

	baseURL, cleanup, _, client := newTestServer(t)
	defer cleanup()

	status, _, err := doJSONRequest(client, http.MethodGet, baseURL+"/v1/orchestrator/templates", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusInternalServerError, status)
}

func TestListOrchestratorTemplates_RedisDecodeError(t *testing.T) {
	backend := newFakeTemplateRegistryBackend()
	backend.values["rca:orchestrator:templates:instance:orc-x"] = "{" // invalid json
	restore := orchestratorregistry.SetBackendForTest(backend)
	defer restore()

	baseURL, cleanup, _, client := newTestServer(t)
	defer cleanup()

	status, _, err := doJSONRequest(client, http.MethodGet, baseURL+"/v1/orchestrator/templates", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusInternalServerError, status)
}

func TestRegisterOrchestratorTemplates_RedisWriteError(t *testing.T) {
	backend := newFakeTemplateRegistryBackend()
	backend.setErr = errors.New("redis down")
	restore := orchestratorregistry.SetBackendForTest(backend)
	defer restore()

	baseURL, cleanup, _, client := newTestServer(t)
	defer cleanup()

	payload := []byte(`{"instanceID":"orc-1","templates":[{"templateID":"basic_rca","version":"v1"}]}`)
	status, _, err := doJSONRequest(client, http.MethodPost, baseURL+"/v1/orchestrator/templates/register", payload)
	require.NoError(t, err)
	require.Equal(t, http.StatusInternalServerError, status)
}
