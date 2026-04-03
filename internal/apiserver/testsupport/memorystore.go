package testsupport

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	apistore "github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/internal/pkg/rid"
	storewhere "github.com/aiopsre/rca-api/pkg/store/where"
)

// MemoryStore is an in-memory implementation of apistore.IStore for unit tests.
type MemoryStore struct {
	mcpServers  *memoryMcpServerStore
	bindings    *memoryToolsetProviderBindingStore
	toolMetadata apistore.ToolMetadataStore
}

var _ apistore.IStore = (*MemoryStore)(nil)

// NewMemoryStore creates an isolated in-memory store for tests.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		mcpServers: newMemoryMcpServerStore(),
		bindings:   newMemoryToolsetProviderBindingStore(),
	}
}

func (s *MemoryStore) DB(_ context.Context, _ ...storewhere.Where) *gorm.DB { return nil }

func (s *MemoryStore) TX(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

func (s *MemoryStore) Fake() apistore.FakeStore { return nil }
func (s *MemoryStore) Incident() apistore.IncidentStore { return nil }
func (s *MemoryStore) AlertEvent() apistore.AlertEventStore { return nil }
func (s *MemoryStore) Evidence() apistore.EvidenceStore { return nil }
func (s *MemoryStore) AIJob() apistore.AIJobStore { return nil }
func (s *MemoryStore) AIJobQueueSignal() apistore.AIJobQueueSignalStore { return nil }
func (s *MemoryStore) AIToolCall() apistore.AIToolCallStore { return nil }
func (s *MemoryStore) KBEntry() apistore.KBEntryStore { return nil }
func (s *MemoryStore) Silence() apistore.SilenceStore { return nil }
func (s *MemoryStore) NoticeChannel() apistore.NoticeChannelStore { return nil }
func (s *MemoryStore) NoticeDelivery() apistore.NoticeDeliveryStore { return nil }
func (s *MemoryStore) IncidentActionLog() apistore.IncidentActionLogStore { return nil }
func (s *MemoryStore) SessionContext() apistore.SessionContextStore { return nil }
func (s *MemoryStore) SessionHistoryEvent() apistore.SessionHistoryEventStore { return nil }
func (s *MemoryStore) InternalStrategyConfig() apistore.InternalStrategyConfigStore { return nil }
func (s *MemoryStore) RBAC() apistore.RBACStore { return nil }
func (s *MemoryStore) AlertingPolicy() apistore.AlertingPolicyStore { return nil }
func (s *MemoryStore) Playbook() apistore.PlaybookStore { return nil }
func (s *MemoryStore) ToolMetadata() apistore.ToolMetadataStore { return s.toolMetadata }
func (s *MemoryStore) McpServer() apistore.McpServerStore { return s.mcpServers }
func (s *MemoryStore) ToolsetProviderBinding() apistore.ToolsetProviderBindingStore { return s.bindings }

// memoryMcpServerStore is a test-only in-memory McpServerStore.
type memoryMcpServerStore struct {
	mu     sync.RWMutex
	nextID int64
	byID   map[string]*model.McpServerM
	byName map[string]string
}

var _ apistore.McpServerStore = (*memoryMcpServerStore)(nil)

func newMemoryMcpServerStore() *memoryMcpServerStore {
	return &memoryMcpServerStore{
		byID:   make(map[string]*model.McpServerM),
		byName: make(map[string]string),
	}
}

func (s *memoryMcpServerStore) Create(_ context.Context, obj *model.McpServerM) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if obj == nil {
		return fmt.Errorf("nil object")
	}
	if obj.ID == 0 {
		s.nextID++
		obj.ID = s.nextID
	}
	if obj.McpServerID == "" {
		obj.McpServerID = rid.McpServerID.New(uint64(obj.ID))
	}
	if _, exists := s.byName[obj.Name]; exists {
		return fmt.Errorf("duplicate record")
	}
	now := time.Now()
	if obj.CreatedAt.IsZero() {
		obj.CreatedAt = now
	}
	if obj.UpdatedAt.IsZero() {
		obj.UpdatedAt = now
	}
	copied := *obj
	s.byID[obj.McpServerID] = &copied
	s.byName[obj.Name] = obj.McpServerID
	return nil
}

func (s *memoryMcpServerStore) Update(_ context.Context, obj *model.McpServerM) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if obj == nil || obj.McpServerID == "" {
		return fmt.Errorf("record not found")
	}
	if _, exists := s.byID[obj.McpServerID]; !exists {
		return fmt.Errorf("record not found")
	}
	now := time.Now()
	obj.UpdatedAt = now
	copied := *obj
	s.byID[obj.McpServerID] = &copied
	s.byName[obj.Name] = obj.McpServerID
	return nil
}

func (s *memoryMcpServerStore) Delete(_ context.Context, opts *storewhere.Options) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	id, name := extractFilters(opts)
	if id != "" {
		if obj, ok := s.byID[id]; ok {
			delete(s.byName, obj.Name)
			delete(s.byID, id)
		}
		return nil
	}
	if name != "" {
		if id = s.byName[name]; id != "" {
			delete(s.byName, name)
			delete(s.byID, id)
		}
	}
	return nil
}

func (s *memoryMcpServerStore) Get(_ context.Context, opts *storewhere.Options) (*model.McpServerM, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	id, name := extractFilters(opts)
	if id != "" {
		if obj, ok := s.byID[id]; ok {
			copied := *obj
			return &copied, nil
		}
		return nil, fmt.Errorf("record not found")
	}
	if name != "" {
		if id = s.byName[name]; id != "" {
			obj := s.byID[id]
			if obj != nil {
				copied := *obj
				return &copied, nil
			}
		}
		return nil, fmt.Errorf("record not found")
	}
	return nil, fmt.Errorf("record not found")
}

func (s *memoryMcpServerStore) List(_ context.Context, opts *storewhere.Options) (int64, []*model.McpServerM, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []*model.McpServerM
	for _, obj := range s.byID {
		if matchesMcpServer(obj, opts) {
			copied := *obj
			out = append(out, &copied)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	total := int64(len(out))
	out = applyPagination(out, opts)
	return total, out, nil
}

// memoryToolsetProviderBindingStore is a test-only in-memory ToolsetProviderBindingStore.
type memoryToolsetProviderBindingStore struct {
	mu       sync.RWMutex
	nextID   int64
	byKey    map[string]*model.ToolsetProviderBinding
}

var _ apistore.ToolsetProviderBindingStore = (*memoryToolsetProviderBindingStore)(nil)

func newMemoryToolsetProviderBindingStore() *memoryToolsetProviderBindingStore {
	return &memoryToolsetProviderBindingStore{
		byKey: make(map[string]*model.ToolsetProviderBinding),
	}
}

func (s *memoryToolsetProviderBindingStore) Create(_ context.Context, obj *model.ToolsetProviderBinding) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if obj == nil {
		return fmt.Errorf("nil object")
	}
	key := bindingKey(obj.ToolsetName, obj.McpServerID)
	if _, exists := s.byKey[key]; exists {
		return fmt.Errorf("duplicate record")
	}
	s.nextID++
	obj.ID = s.nextID
	now := time.Now()
	if obj.CreatedAt.IsZero() {
		obj.CreatedAt = now
	}
	if obj.UpdatedAt.IsZero() {
		obj.UpdatedAt = now
	}
	copied := *obj
	s.byKey[key] = &copied
	return nil
}

func (s *memoryToolsetProviderBindingStore) Update(_ context.Context, obj *model.ToolsetProviderBinding) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if obj == nil {
		return fmt.Errorf("record not found")
	}
	key := bindingKey(obj.ToolsetName, obj.McpServerID)
	if _, exists := s.byKey[key]; !exists {
		return fmt.Errorf("record not found")
	}
	copied := *obj
	s.byKey[key] = &copied
	return nil
}

func (s *memoryToolsetProviderBindingStore) Delete(_ context.Context, opts *storewhere.Options) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	toolsetName, mcpServerID := extractBindingFilters(opts)
	if toolsetName != "" && mcpServerID != "" {
		key := bindingKey(toolsetName, mcpServerID)
		delete(s.byKey, key)
	}
	return nil
}

func (s *memoryToolsetProviderBindingStore) Get(_ context.Context, opts *storewhere.Options) (*model.ToolsetProviderBinding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	toolsetName, mcpServerID := extractBindingFilters(opts)
	if toolsetName != "" && mcpServerID != "" {
		key := bindingKey(toolsetName, mcpServerID)
		if obj, ok := s.byKey[key]; ok {
			copied := *obj
			return &copied, nil
		}
	}
	return nil, fmt.Errorf("record not found")
}

func (s *memoryToolsetProviderBindingStore) List(_ context.Context, opts *storewhere.Options) (int64, []*model.ToolsetProviderBinding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []*model.ToolsetProviderBinding
	for _, obj := range s.byKey {
		if matchesBinding(obj, opts) {
			copied := *obj
			out = append(out, &copied)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority == out[j].Priority {
			return out[i].ID < out[j].ID
		}
		return out[i].Priority < out[j].Priority
	})
	total := int64(len(out))
	out = applyPagination(out, opts)
	return total, out, nil
}

func (s *memoryToolsetProviderBindingStore) ListByToolsetNames(_ context.Context, toolsetNames []string) ([]*model.ToolsetProviderBinding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	allowed := make(map[string]struct{}, len(toolsetNames))
	for _, name := range toolsetNames {
		allowed[strings.TrimSpace(name)] = struct{}{}
	}
	var out []*model.ToolsetProviderBinding
	for _, obj := range s.byKey {
		if obj.Enabled {
			if _, ok := allowed[obj.ToolsetName]; ok {
				copied := *obj
				out = append(out, &copied)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority == out[j].Priority {
			return out[i].McpServerID < out[j].McpServerID
		}
		return out[i].Priority < out[j].Priority
	})
	return out, nil
}

func extractFilters(opts *storewhere.Options) (mcpServerID string, name string) {
	if opts == nil {
		return "", ""
	}
	for key, value := range opts.Filters {
		switch strings.ToLower(fmt.Sprint(key)) {
		case "mcp_server_id":
			mcpServerID = fmt.Sprint(value)
		case "name":
			name = fmt.Sprint(value)
		}
	}
	return mcpServerID, name
}

func extractBindingFilters(opts *storewhere.Options) (toolsetName string, mcpServerID string) {
	if opts == nil {
		return "", ""
	}
	for key, value := range opts.Filters {
		switch strings.ToLower(fmt.Sprint(key)) {
		case "toolset_name":
			toolsetName = fmt.Sprint(value)
		case "mcp_server_id":
			mcpServerID = fmt.Sprint(value)
		case "enabled":
			// no-op for single-key lookup
		}
	}
	return strings.TrimSpace(toolsetName), strings.TrimSpace(mcpServerID)
}

func matchesMcpServer(obj *model.McpServerM, opts *storewhere.Options) bool {
	if opts == nil {
		return true
	}
	for key, value := range opts.Filters {
		switch strings.ToLower(fmt.Sprint(key)) {
		case "mcp_server_id":
			if obj.McpServerID != fmt.Sprint(value) {
				return false
			}
		case "name":
			if obj.Name != fmt.Sprint(value) {
				return false
			}
		case "status":
			if obj.Status != fmt.Sprint(value) {
				return false
			}
		}
	}
	return true
}

func matchesBinding(obj *model.ToolsetProviderBinding, opts *storewhere.Options) bool {
	if opts == nil {
		return true
	}
	for key, value := range opts.Filters {
		switch strings.ToLower(fmt.Sprint(key)) {
		case "toolset_name":
			if obj.ToolsetName != fmt.Sprint(value) {
				return false
			}
		case "mcp_server_id":
			if obj.McpServerID != fmt.Sprint(value) {
				return false
			}
		case "enabled":
			want, ok := value.(bool)
			if !ok {
				want = strings.EqualFold(fmt.Sprint(value), "true")
			}
			if obj.Enabled != want {
				return false
			}
		}
	}
	return true
}

func bindingKey(toolsetName, mcpServerID string) string {
	return strings.TrimSpace(toolsetName) + "|" + strings.TrimSpace(mcpServerID)
}

func applyPagination[T any](items []T, opts *storewhere.Options) []T {
	if opts == nil {
		return items
	}
	start := opts.Offset
	if start < 0 {
		start = 0
	}
	if start >= len(items) {
		return nil
	}
	end := len(items)
	if opts.Limit > 0 && start+opts.Limit < end {
		end = start + opts.Limit
	}
	return items[start:end]
}
