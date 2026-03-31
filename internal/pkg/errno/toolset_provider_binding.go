package errno

import (
	"net/http"

	"github.com/onexstack/onexstack/pkg/errorsx"
)

var (
	ErrToolsetProviderBindingNotFound = errorsx.New(http.StatusNotFound, "NotFound.ToolsetProviderBindingNotFound", "The requested toolset provider binding was not found.")
	ErrToolsetProviderBindingAlreadyExists = errorsx.New(http.StatusConflict, "Conflict.ToolsetProviderBindingAlreadyExists", "A toolset provider binding with the same toolset and MCP server already exists.")
	ErrToolsetProviderBindingCreateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.ToolsetProviderBindingCreateFailed", "Failed to create the toolset provider binding.")
	ErrToolsetProviderBindingUpdateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.ToolsetProviderBindingUpdateFailed", "Failed to update the toolset provider binding.")
	ErrToolsetProviderBindingDeleteFailed = errorsx.New(http.StatusInternalServerError, "InternalError.ToolsetProviderBindingDeleteFailed", "Failed to delete the toolset provider binding.")
	ErrToolsetProviderBindingGetFailed = errorsx.New(http.StatusInternalServerError, "InternalError.ToolsetProviderBindingGetFailed", "Failed to retrieve the toolset provider binding.")
	ErrToolsetProviderBindingListFailed = errorsx.New(http.StatusInternalServerError, "InternalError.ToolsetProviderBindingListFailed", "Failed to list toolset provider bindings.")
)
