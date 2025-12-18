package errno

import (
	"net/http"

	"github.com/onexstack/onexstack/pkg/errorsx"
)

var (
	// ErrAIJobNotFound indicates ai job does not exist.
	ErrAIJobNotFound = errorsx.New(http.StatusNotFound, "NotFound.AIJobNotFound", "The requested AI job was not found.")
	// ErrAIJobCreateFailed indicates ai job creation failure.
	ErrAIJobCreateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.AIJobCreateFailed", "Failed to create AI job.")
	// ErrAIJobGetFailed indicates ai job retrieval failure.
	ErrAIJobGetFailed = errorsx.New(http.StatusInternalServerError, "InternalError.AIJobGetFailed", "Failed to retrieve AI job.")
	// ErrAIJobListFailed indicates ai job list failure.
	ErrAIJobListFailed = errorsx.New(http.StatusInternalServerError, "InternalError.AIJobListFailed", "Failed to list AI jobs.")
	// ErrAIJobStartFailed indicates start transition failure.
	ErrAIJobStartFailed = errorsx.New(http.StatusInternalServerError, "InternalError.AIJobStartFailed", "Failed to start AI job.")
	// ErrAIJobFinalizeFailed indicates finalize transition failure.
	ErrAIJobFinalizeFailed = errorsx.New(http.StatusInternalServerError, "InternalError.AIJobFinalizeFailed", "Failed to finalize AI job.")
	// ErrAIJobCancelFailed indicates cancel transition failure.
	ErrAIJobCancelFailed = errorsx.New(http.StatusInternalServerError, "InternalError.AIJobCancelFailed", "Failed to cancel AI job.")
	// ErrAIJobInvalidTransition indicates status-machine transition conflict.
	ErrAIJobInvalidTransition = errorsx.New(http.StatusConflict, "Conflict.AIJobInvalidTransition", "AI job status transition is not allowed.")
	// ErrAIJobAlreadyRunning indicates another active job exists for this incident.
	ErrAIJobAlreadyRunning = errorsx.New(http.StatusConflict, "Conflict.AIJobAlreadyRunning", "Another queued/running AI job already exists for this incident.")
	// ErrAIJobIdempotencyConflict indicates idempotency key conflicts with existing job.
	ErrAIJobIdempotencyConflict = errorsx.New(http.StatusConflict, "Conflict.AIJobIdempotencyConflict", "Idempotency key conflicts with existing AI job.")
	// ErrAIJobInvalidDiagnosis indicates diagnosis_json does not satisfy schema constraints.
	ErrAIJobInvalidDiagnosis = errorsx.New(http.StatusBadRequest, "BadRequest.AIJobInvalidDiagnosis", "diagnosis_json does not satisfy required schema constraints.")
	// ErrAIToolCallCreateFailed indicates tool call audit write failure.
	ErrAIToolCallCreateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.AIToolCallCreateFailed", "Failed to create AI tool call audit.")
	// ErrAIToolCallListFailed indicates tool call list failure.
	ErrAIToolCallListFailed = errorsx.New(http.StatusInternalServerError, "InternalError.AIToolCallListFailed", "Failed to list AI tool calls.")
	// ErrAIToolCallInvalidStatus indicates invalid tool call status.
	ErrAIToolCallInvalidStatus = errorsx.New(http.StatusBadRequest, "BadRequest.AIToolCallInvalidStatus", "Invalid AI tool call status.")
	// ErrAIToolCallStatusConflict indicates tool call write is blocked by job status.
	ErrAIToolCallStatusConflict = errorsx.New(http.StatusConflict, "Conflict.AIToolCallStatusConflict", "AI tool call can only be written for queued/running jobs.")
)
