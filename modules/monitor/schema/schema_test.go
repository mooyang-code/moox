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
			"t_monitor_check_results",
			"t_monitor_notification_channels",
			"t_monitor_alert_rules",
			"t_monitor_alert_states",
			"t_monitor_alert_events",
			"t_monitor_host_agents", "t_monitor_host_agent_aliases",
			"t_monitor_metric_services", "t_monitor_metric_series", "t_monitor_metric_latest", "t_monitor_metric_ingest_messages",
		} {
			if !db.Migrator().HasTable(table) {
				t.Fatalf("table %s does not exist", table)
			}
		}

		for _, index := range []string{
			"uk_monitor_checks_key",
			"uk_monitor_alert_rules_key",
			"uk_monitor_alert_states_key",
			"uk_monitor_metric_services_key", "uk_monitor_metric_series_key", "uk_monitor_metric_latest_series", "uk_monitor_metric_ingest_message",
		} {
			var count int64
			if err := db.Raw(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&count).Error; err != nil {
				t.Fatalf("query index %s: %v", index, err)
			}
			if count != 1 {
				t.Fatalf("index %s count = %d", index, count)
			}
		}
		for _, table := range []string{"t_monitor_host_inbox", "t_monitor_host_latest", "t_monitor_host_history", "t_monitor_host_history_outbox", "t_monitor_host_alert_rules", "t_monitor_host_alert_states", "t_monitor_host_alert_events", "t_monitor_host_notification_outbox"} {
			if db.Migrator().HasTable(table) {
				t.Fatalf("legacy host sample table %s must not be created", table)
			}
		}
		for _, table := range []string{"t_monitor_instances", "t_monitor_peer_snapshots"} {
			if db.Migrator().HasTable(table) {
				t.Fatalf("single-instance schema must not create %s", table)
			}
		}
		for _, table := range []string{"t_monitor_alert_states", "t_monitor_alert_events"} {
			if db.Migrator().HasColumn(table, "c_owner_instance_id") {
				t.Fatalf("single-instance schema must not create %s.c_owner_instance_id", table)
			}
		}
		for _, table := range []string{"t_monitor_checks", "t_monitor_alert_rules"} {
			if db.Migrator().HasColumn(table, "c_is_deleted") {
				t.Fatalf("greenfield hard-delete schema must not create %s.c_is_deleted", table)
			}
		}
		for _, table := range []string{"t_monitor_webhooks", "t_monitor_metric_rules", "t_monitor_metric_rule_states", "t_monitor_metric_rule_evaluations", "t_monitor_metric_rule_channels"} {
			if db.Migrator().HasTable(table) {
				t.Fatalf("removed table %s must not be created", table)
			}
		}
		return struct{}{}
	})
	if err != nil {
		t.Fatal(err)
	}
}
