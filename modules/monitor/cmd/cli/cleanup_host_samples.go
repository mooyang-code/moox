package main

import (
	"errors"
	"flag"
	"fmt"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
)

var legacyHostSampleTables = []string{
	"t_monitor_host_agents", "t_monitor_host_inbox", "t_monitor_host_latest",
	"t_monitor_host_history", "t_monitor_host_history_outbox",
	"t_monitor_host_alert_rules", "t_monitor_host_alert_states",
	"t_monitor_host_alert_events", "t_monitor_host_notification_outbox",
}

func runCleanupHostSampleTables(args []string) error {
	flags := flag.NewFlagSet("cleanup-host-sample-tables", flag.ContinueOnError)
	dbPath := flags.String("db-path", config.Default().Database.Path, "monitor sqlite database path")
	confirm := flags.Bool("confirm", false, "confirm destructive legacy table removal")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !*confirm {
		return errors.New("refusing to remove legacy host sample tables without --confirm")
	}
	mgr, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer mgr.Close()
	tx := mgr.DB().Begin()
	if tx.Error != nil {
		return tx.Error
	}
	for _, table := range legacyHostSampleTables {
		if err := tx.Exec("DROP TABLE IF EXISTS \"" + table + "\"").Error; err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("drop %s: %w", table, err)
		}
	}
	if err := tx.Commit().Error; err != nil {
		return err
	}
	fmt.Printf("legacy host sample tables removed: %s\n", *dbPath)
	return nil
}
