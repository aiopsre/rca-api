package errno

import (
	"net/http"

	"github.com/onexstack/onexstack/pkg/errorsx"
)

var (
	// ErrSessionContextRevisionConflict indicates optimistic session patch conflict.
	ErrSessionContextRevisionConflict = errorsx.New(
		http.StatusConflict,
		"Conflict.SessionContextRevisionConflict",
		"The session context revision does not match the latest stored value.",
	)
)
