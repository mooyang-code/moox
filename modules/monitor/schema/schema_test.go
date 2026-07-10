package schema

import (
	"path/filepath"
	"testing"

	monstorage "github.com/mooyang-code/moox/modules/monitor/internal/storage"
)

func TestMonitorSchemaCreatesTablesAndIndexes(t *testing.T) {
	mgr, err := monstorage.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer mgr.Close()
	if err := mgr.ApplySchema(SQL()); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	for _, table := range []string{
		"t_monitor_checks",
		"t_monitor_check_results",
		"t_monitor_webhooks",
		"t_monitor_alert_rules",
		"t_monitor_alert_states",
		"t_monitor_alert_events",
		"t_monitor_instances",
		"t_monitor_peer_snapshots",
		"t_monitor_metric_services", "t_monitor_metric_series", "t_monitor_metric_latest", "t_monitor_metric_ingest_messages",
		"t_monitor_metric_rules", "t_monitor_metric_rule_states", "t_monitor_metric_rule_evaluations", "t_monitor_metric_rule_channels",
	} {
		if !mgr.DB().Migrator().HasTable(table) {
			t.Fatalf("table %s does not exist", table)
		}
	}

	for _, index := range []string{
		"uk_monitor_checks_key",
		"uk_monitor_webhooks_key",
		"uk_monitor_alert_rules_key",
		"uk_monitor_alert_states_key",
		"uk_monitor_instances_id",
		"uk_monitor_metric_services_key", "uk_monitor_metric_series_key", "uk_monitor_metric_latest_series", "uk_monitor_metric_ingest_message",
		"uk_monitor_metric_rules_key", "uk_monitor_metric_rule_states_key", "uk_monitor_metric_rule_channels_key",
	} {
		var count int64
		if err := mgr.DB().Raw(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&count).Error; err != nil {
			t.Fatalf("query index %s: %v", index, err)
		}
		if count != 1 {
			t.Fatalf("index %s count = %d", index, count)
		}
	}
}
