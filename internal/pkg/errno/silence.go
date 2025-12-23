package errno

import (
	"net/http"

	"github.com/onexstack/onexstack/pkg/errorsx"
)

var (
	// ErrSilenceNotFound indicates silence does not exist.
	ErrSilenceNotFound = errorsx.New(http.StatusNotFound, "NotFound.SilenceNotFound", "The requested silence was not found.")
	// ErrSilenceCreateFailed indicates silence create failure.
	ErrSilenceCreateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.SilenceCreateFailed", "Failed to create the silence.")
	// ErrSilenceUpdateFailed indicates silence update failure.
	ErrSilenceUpdateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.SilenceUpdateFailed", "Failed to update the silence.")
	// ErrSilenceDeleteFailed indicates silence soft-delete(disable) failure.
	ErrSilenceDeleteFailed = errorsx.New(http.StatusInternalServerError, "InternalError.SilenceDeleteFailed", "Failed to disable the silence.")
	// ErrSilenceGetFailed indicates silence get failure.
	ErrSilenceGetFailed = errorsx.New(http.StatusInternalServerError, "InternalError.SilenceGetFailed", "Failed to retrieve the silence.")
	// ErrSilenceListFailed indicates silence list failure.
	ErrSilenceListFailed = errorsx.New(http.StatusInternalServerError, "InternalError.SilenceListFailed", "Failed to list silences.")
)
