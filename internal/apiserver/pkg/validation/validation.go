package validation

import (
	"github.com/google/wire"

	"zk8s.com/rca-api/internal/apiserver/store"
)

// Validator handles custom business validation logic.
// It holds dependencies required for deep validation, such as database access.
type Validator struct {
	// Some complex validation logic may require direct database queries.
	// This is just an example. If validation requires other dependencies
	// like clients, services, resources, etc., they can all be injected here.
	store store.IStore
}

// ProviderSet is the Wire provider set for the validation package.
var ProviderSet = wire.NewSet(New, wire.Bind(new(any), new(*Validator)))

// New creates and initializes a new Validator instance with the required dependencies.
func New(ds store.IStore) *Validator {
	return &Validator{store: ds}
}
