package audit

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestAppendIncidentTimelineIfExists_EmptyIncidentIDEarlyReturn(t *testing.T) {
	db := newAuditTestDB(t)

	require.NotPanics(t, func() {
		AppendIncidentTimelineIfExists(context.Background(), db, "", "alert_silenced", "alert-event-1", map[string]any{
			"event_id":    "alert-event-1",
			"fingerprint": "fp-test",
			"silence_id":  "silence-test",
		})
	})

	var count int64
	require.NoError(t, db.Table("incident_timeline").Count(&count).Error)
	require.Equal(t, int64(0), count)
}

func newAuditTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
CREATE TABLE incident_timeline (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  incident_id TEXT NOT NULL
)`).Error)
	return db
}
