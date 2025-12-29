package notice

const (
	// EventTypeIncidentCreated is emitted after incident creation commit.
	EventTypeIncidentCreated = "incident_created"
	// EventTypeDiagnosisWritten is emitted after finalize writeback commit.
	EventTypeDiagnosisWritten = "diagnosis_written"

	NoticePayloadModeCompact = "COMPACT"
	NoticePayloadModeFull    = "FULL"

	DeliveryStatusPending   = "pending"
	DeliveryStatusSucceeded = "succeeded"
	DeliveryStatusFailed    = "failed"
	DeliveryStatusCanceled  = "canceled"

	RequestBodyMaxBytes  = 16 * 1024
	ResponseBodyMaxBytes = 8 * 1024
	ErrorBodyMaxBytes    = 2 * 1024
	SnapshotMaxBytes     = 4 * 1024
	SnapshotHeaderMax    = 50
	SnapshotHeaderKeyMax = 256
	SnapshotHeaderValMax = 4096

	NoticePayloadMaxBytes            = 16 * 1024
	NoticePayloadStringMax           = 512
	NoticePayloadMissingEvidenceMax  = 20
	NoticePayloadEvidenceIDsMax      = 50

	timeoutMsMin     = int64(500)
	timeoutMsMax     = int64(10000)
	defaultTimeoutMs = int64(3000)
	maxHTTPReadBytes = int64(64 * 1024)

	defaultDeliveryMaxAttempts = int64(3)
	maxDeliveryMaxAttempts     = int64(20)
)
