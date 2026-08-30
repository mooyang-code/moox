package store

import (
	"context"
	"fmt"
	"strings"
)

const legacyOwnerReconciledColumn = "owner_reconciled"

// LegacyRunnerOwner is the minimum V1 identity needed to release a Trade
// owner that survived the Strategy table archive during a V2 upgrade.
type LegacyRunnerOwner struct {
	TableName        string `gorm:"-"`
	RunnerID         string `gorm:"column:runner_id"`
	SpaceID          string `gorm:"column:space_id"`
	LogicalAccountID string `gorm:"column:logical_account_id"`
}

// ListLegacyRunnerOwners returns archived V1 runner/account bindings. The
// archive is intentionally outside the active schema namespace, but its owner
// identities remain actionable until Trade confirms release.
func (m *Store) ListLegacyRunnerOwners(ctx context.Context) ([]LegacyRunnerOwner, error) {
	if m == nil || m.db == nil {
		return nil, fmt.Errorf("strategy database is not open")
	}
	var tables []string
	if err := m.db.WithContext(ctx).Raw(`
		SELECT name FROM sqlite_master
		WHERE type = 'table' AND name LIKE 'legacy_strategy_v1_strategy_runners%'
		ORDER BY name
	`).Scan(&tables).Error; err != nil {
		return nil, fmt.Errorf("inspect archived Strategy runners: %w", err)
	}
	owners := make([]LegacyRunnerOwner, 0)
	for _, table := range tables {
		quoted := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
		var rows []LegacyRunnerOwner
		if err := m.db.WithContext(ctx).Raw(
			`SELECT runner_id, space_id, logical_account_id FROM ` + quoted + ` WHERE logical_account_id IS NOT NULL AND TRIM(logical_account_id) <> '' AND COALESCE(owner_reconciled, 0) = 0`,
		).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("read archived Strategy runners %s: %w", table, err)
		}
		for index := range rows {
			rows[index].TableName = table
		}
		owners = append(owners, rows...)
	}
	return owners, nil
}

// MarkLegacyRunnerOwnerReconciled records that Trade has accepted release of
// an archived V1 owner. It preserves the archived row while keeping future
// reconciliation passes read-only for that owner.
func (m *Store) MarkLegacyRunnerOwnerReconciled(ctx context.Context, owner LegacyRunnerOwner) error {
	if m == nil || m.db == nil {
		return fmt.Errorf("strategy database is not open")
	}
	if strings.TrimSpace(owner.TableName) == "" || strings.TrimSpace(owner.RunnerID) == "" {
		return fmt.Errorf("archived runner table and runner_id are required")
	}
	quoted := `"` + strings.ReplaceAll(owner.TableName, `"`, `""`) + `"`
	result := m.db.WithContext(ctx).Exec(
		`UPDATE `+quoted+` SET `+legacyOwnerReconciledColumn+` = 1 WHERE runner_id = ?`, owner.RunnerID,
	)
	if result.Error != nil {
		return fmt.Errorf("mark archived Strategy runner %s reconciled: %w", owner.RunnerID, result.Error)
	}
	return nil
}
