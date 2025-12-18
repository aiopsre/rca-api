package errno

import (
	"net/http"

	"github.com/onexstack/onexstack/pkg/errorsx"
)

var (
	// ErrEvidenceNotFound indicates evidence does not exist.
	ErrEvidenceNotFound = errorsx.New(http.StatusNotFound, "NotFound.EvidenceNotFound", "The requested evidence was not found.")
	// ErrEvidenceQueryFailed indicates generic evidence query failure.
	ErrEvidenceQueryFailed = errorsx.New(http.StatusBadGateway, "Dependency.EvidenceQueryFailed", "Evidence query to upstream datasource failed.")
	// ErrEvidenceQueryTimeout indicates upstream query timeout.
	ErrEvidenceQueryTimeout = errorsx.New(http.StatusGatewayTimeout, "Dependency.EvidenceQueryTimeout", "Evidence query to upstream datasource timed out.")
	// ErrEvidenceSaveFailed indicates evidence save failure.
	ErrEvidenceSaveFailed = errorsx.New(http.StatusInternalServerError, "InternalError.EvidenceSaveFailed", "Failed to save evidence.")
	// ErrEvidenceGetFailed indicates evidence get failure.
	ErrEvidenceGetFailed = errorsx.New(http.StatusInternalServerError, "InternalError.EvidenceGetFailed", "Failed to retrieve evidence.")
	// ErrEvidenceListFailed indicates evidence list failure.
	ErrEvidenceListFailed = errorsx.New(http.StatusInternalServerError, "InternalError.EvidenceListFailed", "Failed to list evidence.")
	// ErrEvidenceRateLimited indicates query throttling.
	ErrEvidenceRateLimited = errorsx.New(http.StatusTooManyRequests, "RateLimit.EvidenceQueryRateLimited", "Evidence query rate limit exceeded.")
	// ErrEvidenceIdempotencyConflict indicates idempotency conflict.
	ErrEvidenceIdempotencyConflict = errorsx.New(http.StatusConflict, "Conflict.EvidenceIdempotencyConflict", "Idempotency key conflicts with existing evidence.")
)
