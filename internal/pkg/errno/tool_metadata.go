package errno

import (
	"net/http"

	"github.com/onexstack/onexstack/pkg/errorsx"
)

var (
	// ErrToolMetadataNotFound indicates tool metadata does not exist.
	ErrToolMetadataNotFound = errorsx.New(http.StatusNotFound, "NotFound.ToolMetadataNotFound", "The requested tool metadata was not found.")
	// ErrToolMetadataAlreadyExists indicates tool metadata with same tool_name already exists.
	ErrToolMetadataAlreadyExists = errorsx.New(http.StatusConflict, "Conflict.ToolMetadataAlreadyExists", "Tool metadata with the same tool_name already exists.")
	// ErrToolMetadataCreateFailed indicates tool metadata create failure.
	ErrToolMetadataCreateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.ToolMetadataCreateFailed", "Failed to create tool metadata.")
	// ErrToolMetadataUpdateFailed indicates tool metadata update failure.
	ErrToolMetadataUpdateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.ToolMetadataUpdateFailed", "Failed to update tool metadata.")
	// ErrToolMetadataDeleteFailed indicates tool metadata delete failure.
	ErrToolMetadataDeleteFailed = errorsx.New(http.StatusInternalServerError, "InternalError.ToolMetadataDeleteFailed", "Failed to delete tool metadata.")
	// ErrToolMetadataGetFailed indicates tool metadata get failure.
	ErrToolMetadataGetFailed = errorsx.New(http.StatusInternalServerError, "InternalError.ToolMetadataGetFailed", "Failed to retrieve tool metadata.")
	// ErrToolMetadataListFailed indicates tool metadata list failure.
	ErrToolMetadataListFailed = errorsx.New(http.StatusInternalServerError, "InternalError.ToolMetadataListFailed", "Failed to list tool metadata.")
)