package errno

import (
	"net/http"

	"github.com/onexstack/onexstack/pkg/errorsx"
)

var (
	// ErrAlertEventNotFound indicates alert event does not exist.
	ErrAlertEventNotFound = errorsx.New(http.StatusNotFound, "NotFound.AlertEventNotFound", "The requested alert event was not found.")
	// ErrAlertEventIngestFailed indicates ingest processing failure.
	ErrAlertEventIngestFailed = errorsx.New(http.StatusInternalServerError, "InternalError.AlertEventIngestFailed", "Failed to ingest alert event.")
	// ErrAlertEventGetFailed indicates get failure.
	ErrAlertEventGetFailed = errorsx.New(http.StatusInternalServerError, "InternalError.AlertEventGetFailed", "Failed to retrieve alert event.")
	// ErrAlertEventListFailed indicates list failure.
	ErrAlertEventListFailed = errorsx.New(http.StatusInternalServerError, "InternalError.AlertEventListFailed", "Failed to list alert events.")
	// ErrAlertEventAckFailed indicates ack failure.
	ErrAlertEventAckFailed = errorsx.New(http.StatusInternalServerError, "InternalError.AlertEventAckFailed", "Failed to acknowledge alert event.")
	// ErrAlertEventIdempotencyConflict indicates idempotency-key conflict.
	ErrAlertEventIdempotencyConflict = errorsx.New(http.StatusConflict, "Conflict.AlertEventIdempotencyConflict", "Idempotency key conflicts with existing alert event.")
)
