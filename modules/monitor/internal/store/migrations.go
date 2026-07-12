package store

import (
	"fmt"

	"gorm.io/gorm"
)

var legacyHostSampleTables = []string{
	"t_monitor_host_agents", "t_monitor_host_inbox", "t_monitor_host_latest",
	"t_monitor_host_history", "t_monitor_host_history_outbox",
	"t_monitor_host_alert_rules", "t_monitor_host_alert_states",
	"t_monitor_host_alert_events", "t_monitor_host_notification_outbox",
}

// EnsureMetricRuleStateColumns upgrades databases created before persisted
// metric notification state was introduced.
func (m *Store) EnsureMetricRuleStateColumns() error {
	if m == nil || m.db == nil {
		return fmt.Errorf("monitor database is not open")
	}
	var columns []struct {
		Name string `gorm:"column:name"`
	}
	if err := m.db.Raw("PRAGMA table_info(t_monitor_metric_rule_states)").Scan(&columns).Error; err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		seen[column.Name] = struct{}{}
	}
	for _, column := range []struct {
		name string
		ddl  string
	}{
		{"c_notification_event", "TEXT NOT NULL DEFAULT ''"},
		{"c_notification_key", "TEXT NOT NULL DEFAULT ''"},
		{"c_notification_status", "TEXT NOT NULL DEFAULT ''"},
		{"c_notification_error", "TEXT NOT NULL DEFAULT ''"},
		{"c_notification_attempts", "INTEGER NOT NULL DEFAULT 0"},
		{"c_last_notification_at", "DATETIME"},
	} {
		if _, ok := seen[column.name]; ok {
			continue
		}
		if err := m.db.Exec("ALTER TABLE t_monitor_metric_rule_states ADD COLUMN " + column.name + " " + column.ddl).Error; err != nil {
			return fmt.Errorf("add %s: %w", column.name, err)
		}
	}
	return nil
}

// DropLegacyHostSampleTables removes the obsolete host-monitor tables in one
// transaction. The table list is intentionally fixed to prevent arbitrary DDL.
func (m *Store) DropLegacyHostSampleTables() error {
	if m == nil || m.db == nil {
		return fmt.Errorf("monitor database is not open")
	}
	return m.db.Transaction(func(tx *gorm.DB) error {
		for _, table := range legacyHostSampleTables {
			if err := tx.Exec("DROP TABLE IF EXISTS \"" + table + "\"").Error; err != nil {
				return fmt.Errorf("drop %s: %w", table, err)
			}
		}
		return nil
	})
}
