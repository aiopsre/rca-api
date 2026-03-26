package options

import "github.com/spf13/pflag"

// IOptions defines methods to implement a generic options.
type IOptions interface {
	// Validate validates all the required options.
	// It can also used to complete options if needed.
	Validate() []error

	// AddFlags registers all option fields as command line flags on the given FlagSet,
	// using the provided fullPrefix directly.
	//
	// The fullPrefix should be a complete prefix string, for example: "onex.otel".
	// Implementations are expected to append their own field names to this prefix
	// to build the final flag names, such as:
	//   --onex.otel.endpoint
	//   --onex.otel.insecure
	AddFlags(fs *pflag.FlagSet, fullPrefix string)
}
