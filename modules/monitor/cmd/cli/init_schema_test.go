package main

import (
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"gorm.io/gorm"
)

func TestInitSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "monitor.db")

	if err := run([]string{"init", "--db-path", dbPath}); err != nil {
		t.Fatalf("run init: %v", err)
	}

	mgr, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer mgr.Close()

	_, err = store.WithDatabase(mgr, func(db *gorm.DB) struct{} {
		for _, table := range []string{
			"t_monitor_checks",
			"t_monitor_check_results",
			"t_monitor_notification_channels",
			"t_monitor_alert_rules",
			"t_monitor_alert_states",
			"t_monitor_alert_events",
			"t_monitor_host_agents",
			"t_monitor_host_agent_aliases",
		} {
			if !db.Migrator().HasTable(table) {
				t.Fatalf("table %s does not exist", table)
			}
		}
		return struct{}{}
	})
	if err != nil {
		t.Fatal(err)
	}
}
