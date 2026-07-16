package store

import (
	"fmt"

	"gorm.io/gorm"
)

const plannedExecNodeColumn = "c_planned_exec_node"

func (s *Store) migrateTaskInstanceSchema() error {
	var count int64
	if err := s.db.Raw(`
SELECT COUNT(*)
FROM pragma_table_info('t_collector_task_instances')
WHERE name = ?
`, plannedExecNodeColumn).Scan(&count).Error; err != nil {
		return fmt.Errorf("inspect collector task instance schema: %w", err)
	}
	if count == 0 {
		return nil
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DROP INDEX IF EXISTS idx_collector_instances_exec").Error; err != nil {
			return fmt.Errorf("drop collector execution index: %w", err)
		}
		if err := tx.Exec("ALTER TABLE t_collector_task_instances DROP COLUMN c_planned_exec_node").Error; err != nil {
			return fmt.Errorf("drop planned execution node column: %w", err)
		}
		return nil
	})
}
