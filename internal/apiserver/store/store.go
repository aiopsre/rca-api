package store

import (
	"context"
	"flag"
	"sync"

	"github.com/google/wire"
	"github.com/onexstack/onexstack/pkg/store/where"
	"gorm.io/gorm"
)

// ProviderSet defines the dependency injection providers for the store layer.
// It binds the abstract Interface to the concrete implementation *store.
var ProviderSet = wire.NewSet(NewStore, wire.Bind(new(IStore), new(*store)))

var (
	once sync.Once
	// S is a global variable for convenient access to the initialized store
	// instance from other packages.
	S *store
)

// IStore defines the methods that the persistence layer must implement.
//
//nolint:interfacebloat // Aggregation interface intentionally exposes all domain stores.
type IStore interface {
	// DB returns the underlying *gorm.DB instance, optionally applying filter conditions.
	DB(ctx context.Context, wheres ...where.Where) *gorm.DB
	// TX executes the given function within a database transaction.
	TX(ctx context.Context, fn func(ctx context.Context) error) error
	Fake() FakeStore
	Incident() IncidentStore
	AlertEvent() AlertEventStore
	Datasource() DatasourceStore
	Evidence() EvidenceStore
	AIJob() AIJobStore
	AIToolCall() AIToolCallStore
	Silence() SilenceStore
}

// txKey is the context key for storing the transaction *gorm.DB instance.
type txKey struct{}

// store is the concrete implementation of the Interface.
type store struct {
	db *gorm.DB

	// Additional database instances can be added as needed.
	// Example: fake *gorm.DB
}

// Ensure store implements the Interface at compile time.
var _ IStore = (*store)(nil)

// NewStore creates and returns a new store instance.
func NewStore(db *gorm.DB) *store {
	// Initialize the singleton store instance only once.
	once.Do(func() { S = &store{db} })

	return S
}

// ResetForTest resets package-level singleton state.
// It should only be used by tests that require strict store isolation.
func ResetForTest() {
	// Safety guard: this function must never be called from production code.
	// In normal binaries, the "test" flags are not registered, so we can detect
	// that we're not running under `go test`.
	if flag.Lookup("test.v") == nil {
		panic("store.ResetForTest must only be called from tests")
	}
	once = sync.Once{}
	S = nil
}

// DB returns the database instance. If a transaction exists in the context,
// it returns the transactional DB; otherwise, it returns the core DB.
// Optional 'where' clauses can be applied to the returned DB instance.
func (s *store) DB(ctx context.Context, wheres ...where.Where) *gorm.DB {
	db := s.db
	// Retrieve transaction from context if it exists.
	if tx, ok := ctx.Value(txKey{}).(*gorm.DB); ok {
		db = tx
	}

	for _, w := range wheres {
		db = w.Where(db)
	}

	return db
}

// FakeDB is used to demonstrate multiple database instances.
// It returns a nil gorm.DB, indicating a fake database.
func (s *store) FakeDB(ctx context.Context) *gorm.DB { return nil }

// TX executes the provided function fn within a database transaction.
// It injects the transaction handle into the context passed to fn.
func (s *store) TX(ctx context.Context, fn func(ctx context.Context) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Inject the transaction DB into the context.
		ctx := context.WithValue(ctx, txKey{}, tx)
		return fn(ctx)
	})
}

// Fake returns an instance that implements the FakeStore interface.
func (s *store) Fake() FakeStore {
	return newFakeStore(s)
}

func (s *store) Incident() IncidentStore {
	return newIncidentStore(s)
}

func (s *store) AlertEvent() AlertEventStore {
	return newAlertEventStore(s)
}

func (s *store) Datasource() DatasourceStore {
	return newDatasourceStore(s)
}

func (s *store) Evidence() EvidenceStore {
	return newEvidenceStore(s)
}

func (s *store) AIJob() AIJobStore {
	return newAIJobStore(s)
}

func (s *store) AIToolCall() AIToolCallStore {
	return newAIToolCallStore(s)
}

func (s *store) Silence() SilenceStore {
	return newSilenceStore(s)
}
