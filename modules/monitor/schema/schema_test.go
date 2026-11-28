package schema

import (
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"gorm.io/gorm"
)

func TestMonitorSchemaCreatesTablesAndIndexes(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer mgr.Close()
	if err := mgr.ApplySchema(SQL()); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	_, err = store.WithDatabase(mgr, func(db *gorm.DB) struct{} {
		for _, table := range []string{
			"t_monitor_checks",
			"t_monitor_notification_channels",
			"t_monitor_alert_events",
			"t_monitor_host_agents",
			"t_monitor_host_samples",
			"t_monitor_metric_services", "t_monitor_metric_series", "t_monitor_metric_ingest_messages",
			"t_monitor_subject_first_seen",
		} {
			if !db.Migrator().HasTable(table) {
				t.Fatalf("table %s does not exist", table)
			}
		}

		for _, index := range []string{
			"idx_t_monitor_checks_alert",
			"idx_t_monitor_alert_events_check",
			"uk_monitor_metric_services_key", "uk_monitor_metric_series_key", "uk_monitor_metric_ingest_message",
		} {
			var count int64
			if err := db.Raw(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&count).Error; err != nil {
				t.Fatalf("query index %s: %v", index, err)
			}
			if count != 1 {
				t.Fatalf("index %s count = %d", index, count)
			}
		}
		return struct{}{}
	})
	if err != nil {
		t.Fatal(err)
	}
}
