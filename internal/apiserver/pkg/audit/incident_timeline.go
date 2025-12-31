package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/pkg/contextx"
)

// AppendIncidentTimelineIfExists writes one incident timeline event only when table/columns are present.
// It never blocks main workflow: schema mismatch/write errors are logged and ignored.
//
//nolint:gocognit,gocyclo // Best-effort schema-compatible insert intentionally handles many branches defensively.
func AppendIncidentTimelineIfExists(ctx context.Context, db *gorm.DB, incidentID string, eventType string, refID string, payload map[string]any) {
	if db == nil {
		return
	}
	if strings.TrimSpace(incidentID) == "" {
		return
	}

	jobID := payloadString(payload, "job_id")
	toolCallID := payloadString(payload, "tool_call_id")
	datasourceID := payloadString(payload, "datasource_id")
	eventID := payloadString(payload, "event_id")

	if !db.Migrator().HasTable("incident_timeline") {
		slog.InfoContext(ctx, "skip timeline append: table not found",
			"request_id", contextx.RequestID(ctx),
			"incident_id", incidentID,
			"event_id", eventID,
			"job_id", jobID,
			"tool_call_id", toolCallID,
			"datasource_id", datasourceID,
		)
		return
	}

	columns, err := db.Migrator().ColumnTypes("incident_timeline")
	if err != nil {
		slog.WarnContext(ctx, "skip timeline append: inspect columns failed",
			"request_id", contextx.RequestID(ctx),
			"incident_id", incidentID,
			"event_id", eventID,
			"job_id", jobID,
			"tool_call_id", toolCallID,
			"datasource_id", datasourceID,
			"error", err,
		)
		return
	}
	colSet := make(map[string]struct{}, len(columns))
	for _, c := range columns {
		colSet[strings.ToLower(c.Name())] = struct{}{}
	}
	if _, ok := colSet["incident_id"]; !ok {
		slog.WarnContext(ctx, "skip timeline append: incident_id column missing",
			"request_id", contextx.RequestID(ctx),
			"incident_id", incidentID,
			"event_id", eventID,
			"job_id", jobID,
			"tool_call_id", toolCallID,
			"datasource_id", datasourceID,
		)
		return
	}

	row := map[string]any{
		"incident_id": incidentID,
	}
	now := time.Now().UTC()
	if hasCol(colSet, "event_type") {
		row["event_type"] = eventType
	} else if hasCol(colSet, "type") {
		row["type"] = eventType
	}
	if hasCol(colSet, "ref_id") {
		row["ref_id"] = refID
	} else if hasCol(colSet, "ref") {
		row["ref"] = refID
	}

	if payload != nil {
		if encoded, marshalErr := json.Marshal(payload); marshalErr == nil {
			switch {
			case hasCol(colSet, "payload_json"):
				row["payload_json"] = string(encoded)
			case hasCol(colSet, "detail_json"):
				row["detail_json"] = string(encoded)
			case hasCol(colSet, "detail"):
				row["detail"] = string(encoded)
			case hasCol(colSet, "message"):
				row["message"] = string(encoded)
			}
		}
	}

	if hasCol(colSet, "created_at") {
		row["created_at"] = now
	}
	if hasCol(colSet, "updated_at") {
		row["updated_at"] = now
	}

	if err := db.Table("incident_timeline").Create(row).Error; err != nil {
		slog.WarnContext(ctx, "append incident timeline failed",
			"request_id", contextx.RequestID(ctx),
			"incident_id", incidentID,
			"event_id", eventID,
			"job_id", jobID,
			"tool_call_id", toolCallID,
			"datasource_id", datasourceID,
			"error", err,
		)
	}
}

func payloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	v, ok := payload[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func hasCol(cols map[string]struct{}, key string) bool {
	_, ok := cols[key]
	return ok
}
