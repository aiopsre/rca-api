package errno

import (
	"net/http"

	"github.com/onexstack/onexstack/pkg/errorsx"
)

var (
	// ErrOrchestratorToolsetNotFound indicates normalized pipeline has no mapped toolset.
	ErrOrchestratorToolsetNotFound = errorsx.New(
		http.StatusNotFound,
		"NotFound.OrchestratorToolsetNotFound",
		"The requested orchestrator toolset mapping was not found.",
	)
	// ErrOrchestratorToolsetConfigInvalid indicates server-side toolset config is invalid.
	ErrOrchestratorToolsetConfigInvalid = errorsx.New(
		http.StatusInternalServerError,
		"InternalError.OrchestratorToolsetConfigInvalid",
		"The server-side orchestrator toolset config is invalid.",
	)
)
