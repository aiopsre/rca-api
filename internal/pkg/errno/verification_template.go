package errno

import (
	"net/http"

	"github.com/onexstack/onexstack/pkg/errorsx"
)

// Verification Template error codes.
var (
	// ErrVerificationTemplateNotFound indicates verification template does not exist.
	ErrVerificationTemplateNotFound = errorsx.New(http.StatusNotFound, "NotFound.VerificationTemplateNotFound", "The requested verification template was not found.")
	// ErrVerificationTemplateAlreadyExists indicates verification template with same name already exists.
	ErrVerificationTemplateAlreadyExists = errorsx.New(http.StatusConflict, "Conflict.VerificationTemplateAlreadyExists", "A verification template with the same name already exists.")
	// ErrVerificationTemplateCreateFailed indicates verification template create failure.
	ErrVerificationTemplateCreateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.VerificationTemplateCreateFailed", "Failed to create the verification template.")
	// ErrVerificationTemplateUpdateFailed indicates verification template update failure.
	ErrVerificationTemplateUpdateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.VerificationTemplateUpdateFailed", "Failed to update the verification template.")
	// ErrVerificationTemplateDeleteFailed indicates verification template delete failure.
	ErrVerificationTemplateDeleteFailed = errorsx.New(http.StatusInternalServerError, "InternalError.VerificationTemplateDeleteFailed", "Failed to delete the verification template.")
	// ErrVerificationTemplateGetFailed indicates verification template get failure.
	ErrVerificationTemplateGetFailed = errorsx.New(http.StatusInternalServerError, "InternalError.VerificationTemplateGetFailed", "Failed to retrieve the verification template.")
	// ErrVerificationTemplateListFailed indicates verification template list failure.
	ErrVerificationTemplateListFailed = errorsx.New(http.StatusInternalServerError, "InternalError.VerificationTemplateListFailed", "Failed to list verification templates.")
	// ErrVerificationTemplateActivateFailed indicates verification template activate failure.
	ErrVerificationTemplateActivateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.VerificationTemplateActivateFailed", "Failed to activate the verification template.")
	// ErrVerificationTemplateDeactivateFailed indicates verification template deactivate failure.
	ErrVerificationTemplateDeactivateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.VerificationTemplateDeactivateFailed", "Failed to deactivate the verification template.")
	// ErrVerificationTemplateInvalidMatch indicates verification template match JSON validation failure.
	ErrVerificationTemplateInvalidMatch = errorsx.New(http.StatusBadRequest, "InvalidArgument.VerificationTemplateInvalidMatch", "The verification template match JSON is invalid.")
	// ErrVerificationTemplateInvalidSteps indicates verification template steps JSON validation failure.
	ErrVerificationTemplateInvalidSteps = errorsx.New(http.StatusBadRequest, "InvalidArgument.VerificationTemplateInvalidSteps", "The verification template steps JSON is invalid.")
	// ErrVerificationTemplateVersionMismatch indicates verification template version mismatch during update.
	ErrVerificationTemplateVersionMismatch = errorsx.New(http.StatusConflict, "Conflict.VerificationTemplateVersionMismatch", "The verification template version mismatch.")
)