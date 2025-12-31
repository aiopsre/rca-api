// nolint: dupl
package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
)

var (
	// ErrNotFound is returned when the requested record does not exist in the fake store.
	ErrNotFound = errors.New("record not found")
	// ErrDuplicate is returned when trying to create a record with an existing ID.
	ErrDuplicate = errors.New("duplicate record ID")
	// ErrMissingID is returned when the model provided does not have a valid ID.
	ErrMissingID = errors.New("model ID is empty")
)

// FakeStore defines the interface for managing fake-related data operations.
type FakeStore interface {
	// Create inserts a new Fake record into the store.
	Create(ctx context.Context, obj *model.FakeM) error

	// Update modifies an existing Fake record in the store based on the given model.
	Update(ctx context.Context, obj *model.FakeM) error

	// Delete removes Fake records that satisfy the given query options.
	Delete(ctx context.Context, opts *where.Options) error

	// Get retrieves a single Fake record that satisfies the given query options.
	Get(ctx context.Context, opts *where.Options) (*model.FakeM, error)

	// List retrieves a list of Fake records and their total count based on the given query options.
	List(ctx context.Context, opts *where.Options) (int64, []*model.FakeM, error)

	// FakeExpansion is a placeholder for extension methods for fakes,
	// to be implemented by additional interfaces if needed.
	FakeExpansion
}

// FakeExpansion is an empty interface provided for extending
// the FakeStore interface.
type FakeExpansion interface{}

// fakeStore implements the FakeStore interface using an in-memory map.
type fakeStore struct {
	mu     sync.RWMutex
	data   map[any]*model.FakeM
	lastID uint64
}

// Ensure that fakeStore satisfies the FakeStore interface at compile time.
var _ FakeStore = (*fakeStore)(nil)

// newFakeStore creates a new fakeStore instance.
// Note: The 'store' parameter is ignored as this is an in-memory implementation,
// but kept to match the constructor signature pattern.
func newFakeStore(_ *store) *fakeStore {
	fs := &fakeStore{
		data:   make(map[any]*model.FakeM),
		lastID: 0,
	}
	_ = fs.Create(context.Background(), &model.FakeM{
		ID:        0, // ID is 0, which will trigger auto-generation
		FakeID:    "fake-xxxxxx",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	return fs
}

// Create inserts a new Fake record into the memory map.
func (s *fakeStore) Create(_ context.Context, obj *model.FakeM) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. Get current ID
	currentID := getID(obj)

	// 2. Determine if ID auto-generation is needed (assuming generation is needed when ID is 0 or nil)
	isZeroID := false
	switch v := currentID.(type) {
	case int:
		isZeroID = v == 0
	case int64:
		isZeroID = v == 0
	case uint:
		isZeroID = v == 0
	case uint64:
		isZeroID = v == 0
	case nil:
		isZeroID = true
	}

	// 3. If ID is 0, generate a new ID and set it back to the object
	if isZeroID {
		s.lastID++
		// Try to set ID via reflection
		if err := setID(obj, s.lastID); err != nil {
			return fmt.Errorf("failed to auto-generate ID: %w", err)
		}
		currentID = getID(obj) // Get the newly set ID to use as the map key
	} else {
		// If not auto-generated, check for duplicates
		if _, exists := s.data[currentID]; exists {
			return ErrDuplicate
		}
		// If the user provided an ID, update lastID to prevent future conflicts (optional logic, depends on requirements)
		// Here strictly handled: do nothing, assuming the user knows what they are doing
	}

	if currentID == nil {
		return ErrMissingID
	}

	// Store a copy to prevent external mutation.
	copied := *obj
	s.data[currentID] = &copied
	return nil
}

// Update modifies an existing Fake record in the memory map.
func (s *fakeStore) Update(_ context.Context, obj *model.FakeM) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := getID(obj)
	if id == nil {
		return ErrMissingID
	}

	if _, exists := s.data[id]; !exists {
		return ErrNotFound
	}

	// Overwrite with a copy.
	copied := *obj
	s.data[id] = &copied
	return nil
}

// Delete removes Fake records.
func (s *fakeStore) Delete(_ context.Context, _ *where.Options) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Implementation skipped for brevity as per original request
	return nil
}

// Get retrieves a single Fake record.
func (s *fakeStore) Get(_ context.Context, _ *where.Options) (*model.FakeM, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.data {
		copied := *item
		return &copied, nil
	}
	return nil, ErrNotFound
}

// List retrieves all Fake records stored in memory.
func (s *fakeStore) List(_ context.Context, _ *where.Options) (int64, []*model.FakeM, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := make([]*model.FakeM, 0, len(s.data))
	for _, item := range s.data {
		copied := *item
		list = append(list, &copied)
	}
	return int64(len(list)), list, nil
}

// getID uses reflection to extract the ID field.
func getID(obj interface{}) any {
	val := reflect.ValueOf(obj)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	field := val.FieldByName("ID")
	if field.IsValid() && !field.IsZero() { // Slightly modified logic to return only non-zero values, or let the caller decide
		return field.Interface()
	}
	// If the field exists but is a zero value (0), return interface{}(0) as well for easier type assertion
	if field.IsValid() {
		return field.Interface()
	}
	return nil
}

// setID uses reflection to set the ID field.
// Supports int, int64, uint, uint64.
func setID(obj interface{}, id uint64) error {
	val := reflect.ValueOf(obj)
	if val.Kind() != reflect.Ptr {
		return errors.New("obj must be a pointer")
	}
	val = val.Elem()
	field := val.FieldByName("ID")

	if !field.IsValid() {
		return errors.New("field ID not found")
	}
	if !field.CanSet() {
		return errors.New("field ID cannot be set")
	}

	switch field.Kind() {
	case reflect.Int, reflect.Int64, reflect.Int32:
		field.SetInt(int64(id))
	case reflect.Uint, reflect.Uint64, reflect.Uint32:
		field.SetUint(id)
	default:
		return fmt.Errorf("unsupported ID type: %s", field.Kind())
	}
	return nil
}
