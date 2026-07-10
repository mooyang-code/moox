package schema

import (
	_ "embed"
	"fmt"

	"gorm.io/gorm"
)

//go:embed monitor.sql
var monitorSQL string

func SQL() string {
	return monitorSQL
}

// EnsureMetricRuleStateColumns upgrades databases created before persisted
// metric notification state was introduced. SQLite has no portable
// ALTER TABLE ... ADD COLUMN IF NOT EXISTS, so inspect table_info first.
func EnsureMetricRuleStateColumns(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("monitor database is nil")
	}
	var columns []struct {
		Name string `gorm:"column:name"`
	}
	if err := db.Raw("PRAGMA table_info(t_monitor_metric_rule_states)").Scan(&columns).Error; err != nil {
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
		if err := db.Exec("ALTER TABLE t_monitor_metric_rule_states ADD COLUMN " + column.name + " " + column.ddl).Error; err != nil {
			return fmt.Errorf("add %s: %w", column.name, err)
		}
	}
	return nil
}
