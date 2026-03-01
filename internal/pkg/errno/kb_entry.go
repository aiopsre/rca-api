package errno

import (
	"net/http"

	"github.com/onexstack/onexstack/pkg/errorsx"
)

// KB Entry related errors.

// ErrKBEntryNotFound indicates the requested KB entry was not found.
var ErrKBEntryNotFound = errorsx.New(http.StatusNotFound, "NotFound.KBEntryNotFound", "The requested KB entry was not found.")

// ErrKBEntryAlreadyExists indicates a KB entry with the same kb_id already exists.
var ErrKBEntryAlreadyExists = errorsx.New(http.StatusConflict, "Conflict.KBEntryAlreadyExists", "A KB entry with the same kb_id already exists.")

// ErrKBEntryCreateFailed indicates failure to create KB entry.
var ErrKBEntryCreateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.KBEntryCreateFailed", "Failed to create the KB entry.")

// ErrKBEntryUpdateFailed indicates failure to update KB entry.
var ErrKBEntryUpdateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.KBEntryUpdateFailed", "Failed to update the KB entry.")

// ErrKBEntryDeleteFailed indicates failure to delete KB entry.
var ErrKBEntryDeleteFailed = errorsx.New(http.StatusInternalServerError, "InternalError.KBEntryDeleteFailed", "Failed to delete the KB entry.")

// ErrKBEntryGetFailed indicates failure to get KB entry.
var ErrKBEntryGetFailed = errorsx.New(http.StatusInternalServerError, "InternalError.KBEntryGetFailed", "Failed to retrieve the KB entry.")

// ErrKBEntryListFailed indicates failure to list KB entries.
var ErrKBEntryListFailed = errorsx.New(http.StatusInternalServerError, "InternalError.KBEntryListFailed", "Failed to list KB entries.")