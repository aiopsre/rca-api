//nolint:gocognit,gocyclo,nestif,nilerr,nilnil,protogetter,modernize,whitespace
package alert_event

//go:generate mockgen -destination mock_alert_event.go -package alert_event zk8s.com/rca-api/internal/apiserver/biz/v1/alert_event AlertEventBiz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/onexstack/onexstack/pkg/errorsx"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"zk8s.com/rca-api/internal/apiserver/model"
	"zk8s.com/rca-api/internal/apiserver/pkg/audit"
	"zk8s.com/rca-api/internal/apiserver/pkg/conversion"
	"zk8s.com/rca-api/internal/apiserver/pkg/metrics"
	"zk8s.com/rca-api/internal/apiserver/pkg/silenceutil"
	"zk8s.com/rca-api/internal/apiserver/store"
	"zk8s.com/rca-api/internal/pkg/contextx"
	"zk8s.com/rca-api/internal/pkg/errno"
	v1 "zk8s.com/rca-api/pkg/api/apiserver/v1"
	"zk8s.com/rca-api/pkg/store/where"
)

const (
	alertStatusFiring     = "firing"
	alertStatusResolved   = "resolved"
	alertStatusSuppressed = "suppressed"

	incidentStatusNew           = "new"
	incidentStatusInvestigating = "investigating"
	incidentStatusResolved      = "resolved"
	incidentStatusClosed        = "closed"

	defaultAlertSource   = "alertmanager"
	defaultAlertSeverity = "warning"
	defaultService       = "unknown"
	defaultCluster       = "default"
	defaultNamespace     = "default"
	defaultWorkload      = "unknown"

	defaultListLimit = int64(20)
	maxListLimit     = int64(200)
)

var volatileLabelKeys = map[string]struct{}{
	"pod":          {},
	"instance":     {},
	"endpoint":     {},
	"container_id": {},
	"request_id":   {},
	"trace_id":     {},
	"span_id":      {},
	"ip":           {},
	"node_ip":      {},
}

// AlertEventBiz defines alert-event use-cases.
type AlertEventBiz interface {
	Ingest(ctx context.Context, rq *v1.IngestAlertEventRequest) (*v1.IngestAlertEventResponse, error)
	ListCurrent(ctx context.Context, rq *v1.ListCurrentAlertEventsRequest) (*v1.ListCurrentAlertEventsResponse, error)
	ListHistory(ctx context.Context, rq *v1.ListHistoryAlertEventsRequest) (*v1.ListHistoryAlertEventsResponse, error)
	Ack(ctx context.Context, rq *v1.AckAlertEventRequest) (*v1.AckAlertEventResponse, error)

	AlertEventExpansion
}

type AlertEventExpansion interface{}

type alertEventBiz struct {
	store store.IStore
}

var _ AlertEventBiz = (*alertEventBiz)(nil)

func New(store store.IStore) *alertEventBiz {
	return &alertEventBiz{store: store}
}

type ingestInput struct {
	idempotencyKey  string
	fingerprint     string
	dedupKey        string
	source          string
	status          string
	severity        string
	alertName       string
	service         string
	cluster         string
	namespace       string
	workload        string
	startsAt        *time.Time
	endsAt          *time.Time
	lastSeenAt      time.Time
	labelsJSON      *string
	annotationsJSON *string
	generatorURL    *string
	rawEventJSON    *string
}

func (b *alertEventBiz) Ingest(ctx context.Context, rq *v1.IngestAlertEventRequest) (*v1.IngestAlertEventResponse, error) {
	startedAt := time.Now()
	outcome := "ok"
	mergeResult := ""

	defer func() {
		if metrics.M != nil {
			metrics.M.RecordAlertEventIngest(ctx, mergeResult, outcome, time.Since(startedAt))
		}
	}()

	in, err := normalizeIngestInput(rq)
	if err != nil {
		outcome = "invalid_argument"
		return nil, err
	}

	var (
		eventID               string
		incidentID            string
		reused                bool
		incidentCreated       bool
		incidentStatusChanged bool
		silenced              bool
		silenceID             string
	)

	err = b.store.TX(ctx, func(txCtx context.Context) error {
		if in.idempotencyKey != "" {
			existing, getErr := b.store.AlertEvent().Get(txCtx, where.T(txCtx).F("idempotency_key", in.idempotencyKey))
			if getErr == nil {
				if existing.Fingerprint != in.fingerprint {
					return errno.ErrAlertEventIdempotencyConflict
				}
				reused = true
				eventID = existing.EventID
				incidentID = derefString(existing.IncidentID)
				silenced = existing.IsSilenced
				silenceID = derefString(existing.SilenceID)
				mergeResult = "idempotent_reused"
				return nil
			}
			if getErr != nil && !errorsx.Is(getErr, gorm.ErrRecordNotFound) {
				return errno.ErrAlertEventGetFailed
			}
		}

		matchedSilence, matchErr := b.matchActiveSilence(txCtx, in)
		if matchErr != nil {
			return matchErr
		}
		if matchedSilence != nil {
			silenced = true
			silenceID = matchedSilence.SilenceID
		}

		if !silenced {
			incident, created, statusChanged, resolveErr := b.resolveIncidentForIngest(txCtx, in)
			if resolveErr != nil {
				return resolveErr
			}
			incidentCreated = created
			incidentStatusChanged = statusChanged
			if incident != nil {
				incidentID = incident.IncidentID
			}
		}

		history := buildAlertEventModel(in, incidentID, false, nil, strPtr(in.idempotencyKey), silenced, silenceID)
		if err := b.store.AlertEvent().Create(txCtx, history); err != nil {
			if in.idempotencyKey != "" && isDuplicateKeyError(err) {
				existing, getErr := b.store.AlertEvent().Get(txCtx, where.T(txCtx).F("idempotency_key", in.idempotencyKey))
				if getErr == nil {
					reused = true
					eventID = existing.EventID
					incidentID = derefString(existing.IncidentID)
					silenced = existing.IsSilenced
					silenceID = derefString(existing.SilenceID)
					mergeResult = "idempotent_reused"
					return nil
				}
				return errno.ErrAlertEventIdempotencyConflict
			}
			return errno.ErrAlertEventIngestFailed
		}
		eventID = history.EventID

		mergeResult, err = b.mergeCurrentAlert(txCtx, in, incidentID, silenced, silenceID)
		if err != nil {
			return err
		}
		if silenced && mergeResult != "idempotent_reused" {
			mergeResult = "silenced_" + mergeResult
		}

		if silenced {
			audit.AppendIncidentTimelineIfExists(txCtx, b.store.DB(txCtx), incidentID, "alert_silenced", eventID, map[string]any{
				"event_id":    eventID,
				"fingerprint": in.fingerprint,
				"silence_id":  silenceID,
			})
		}

		if !silenced && incidentID != "" {
			audit.AppendIncidentTimelineIfExists(txCtx, b.store.DB(txCtx), incidentID, "alert_ingested", eventID, map[string]any{
				"event_id":     eventID,
				"fingerprint":  in.fingerprint,
				"status":       in.status,
				"merge_result": mergeResult,
			})
			if incidentCreated {
				audit.AppendIncidentTimelineIfExists(txCtx, b.store.DB(txCtx), incidentID, "incident_created", incidentID, map[string]any{
					"event_id":    eventID,
					"fingerprint": in.fingerprint,
					"status":      incidentStatusNew,
				})
			}
			if incidentStatusChanged {
				audit.AppendIncidentTimelineIfExists(txCtx, b.store.DB(txCtx), incidentID, "incident_status_changed", incidentID, map[string]any{
					"event_id":    eventID,
					"from_status": incidentStatusInvestigating,
					"to_status":   incidentStatusResolved,
				})
			}
		}

		return nil
	})
	if err != nil {
		outcome = "failed"
		return nil, err
	}

	if reused {
		outcome = "reused"
	}

	slog.InfoContext(ctx, "alert event ingested",
		"request_id", contextx.RequestID(ctx),
		"incident_id", incidentID,
		"event_id", eventID,
		"job_id", "",
		"tool_call_id", "",
		"datasource_id", "",
		"fingerprint", in.fingerprint,
		"status", in.status,
		"reused", reused,
		"merge_result", mergeResult,
		"idempotency_key", in.idempotencyKey,
		"silenced", silenced,
		"silence_id", silenceID,
	)

	resp := &v1.IngestAlertEventResponse{
		EventID:     eventID,
		Fingerprint: in.fingerprint,
		Status:      in.status,
		Reused:      reused,
		MergeResult: mergeResult,
		Silenced:    silenced,
	}
	if incidentID != "" {
		resp.IncidentID = &incidentID
	}
	if silenceID != "" {
		resp.SilenceID = &silenceID
	}
	return resp, nil
}

func (b *alertEventBiz) ListCurrent(ctx context.Context, rq *v1.ListCurrentAlertEventsRequest) (*v1.ListCurrentAlertEventsResponse, error) {
	limit := normalizeListLimit(rq.GetLimit())
	whr := where.T(ctx).O(int(rq.GetOffset())).L(int(limit)).F("is_current", true)

	if v := trimOptional(rq.Severity); v != "" {
		whr = whr.F("severity", normalizeSeverity(v))
	}
	if v := trimOptional(rq.Service); v != "" {
		whr = whr.F("service", v)
	}
	if v := trimOptional(rq.Cluster); v != "" {
		whr = whr.F("cluster", v)
	}
	if v := trimOptional(rq.Namespace); v != "" {
		whr = whr.F("namespace", v)
	}
	if v := trimOptional(rq.Fingerprint); v != "" {
		whr = whr.F("fingerprint", v)
	}
	if v := trimOptional(rq.Status); v != "" {
		whr = whr.F("status", strings.ToLower(v))
	}
	if rq.LastSeenStart != nil {
		whr = whr.C(clause.Expr{SQL: "last_seen_at >= ?", Vars: []any{rq.GetLastSeenStart().AsTime().UTC()}})
	}
	if rq.LastSeenEnd != nil {
		whr = whr.C(clause.Expr{SQL: "last_seen_at <= ?", Vars: []any{rq.GetLastSeenEnd().AsTime().UTC()}})
	}

	total, list, err := b.store.AlertEvent().List(ctx, whr)
	if err != nil {
		return nil, errno.ErrAlertEventListFailed
	}

	out := make([]*v1.AlertEvent, 0, len(list))
	for _, item := range list {
		out = append(out, conversion.AlertEventMToAlertEventV1(item))
	}
	return &v1.ListCurrentAlertEventsResponse{TotalCount: total, Events: out}, nil
}

func (b *alertEventBiz) ListHistory(ctx context.Context, rq *v1.ListHistoryAlertEventsRequest) (*v1.ListHistoryAlertEventsResponse, error) {
	limit := normalizeListLimit(rq.GetLimit())
	whr := where.T(ctx).O(int(rq.GetOffset())).L(int(limit)).F("is_current", false)

	if v := trimOptional(rq.Severity); v != "" {
		whr = whr.F("severity", normalizeSeverity(v))
	}
	if v := trimOptional(rq.Service); v != "" {
		whr = whr.F("service", v)
	}
	if v := trimOptional(rq.Cluster); v != "" {
		whr = whr.F("cluster", v)
	}
	if v := trimOptional(rq.Namespace); v != "" {
		whr = whr.F("namespace", v)
	}
	if v := trimOptional(rq.Fingerprint); v != "" {
		whr = whr.F("fingerprint", v)
	}
	if v := trimOptional(rq.Status); v != "" {
		whr = whr.F("status", strings.ToLower(v))
	}
	if v := trimOptional(rq.IncidentID); v != "" {
		whr = whr.F("incident_id", v)
	}
	if rq.LastSeenStart != nil {
		whr = whr.C(clause.Expr{SQL: "last_seen_at >= ?", Vars: []any{rq.GetLastSeenStart().AsTime().UTC()}})
	}
	if rq.LastSeenEnd != nil {
		whr = whr.C(clause.Expr{SQL: "last_seen_at <= ?", Vars: []any{rq.GetLastSeenEnd().AsTime().UTC()}})
	}

	total, list, err := b.store.AlertEvent().List(ctx, whr)
	if err != nil {
		return nil, errno.ErrAlertEventListFailed
	}

	out := make([]*v1.AlertEvent, 0, len(list))
	for _, item := range list {
		out = append(out, conversion.AlertEventMToAlertEventV1(item))
	}
	return &v1.ListHistoryAlertEventsResponse{TotalCount: total, Events: out}, nil
}

func (b *alertEventBiz) Ack(ctx context.Context, rq *v1.AckAlertEventRequest) (*v1.AckAlertEventResponse, error) {
	eventID := strings.TrimSpace(rq.GetEventID())
	if eventID == "" {
		return nil, errorsx.ErrInvalidArgument
	}

	ackedAt := time.Now().UTC()
	ackedBy := normalizeAckedBy(ctx, rq.AckedBy)
	incidentID := ""

	err := b.store.TX(ctx, func(txCtx context.Context) error {
		event, err := b.store.AlertEvent().Get(txCtx, where.T(txCtx).F("event_id", eventID))
		if err != nil {
			return toAlertEventGetError(err)
		}

		if event.AckedAt != nil {
			ackedAt = event.AckedAt.UTC()
			ackedBy = derefString(event.AckedBy)
			incidentID = derefString(event.IncidentID)
			if ackedBy == "" {
				ackedBy = normalizeAckedBy(ctx, rq.AckedBy)
			}
			return nil
		}

		event.AckedAt = &ackedAt
		event.AckedBy = &ackedBy
		if err := b.store.AlertEvent().Update(txCtx, event); err != nil {
			return errno.ErrAlertEventAckFailed
		}

		incidentID = derefString(event.IncidentID)
		if incidentID != "" {
			audit.AppendIncidentTimelineIfExists(txCtx, b.store.DB(txCtx), incidentID, "alert_acked", eventID, map[string]any{
				"event_id": eventID,
				"acked_by": ackedBy,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	slog.InfoContext(ctx, "alert event acknowledged",
		"request_id", contextx.RequestID(ctx),
		"incident_id", incidentID,
		"event_id", eventID,
		"job_id", "",
		"tool_call_id", "",
		"datasource_id", "",
		"acked_by", ackedBy,
	)

	resp := &v1.AckAlertEventResponse{
		EventID: eventID,
		AckedAt: timestamppb.New(ackedAt),
		AckedBy: ackedBy,
	}
	if incidentID != "" {
		resp.IncidentID = &incidentID
	}
	return resp, nil
}

// Merge policy (deterministic, strategy A):
// one fingerprint can bind only one non-closed incident via incidents.active_fingerprint_key unique index.
// When incident is resolved/closed, active_fingerprint_key is cleared so the next firing ingest creates a new incident.
func (b *alertEventBiz) resolveIncidentForIngest(ctx context.Context, in *ingestInput) (*model.IncidentM, bool, bool, error) {
	incident, err := b.store.Incident().Get(ctx, where.T(ctx).F("active_fingerprint_key", in.fingerprint))
	if err == nil {
		if isIncidentClosed(incident.Status) {
			incident.ActiveFingerprintKey = nil
			if updateErr := b.store.Incident().Update(ctx, incident); updateErr != nil {
				return nil, false, false, errno.ErrIncidentUpdateFailed
			}
		} else {
			statusChanged := false
			incident.Severity = in.severity
			if in.startsAt != nil && (incident.StartAt == nil || in.startsAt.Before(*incident.StartAt)) {
				incident.StartAt = cloneTimePtr(in.startsAt)
			}
			if in.status == alertStatusResolved {
				incident.Status = incidentStatusResolved
				incident.ActiveFingerprintKey = nil
				if in.endsAt != nil {
					incident.EndAt = cloneTimePtr(in.endsAt)
				} else {
					resolvedAt := in.lastSeenAt
					incident.EndAt = &resolvedAt
				}
				statusChanged = true
			}
			if updateErr := b.store.Incident().Update(ctx, incident); updateErr != nil {
				return nil, false, false, errno.ErrIncidentUpdateFailed
			}
			return incident, false, statusChanged, nil
		}
	}
	if err != nil && !errorsx.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, false, errno.ErrIncidentGetFailed
	}

	if in.status == alertStatusResolved {
		return nil, false, false, nil
	}

	incident = &model.IncidentM{
		TenantID:             "default",
		Cluster:              in.cluster,
		Namespace:            in.namespace,
		WorkloadKind:         "Deployment",
		WorkloadName:         in.workload,
		Service:              in.service,
		Environment:          "prod",
		Source:               in.source,
		Severity:             in.severity,
		Status:               incidentStatusNew,
		StartAt:              cloneTimePtr(in.startsAt),
		RCAStatus:            "pending",
		ActionStatus:         "none",
		Fingerprint:          strPtr(in.fingerprint),
		ActiveFingerprintKey: strPtr(in.fingerprint),
		LabelsJSON:           cloneStringPtr(in.labelsJSON),
		AnnotationsJSON:      cloneStringPtr(in.annotationsJSON),
	}
	if in.alertName != "" {
		incident.AlertName = strPtr(in.alertName)
	}

	if createErr := b.store.Incident().Create(ctx, incident); createErr != nil {
		if isDuplicateKeyError(createErr) {
			existing, getErr := b.retryGetIncidentAfterDuplicateCreate(ctx, in.fingerprint)
			if getErr == nil {
				return existing, false, false, nil
			}
			slog.WarnContext(ctx, "incident create duplicate fallback read failed",
				"request_id", contextx.RequestID(ctx),
				"incident_id", "",
				"event_id", "",
				"job_id", "",
				"tool_call_id", "",
				"datasource_id", "",
				"fingerprint", in.fingerprint,
				"error", getErr,
			)
		}
		return nil, false, false, errno.ErrIncidentCreateFailed
	}

	return incident, true, false, nil
}

func (b *alertEventBiz) mergeCurrentAlert(ctx context.Context, in *ingestInput, incidentID string, silenced bool, silenceID string) (string, error) {
	current, err := b.store.AlertEvent().Get(ctx, where.T(ctx).F("current_key", in.fingerprint))
	if err != nil && !errorsx.Is(err, gorm.ErrRecordNotFound) {
		return "", errno.ErrAlertEventGetFailed
	}

	if in.status == alertStatusResolved {
		if err != nil {
			return "history_appended", nil
		}
		applyIngestOnCurrent(current, in, incidentID, silenced, silenceID)
		current.IsCurrent = false
		current.CurrentKey = nil
		if in.endsAt == nil {
			resolvedAt := in.lastSeenAt
			current.EndsAt = &resolvedAt
		}
		if updateErr := b.store.AlertEvent().Update(ctx, current); updateErr != nil {
			return "", errno.ErrAlertEventIngestFailed
		}
		return "current_resolved", nil
	}

	if err != nil {
		curKey := in.fingerprint
		obj := buildAlertEventModel(in, incidentID, true, &curKey, nil, silenced, silenceID)
		if createErr := b.store.AlertEvent().Create(ctx, obj); createErr != nil {
			if isDuplicateKeyError(createErr) {
				existing, getErr := b.store.AlertEvent().Get(ctx, where.T(ctx).F("current_key", in.fingerprint))
				if getErr != nil {
					return "", errno.ErrAlertEventIngestFailed
				}
				applyIngestOnCurrent(existing, in, incidentID, silenced, silenceID)
				if updateErr := b.store.AlertEvent().Update(ctx, existing); updateErr != nil {
					return "", errno.ErrAlertEventIngestFailed
				}
				return "current_updated", nil
			}
			return "", errno.ErrAlertEventIngestFailed
		}
		return "current_created", nil
	}

	applyIngestOnCurrent(current, in, incidentID, silenced, silenceID)
	if updateErr := b.store.AlertEvent().Update(ctx, current); updateErr != nil {
		return "", errno.ErrAlertEventIngestFailed
	}
	return "current_updated", nil
}

func normalizeIngestInput(rq *v1.IngestAlertEventRequest) (*ingestInput, error) {
	if rq == nil {
		return nil, errorsx.ErrInvalidArgument
	}

	status := strings.ToLower(strings.TrimSpace(rq.GetStatus()))
	switch status {
	case alertStatusFiring, alertStatusResolved, alertStatusSuppressed:
	default:
		return nil, errorsx.ErrInvalidArgument
	}

	now := time.Now().UTC()
	lastSeenAt := now
	if rq.LastSeenAt != nil {
		lastSeenAt = rq.GetLastSeenAt().AsTime().UTC()
	}

	var startsAt *time.Time
	if rq.StartsAt != nil {
		t := rq.GetStartsAt().AsTime().UTC()
		startsAt = &t
	}
	var endsAt *time.Time
	if rq.EndsAt != nil {
		t := rq.GetEndsAt().AsTime().UTC()
		endsAt = &t
	}
	if status == alertStatusResolved && endsAt == nil {
		t := lastSeenAt
		endsAt = &t
	}
	if startsAt != nil && endsAt != nil && startsAt.After(*endsAt) {
		return nil, errorsx.ErrInvalidArgument
	}

	labelsJSON := trimOptionalPtr(rq.LabelsJSON)
	annotationsJSON := trimOptionalPtr(rq.AnnotationsJSON)
	generatorURL := trimOptionalPtr(rq.GeneratorURL)
	rawEventJSON := trimOptionalPtr(rq.RawEventJSON)
	labels := parseLabelsMap(labelsJSON)

	alertName := firstNonEmpty(trimOptional(rq.AlertName), labels["alertname"])
	service := firstNonEmpty(trimOptional(rq.Service), labels["service"], labels["app"], defaultService)
	cluster := firstNonEmpty(trimOptional(rq.Cluster), labels["cluster"], labels["kubernetes_cluster"], defaultCluster)
	namespace := firstNonEmpty(trimOptional(rq.Namespace), labels["namespace"], labels["kubernetes_namespace"], defaultNamespace)
	workload := firstNonEmpty(trimOptional(rq.Workload), labels["workload"], labels["deployment"], labels["statefulset"], defaultWorkload)

	fingerprint := strings.TrimSpace(rq.GetFingerprint())
	if fingerprint == "" {
		fingerprint = deriveFingerprint(labels, service, cluster, namespace, workload, alertName)
	}
	if fingerprint == "" {
		return nil, errorsx.ErrInvalidArgument
	}

	dedupKey := strings.TrimSpace(rq.GetDedupKey())
	if dedupKey == "" {
		dedupKey = fingerprint
	}

	source := strings.ToLower(strings.TrimSpace(rq.GetSource()))
	if source == "" {
		source = defaultAlertSource
	}

	return &ingestInput{
		idempotencyKey:  trimOptional(rq.IdempotencyKey),
		fingerprint:     fingerprint,
		dedupKey:        dedupKey,
		source:          source,
		status:          status,
		severity:        normalizeSeverity(strings.TrimSpace(rq.GetSeverity())),
		alertName:       alertName,
		service:         service,
		cluster:         cluster,
		namespace:       namespace,
		workload:        workload,
		startsAt:        startsAt,
		endsAt:          endsAt,
		lastSeenAt:      lastSeenAt,
		labelsJSON:      labelsJSON,
		annotationsJSON: annotationsJSON,
		generatorURL:    generatorURL,
		rawEventJSON:    rawEventJSON,
	}, nil
}

func buildAlertEventModel(
	in *ingestInput,
	incidentID string,
	isCurrent bool,
	currentKey *string,
	idempotencyKey *string,
	silenced bool,
	silenceID string,
) *model.AlertEventM {
	obj := &model.AlertEventM{
		IncidentID:      nil,
		Fingerprint:     in.fingerprint,
		DedupKey:        in.dedupKey,
		Source:          in.source,
		Status:          in.status,
		Severity:        in.severity,
		AlertName:       strPtr(in.alertName),
		Service:         strPtr(in.service),
		Cluster:         strPtr(in.cluster),
		Namespace:       strPtr(in.namespace),
		Workload:        strPtr(in.workload),
		StartsAt:        cloneTimePtr(in.startsAt),
		EndsAt:          cloneTimePtr(in.endsAt),
		LastSeenAt:      in.lastSeenAt,
		LabelsJSON:      cloneStringPtr(in.labelsJSON),
		AnnotationsJSON: cloneStringPtr(in.annotationsJSON),
		GeneratorURL:    cloneStringPtr(in.generatorURL),
		RawEventJSON:    cloneStringPtr(in.rawEventJSON),
		IsCurrent:       isCurrent,
		CurrentKey:      cloneStringPtr(currentKey),
		IdempotencyKey:  cloneStringPtr(idempotencyKey),
		IsSilenced:      silenced,
		SilenceID:       strPtr(silenceID),
	}
	if incidentID != "" {
		obj.IncidentID = strPtr(incidentID)
	}
	return obj
}

func applyIngestOnCurrent(current *model.AlertEventM, in *ingestInput, incidentID string, silenced bool, silenceID string) {
	current.Fingerprint = in.fingerprint
	current.DedupKey = in.dedupKey
	current.Source = in.source
	current.Status = in.status
	current.Severity = in.severity
	current.AlertName = strPtr(in.alertName)
	current.Service = strPtr(in.service)
	current.Cluster = strPtr(in.cluster)
	current.Namespace = strPtr(in.namespace)
	current.Workload = strPtr(in.workload)
	if in.startsAt != nil {
		if current.StartsAt == nil || in.startsAt.Before(*current.StartsAt) {
			current.StartsAt = cloneTimePtr(in.startsAt)
		}
	}
	if in.endsAt != nil {
		current.EndsAt = cloneTimePtr(in.endsAt)
	} else if in.status != alertStatusResolved {
		current.EndsAt = nil
	}
	if in.lastSeenAt.After(current.LastSeenAt) {
		current.LastSeenAt = in.lastSeenAt
	}
	current.LabelsJSON = cloneStringPtr(in.labelsJSON)
	current.AnnotationsJSON = cloneStringPtr(in.annotationsJSON)
	current.GeneratorURL = cloneStringPtr(in.generatorURL)
	current.RawEventJSON = cloneStringPtr(in.rawEventJSON)
	if in.status != alertStatusResolved {
		current.IsCurrent = true
		current.CurrentKey = strPtr(in.fingerprint)
	}
	current.IdempotencyKey = nil
	current.IsSilenced = silenced
	current.SilenceID = strPtr(silenceID)
	if incidentID != "" {
		current.IncidentID = strPtr(incidentID)
	} else if silenced {
		// Keep silenced current row detached from incident progression.
		current.IncidentID = nil
	}
}

func normalizeSeverity(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	switch v {
	case "critical", "p0", "high":
		return "critical"
	case "warning", "warn", "p1", "medium":
		return "warning"
	case "info", "p2", "low":
		return "info"
	case "":
		return defaultAlertSeverity
	default:
		return v
	}
}

func deriveFingerprint(labels map[string]string, service string, cluster string, namespace string, workload string, alertName string) string {
	stable := make(map[string]string, len(labels)+5)
	for k, v := range labels {
		key := strings.ToLower(strings.TrimSpace(k))
		val := strings.TrimSpace(v)
		if key == "" || val == "" {
			continue
		}
		if _, ok := volatileLabelKeys[key]; ok {
			continue
		}
		stable[key] = val
	}
	if _, ok := stable["service"]; !ok && service != "" {
		stable["service"] = service
	}
	if _, ok := stable["cluster"]; !ok && cluster != "" {
		stable["cluster"] = cluster
	}
	if _, ok := stable["namespace"]; !ok && namespace != "" {
		stable["namespace"] = namespace
	}
	if _, ok := stable["workload"]; !ok && workload != "" {
		stable["workload"] = workload
	}
	if _, ok := stable["alertname"]; !ok && alertName != "" {
		stable["alertname"] = alertName
	}
	if len(stable) == 0 {
		return ""
	}

	keys := make([]string, 0, len(stable))
	for k := range stable {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	b := strings.Builder{}
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(stable[k])
		b.WriteByte('\n')
	}

	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:16])
}

func parseLabelsMap(raw *string) map[string]string {
	out := map[string]string{}
	if raw == nil {
		return out
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(*raw), &decoded); err != nil {
		return out
	}
	for k, v := range decoded {
		key := strings.ToLower(strings.TrimSpace(k))
		if key == "" || v == nil {
			continue
		}
		if s, ok := v.(string); ok {
			out[key] = strings.TrimSpace(s)
			continue
		}
		encoded, err := json.Marshal(v)
		if err != nil {
			continue
		}
		out[key] = strings.TrimSpace(string(encoded))
	}
	return out
}

func toAlertEventGetError(err error) error {
	if errorsx.Is(err, gorm.ErrRecordNotFound) {
		return errno.ErrAlertEventNotFound
	}
	return errno.ErrAlertEventGetFailed
}

func normalizeAckedBy(ctx context.Context, ackedBy *string) string {
	if v := trimOptional(ackedBy); v != "" {
		return v
	}
	if u := strings.TrimSpace(contextx.Username(ctx)); u != "" {
		return "user:" + u
	}
	if uid := strings.TrimSpace(contextx.UserID(ctx)); uid != "" {
		return "user:" + uid
	}
	return "system"
}

func normalizeListLimit(limit int64) int64 {
	if limit <= 0 {
		return defaultListLimit
	}
	if limit > maxListLimit {
		return maxListLimit
	}
	return limit
}

func (b *alertEventBiz) matchActiveSilence(ctx context.Context, in *ingestInput) (*model.SilenceM, error) {
	active, err := b.store.Silence().ListActive(ctx, in.namespace, time.Now().UTC())
	if err != nil {
		if isTableNotFoundError(err) {
			return nil, nil
		}
		return nil, errno.ErrSilenceListFailed
	}
	if len(active) == 0 {
		return nil, nil
	}

	attrs := map[string]string{
		"fingerprint":  in.fingerprint,
		"service":      in.service,
		"workloadkind": silenceutil.DefaultWorkloadKind,
		"workloadname": in.workload,
		"severity":     in.severity,
	}
	for _, candidate := range active {
		matchers, decodeErr := silenceutil.DecodeMatchers(candidate.MatchersJSON)
		if decodeErr != nil || len(matchers) == 0 {
			continue
		}
		if silenceutil.MatchesAll(matchers, attrs) {
			return candidate, nil
		}
	}
	return nil, nil
}

func (b *alertEventBiz) retryGetIncidentAfterDuplicateCreate(ctx context.Context, fingerprint string) (*model.IncidentM, error) {
	const (
		maxAttempts = 6
		backoff     = 20 * time.Millisecond
	)
	//nolint:contextcheck // Detach from TX-scoped context to avoid stale readback after duplicate-key races.
	readbackCtx := context.Background()

	return retryIncidentDuplicateReadback(ctx, maxAttempts, backoff, func() (*model.IncidentM, error) {
		return b.store.Incident().Get(readbackCtx, where.T(ctx).F("active_fingerprint_key", fingerprint))
	})
}

func retryIncidentDuplicateReadback(ctx context.Context, attempts int, backoff time.Duration, readFn func() (*model.IncidentM, error)) (*model.IncidentM, error) {
	attempts, backoff = normalizeDuplicateReadbackPolicy(attempts, backoff)

	for i := range attempts {
		incident, err := readFn()
		if err == nil {
			return incident, nil
		}
		if !errorsx.Is(err, gorm.ErrRecordNotFound) || i == attempts-1 {
			return nil, err
		}
		if waitErr := waitDuplicateReadback(ctx, backoff); waitErr != nil {
			return nil, waitErr
		}
	}

	return nil, gorm.ErrRecordNotFound
}

func normalizeDuplicateReadbackPolicy(attempts int, backoff time.Duration) (int, time.Duration) {
	if attempts <= 0 {
		attempts = 1
	}
	if backoff < 0 {
		backoff = 0
	}

	return attempts, backoff
}

func waitDuplicateReadback(ctx context.Context, backoff time.Duration) error {
	if backoff == 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(backoff):
		return nil
	}
}

func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "duplicate") || strings.Contains(lower, "unique constraint")
}

func isTableNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "no such table") || strings.Contains(lower, "doesn't exist")
}

func isIncidentClosed(status string) bool {
	s := strings.ToLower(strings.TrimSpace(status))
	return s == incidentStatusResolved || s == incidentStatusClosed
}

func trimOptional(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

func trimOptionalPtr(v *string) *string {
	if v == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*v)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func cloneStringPtr(v *string) *string {
	if v == nil {
		return nil
	}
	value := *v
	return &value
}

func cloneTimePtr(v *time.Time) *time.Time {
	if v == nil {
		return nil
	}
	value := v.UTC()
	return &value
}

func strPtr(v string) *string {
	trimmed := strings.TrimSpace(v)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func derefString(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}
