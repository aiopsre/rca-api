package errno

import (
	"net/http"

	"github.com/onexstack/onexstack/pkg/errorsx"
)

var (
	// ErrPlaybookNotFound indicates playbook does not exist.
	ErrPlaybookNotFound = errorsx.New(http.StatusNotFound, "NotFound.PlaybookNotFound", "The requested playbook was not found.")
	// ErrPlaybookAlreadyExists indicates playbook with same name already exists.
	ErrPlaybookAlreadyExists = errorsx.New(http.StatusConflict, "Conflict.PlaybookAlreadyExists", "A playbook with the same name already exists.")
	// ErrPlaybookCreateFailed indicates playbook create failure.
	ErrPlaybookCreateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.PlaybookCreateFailed", "Failed to create the playbook.")
	// ErrPlaybookUpdateFailed indicates playbook update failure.
	ErrPlaybookUpdateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.PlaybookUpdateFailed", "Failed to update the playbook.")
	// ErrPlaybookDeleteFailed indicates playbook delete failure.
	ErrPlaybookDeleteFailed = errorsx.New(http.StatusInternalServerError, "InternalError.PlaybookDeleteFailed", "Failed to delete the playbook.")
	// ErrPlaybookGetFailed indicates playbook get failure.
	ErrPlaybookGetFailed = errorsx.New(http.StatusInternalServerError, "InternalError.PlaybookGetFailed", "Failed to retrieve the playbook.")
	// ErrPlaybookListFailed indicates playbook list failure.
	ErrPlaybookListFailed = errorsx.New(http.StatusInternalServerError, "InternalError.PlaybookListFailed", "Failed to list playbooks.")
	// ErrPlaybookActivateFailed indicates playbook activate failure.
	ErrPlaybookActivateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.PlaybookActivateFailed", "Failed to activate the playbook.")
	// ErrPlaybookDeactivateFailed indicates playbook deactivate failure.
	ErrPlaybookDeactivateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.PlaybookDeactivateFailed", "Failed to deactivate the playbook.")
	// ErrPlaybookRollbackFailed indicates playbook rollback failure.
	ErrPlaybookRollbackFailed = errorsx.New(http.StatusInternalServerError, "InternalError.PlaybookRollbackFailed", "Failed to rollback the playbook.")
	// ErrPlaybookInvalidConfig indicates playbook config validation failure.
	ErrPlaybookInvalidConfig = errorsx.New(http.StatusBadRequest, "InvalidArgument.PlaybookInvalidConfig", "The playbook configuration is invalid.")
	// ErrPlaybookVersionMismatch indicates playbook version mismatch during update.
	ErrPlaybookVersionMismatch = errorsx.New(http.StatusConflict, "Conflict.PlaybookVersionMismatch", "The playbook version mismatch.")
)
