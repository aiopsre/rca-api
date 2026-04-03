package errno

import (
	"net/http"

	"github.com/onexstack/onexstack/pkg/errorsx"
)

var (
	// ErrSessionContextNotFound indicates session context does not exist.
	ErrSessionContextNotFound = errorsx.New(http.StatusNotFound, "NotFound.SessionContextNotFound", "The requested session context was not found.")
	// ErrSessionContextCreateFailed indicates session context create failure.
	ErrSessionContextCreateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.SessionContextCreateFailed", "Failed to create session context.")
	// ErrSessionContextUpdateFailed indicates session context update failure.
	ErrSessionContextUpdateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.SessionContextUpdateFailed", "Failed to update session context.")
	// ErrSessionContextGetFailed indicates session context get failure.
	ErrSessionContextGetFailed = errorsx.New(http.StatusInternalServerError, "InternalError.SessionContextGetFailed", "Failed to retrieve session context.")
	// ErrSessionContextListFailed indicates session context list failure.
	ErrSessionContextListFailed = errorsx.New(http.StatusInternalServerError, "InternalError.SessionContextListFailed", "Failed to list session contexts.")
	// ErrSessionContextConflict indicates session context uniqueness conflict.
	ErrSessionContextConflict = errorsx.New(http.StatusConflict, "Conflict.SessionContextConflict", "Session context conflicts with existing business key.")
	// ErrSessionHistoryCreateFailed indicates session history event create failure.
	ErrSessionHistoryCreateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.SessionHistoryCreateFailed", "Failed to create session history event.")
	// ErrSessionHistoryListFailed indicates session history event list failure.
	ErrSessionHistoryListFailed = errorsx.New(http.StatusInternalServerError, "InternalError.SessionHistoryListFailed", "Failed to list session history events.")
)
