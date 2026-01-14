package errno

import (
	"net/http"

	"github.com/onexstack/onexstack/pkg/errorsx"
)

// ErrIncidentNotFound indicates that the specified incident was not found.
var ErrIncidentNotFound = errorsx.New(http.StatusNotFound, "NotFound.IncidentNotFound", "The requested incident was not found.")

// ErrIncidentCreateFailed indicates that the incident creation operation failed.
var ErrIncidentCreateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.IncidentCreateFailed", "Failed to create the incident.")

// ErrIncidentUpdateFailed indicates that the incident update operation failed.
var ErrIncidentUpdateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.IncidentUpdateFailed", "Failed to update the incident.")

// ErrIncidentDeleteFailed indicates that the incident deletion operation failed.
var ErrIncidentDeleteFailed = errorsx.New(http.StatusInternalServerError, "InternalError.IncidentDeleteFailed", "Failed to delete the incident.")

// ErrIncidentGetFailed indicates that retrieving the specified incident failed.
var ErrIncidentGetFailed = errorsx.New(http.StatusInternalServerError, "InternalError.IncidentGetFailed", "Failed to retrieve the incident details.")

// ErrIncidentListFailed indicates that listing incidents failed.
var ErrIncidentListFailed = errorsx.New(http.StatusInternalServerError, "InternalError.IncidentListFailed", "Failed to list incidents.")

// ErrIncidentActionCreateFailed indicates incident action-log create failure.
var ErrIncidentActionCreateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.IncidentActionCreateFailed", "Failed to create incident action log.")

// ErrIncidentActionListFailed indicates incident action-log list failure.
var ErrIncidentActionListFailed = errorsx.New(http.StatusInternalServerError, "InternalError.IncidentActionListFailed", "Failed to list incident action logs.")

// ErrIncidentVerificationRunCreateFailed indicates incident verification-run create failure.
var ErrIncidentVerificationRunCreateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.IncidentVerificationRunCreateFailed", "Failed to create incident verification run.")

// ErrIncidentVerificationRunListFailed indicates incident verification-run list failure.
var ErrIncidentVerificationRunListFailed = errorsx.New(http.StatusInternalServerError, "InternalError.IncidentVerificationRunListFailed", "Failed to list incident verification runs.")
