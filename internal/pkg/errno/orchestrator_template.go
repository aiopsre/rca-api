package errno

import (
	"net/http"

	"github.com/onexstack/onexstack/pkg/errorsx"
)

var (
	// ErrOrchestratorTemplateRegisterFailed indicates template registry register operation failed.
	ErrOrchestratorTemplateRegisterFailed = errorsx.New(
		http.StatusInternalServerError,
		"InternalError.OrchestratorTemplateRegisterFailed",
		"The orchestrator template register operation failed.",
	)
	// ErrOrchestratorTemplateListFailed indicates template registry list operation failed.
	ErrOrchestratorTemplateListFailed = errorsx.New(
		http.StatusInternalServerError,
		"InternalError.OrchestratorTemplateListFailed",
		"The orchestrator template list operation failed.",
	)
)
