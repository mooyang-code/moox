package main

import (
	"path/filepath"
	"testing"

	monstorage "github.com/mooyang-code/moox/modules/monitor/internal/storage"
)

func TestInitSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "monitor.db")

	if err := run([]string{"init", "--db-path", dbPath}); err != nil {
		t.Fatalf("run init: %v", err)
	}

	mgr, err := monstorage.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer mgr.Close()

	for _, table := range []string{
		"t_monitor_checks",
		"t_monitor_check_results",
		"t_monitor_webhooks",
		"t_monitor_alert_rules",
		"t_monitor_alert_states",
		"t_monitor_alert_events",
		"t_monitor_instances",
		"t_monitor_peer_snapshots",
	} {
		if !mgr.DB().Migrator().HasTable(table) {
			t.Fatalf("table %s does not exist", table)
		}
	}
}
