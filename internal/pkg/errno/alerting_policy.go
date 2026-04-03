package errno

import (
	"net/http"

	"github.com/onexstack/onexstack/pkg/errorsx"
)

var (
	// ErrAlertingPolicyNotFound indicates alerting policy does not exist.
	ErrAlertingPolicyNotFound = errorsx.New(http.StatusNotFound, "NotFound.AlertingPolicyNotFound", "The requested alerting policy was not found.")
	// ErrAlertingPolicyAlreadyExists indicates alerting policy with same name already exists.
	ErrAlertingPolicyAlreadyExists = errorsx.New(http.StatusConflict, "Conflict.AlertingPolicyAlreadyExists", "An alerting policy with the same name already exists.")
	// ErrAlertingPolicyCreateFailed indicates alerting policy create failure.
	ErrAlertingPolicyCreateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.AlertingPolicyCreateFailed", "Failed to create the alerting policy.")
	// ErrAlertingPolicyUpdateFailed indicates alerting policy update failure.
	ErrAlertingPolicyUpdateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.AlertingPolicyUpdateFailed", "Failed to update the alerting policy.")
	// ErrAlertingPolicyDeleteFailed indicates alerting policy delete failure.
	ErrAlertingPolicyDeleteFailed = errorsx.New(http.StatusInternalServerError, "InternalError.AlertingPolicyDeleteFailed", "Failed to delete the alerting policy.")
	// ErrAlertingPolicyGetFailed indicates alerting policy get failure.
	ErrAlertingPolicyGetFailed = errorsx.New(http.StatusInternalServerError, "InternalError.AlertingPolicyGetFailed", "Failed to retrieve the alerting policy.")
	// ErrAlertingPolicyListFailed indicates alerting policy list failure.
	ErrAlertingPolicyListFailed = errorsx.New(http.StatusInternalServerError, "InternalError.AlertingPolicyListFailed", "Failed to list alerting policies.")
	// ErrAlertingPolicyActivateFailed indicates alerting policy activate failure.
	ErrAlertingPolicyActivateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.AlertingPolicyActivateFailed", "Failed to activate the alerting policy.")
	// ErrAlertingPolicyDeactivateFailed indicates alerting policy deactivate failure.
	ErrAlertingPolicyDeactivateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.AlertingPolicyDeactivateFailed", "Failed to deactivate the alerting policy.")
	// ErrAlertingPolicyRollbackFailed indicates alerting policy rollback failure.
	ErrAlertingPolicyRollbackFailed = errorsx.New(http.StatusInternalServerError, "InternalError.AlertingPolicyRollbackFailed", "Failed to rollback the alerting policy.")
	// ErrAlertingPolicyInvalidConfig indicates alerting policy config validation failure.
	ErrAlertingPolicyInvalidConfig = errorsx.New(http.StatusBadRequest, "InvalidArgument.AlertingPolicyInvalidConfig", "The alerting policy configuration is invalid.")
	// ErrAlertingPolicyVersionMismatch indicates alerting policy version mismatch during update.
	ErrAlertingPolicyVersionMismatch = errorsx.New(http.StatusConflict, "Conflict.AlertingPolicyVersionMismatch", "The alerting policy version mismatch.")
	// ErrAlertingPolicyCannotDeactivateActive indicates cannot deactivate the only active policy.
	ErrAlertingPolicyCannotDeactivateActive = errorsx.New(http.StatusBadRequest, "InvalidArgument.AlertingPolicyCannotDeactivateActive", "Cannot deactivate the only active alerting policy.")
)
