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
			"t_monitor_webhooks",
			"t_monitor_alert_rules",
			"t_monitor_alert_states",
			"t_monitor_alert_events",
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

func TestCleanupHostSampleTablesRequiresConfirmation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "monitor.db")
	if err := run([]string{"init", "--db-path", dbPath}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"cleanup-host-sample-tables", "--db-path", dbPath}); err == nil {
		t.Fatal("cleanup without confirmation unexpectedly succeeded")
	}
}

func TestCleanupHostSampleTablesWithConfirmation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "monitor.db")
	if err := run([]string{"init", "--db-path", dbPath}); err != nil {
		t.Fatal(err)
	}
	mgr, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var execErr error
	if _, err := store.WithDatabase(mgr, func(db *gorm.DB) struct{} {
		execErr = db.Exec("CREATE TABLE t_monitor_host_history(c_id INTEGER)").Error
		return struct{}{}
	}); err != nil {
		mgr.Close()
		t.Fatal(err)
	}
	if execErr != nil {
		mgr.Close()
		t.Fatal(execErr)
	}
	_ = mgr.Close()
	if err := run([]string{"cleanup-host-sample-tables", "--db-path", dbPath, "--confirm"}); err != nil {
		t.Fatal(err)
	}
	mgr, err = store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	var exists bool
	if _, err := store.WithDatabase(mgr, func(db *gorm.DB) struct{} {
		exists = db.Migrator().HasTable("t_monitor_host_history")
		return struct{}{}
	}); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("legacy table still exists")
	}
}
