package errno

import (
	"net/http"

	"github.com/onexstack/onexstack/pkg/errorsx"
)

var (
	// ErrOrchestratorSkillsetConfigInvalid indicates server-side skillset config is invalid.
	ErrOrchestratorSkillsetConfigInvalid = errorsx.New(
		http.StatusInternalServerError,
		"InternalError.OrchestratorSkillsetConfigInvalid",
		"The server-side orchestrator skillset config is invalid.",
	)
)
