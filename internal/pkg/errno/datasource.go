package errno

import (
	"net/http"

	"github.com/onexstack/onexstack/pkg/errorsx"
)

var (
	// ErrDatasourceNotFound indicates datasource does not exist.
	ErrDatasourceNotFound = errorsx.New(http.StatusNotFound, "NotFound.DatasourceNotFound", "The requested datasource was not found.")
	// ErrDatasourceDisabled indicates datasource is disabled.
	ErrDatasourceDisabled = errorsx.New(http.StatusConflict, "Conflict.DatasourceDisabled", "The datasource is disabled.")
	// ErrDatasourceUnsupportedType indicates datasource type mismatch.
	ErrDatasourceUnsupportedType = errorsx.New(http.StatusBadRequest, "InvalidArgument.DatasourceType", "The datasource type is not supported for this operation.")
	// ErrDatasourceCreateFailed indicates datasource create failure.
	ErrDatasourceCreateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.DatasourceCreateFailed", "Failed to create datasource.")
	// ErrDatasourceUpdateFailed indicates datasource update failure.
	ErrDatasourceUpdateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.DatasourceUpdateFailed", "Failed to update datasource.")
	// ErrDatasourceDeleteFailed indicates datasource delete failure.
	ErrDatasourceDeleteFailed = errorsx.New(http.StatusInternalServerError, "InternalError.DatasourceDeleteFailed", "Failed to delete datasource.")
	// ErrDatasourceGetFailed indicates datasource get failure.
	ErrDatasourceGetFailed = errorsx.New(http.StatusInternalServerError, "InternalError.DatasourceGetFailed", "Failed to retrieve datasource.")
	// ErrDatasourceListFailed indicates datasource list failure.
	ErrDatasourceListFailed = errorsx.New(http.StatusInternalServerError, "InternalError.DatasourceListFailed", "Failed to list datasources.")
)
