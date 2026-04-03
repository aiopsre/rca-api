package errno

import (
	"net/http"

	"github.com/onexstack/onexstack/pkg/errorsx"
)

var (
	// ErrOrchestratorStrategyNotFound indicates normalized pipeline has no mapped strategy.
	ErrOrchestratorStrategyNotFound = errorsx.New(
		http.StatusNotFound,
		"NotFound.OrchestratorStrategyNotFound",
		"The requested orchestrator strategy mapping was not found.",
	)
	// ErrOrchestratorStrategyConfigInvalid indicates server-side strategy config is invalid.
	ErrOrchestratorStrategyConfigInvalid = errorsx.New(
		http.StatusInternalServerError,
		"InternalError.OrchestratorStrategyConfigInvalid",
		"The server-side orchestrator strategy config is invalid.",
	)
	// ErrOrchestratorStrategyTemplateNotRegistered indicates strategy template is not registered.
	ErrOrchestratorStrategyTemplateNotRegistered = errorsx.New(
		http.StatusNotFound,
		"NotFound.OrchestratorStrategyTemplateNotRegistered",
		"The resolved strategy template has not been registered by orchestrator.",
	)
)
