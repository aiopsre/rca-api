package errno

import (
	"net/http"

	"github.com/onexstack/onexstack/pkg/errorsx"
)

var (
	// ErrMcpServerNotFound indicates MCP server does not exist.
	ErrMcpServerNotFound = errorsx.New(http.StatusNotFound, "NotFound.McpServerNotFound", "The requested MCP server was not found.")
	// ErrMcpServerAlreadyExists indicates MCP server with same name already exists.
	ErrMcpServerAlreadyExists = errorsx.New(http.StatusConflict, "Conflict.McpServerAlreadyExists", "An MCP server with the same name already exists.")
	// ErrMcpServerCreateFailed indicates MCP server create failure.
	ErrMcpServerCreateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.McpServerCreateFailed", "Failed to create the MCP server.")
	// ErrMcpServerUpdateFailed indicates MCP server update failure.
	ErrMcpServerUpdateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.McpServerUpdateFailed", "Failed to update the MCP server.")
	// ErrMcpServerDeleteFailed indicates MCP server delete failure.
	ErrMcpServerDeleteFailed = errorsx.New(http.StatusInternalServerError, "InternalError.McpServerDeleteFailed", "Failed to delete the MCP server.")
	// ErrMcpServerGetFailed indicates MCP server get failure.
	ErrMcpServerGetFailed = errorsx.New(http.StatusInternalServerError, "InternalError.McpServerGetFailed", "Failed to retrieve the MCP server.")
	// ErrMcpServerListFailed indicates MCP server list failure.
	ErrMcpServerListFailed = errorsx.New(http.StatusInternalServerError, "InternalError.McpServerListFailed", "Failed to list MCP servers.")
)
