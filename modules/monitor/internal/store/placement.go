package store

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ReconcilePlacements atomically replaces the system-owned probe definitions.
// Probe history and scheduling survive refreshes. Re-enabling a check makes it
// immediately due; disabling/removing a check also retires its firing alerts.
func (r *CheckRepository) ReconcilePlacements(ctx context.Context, desired []domain.Check) (int, error) {
	count := 0
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE t_monitor_checks SET c_id = c_id WHERE 0").Error; err != nil {
			return err
		}
		var existing []domain.Check
		if err := tx.Where("c_space_id = ''").Find(&existing).Error; err != nil {
			return err
		}
		byID := make(map[string]domain.Check, len(existing))
		for _, check := range existing {
			byID[check.CheckID] = check
		}
		keep := make(map[string]bool, len(desired))
		for _, check := range desired {
			if check.Source != domain.CheckSourcePlacement || check.SpaceID != "" || check.CheckID == "" || keep[check.CheckID] {
				return fmt.Errorf("invalid or duplicate placement check %q", check.CheckID)
			}
			keep[check.CheckID] = true
			previous, exists := byID[check.CheckID]
			if exists && previous.Source != domain.CheckSourcePlacement {
				return fmt.Errorf("placement check %q collides with another source", check.CheckID)
			}
			columns := []string{"c_name", "c_group_name", "c_kind", "c_url", "c_connect_address", "c_server_name", "c_trust_mode", "c_ca_file", "c_ca_baseline", "c_method", "c_headers", "c_body", "c_tcp_host", "c_tcp_port", "c_interval_seconds", "c_timeout_ms", "c_expected_status", "c_max_response_ms", "c_body_contains", "c_enabled", "c_labels", "c_description"}
			if exists && !previous.Enabled && check.Enabled {
				columns = append(columns, "c_next_check_at")
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "c_space_id"}, {Name: "c_check_id"}},
				DoUpdates: clause.AssignmentColumns(columns),
			}).Create(&check).Error; err != nil {
				return err
			}
			if !check.Enabled {
				if err := deletePlacementAlerts(tx, check.CheckID); err != nil {
					return err
				}
			}
			count++
		}
		for _, check := range existing {
			if check.Source != domain.CheckSourcePlacement || keep[check.CheckID] {
				continue
			}
			if err := deletePlacementAlerts(tx, check.CheckID); err != nil {
				return err
			}
			if err := tx.Where("c_space_id = '' AND c_check_id = ?", check.CheckID).Delete(&domain.CheckResult{}).Error; err != nil {
				return err
			}
			if err := tx.Delete(&check).Error; err != nil {
				return err
			}
			count++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

func deletePlacementAlerts(tx *gorm.DB, checkID string) error {
	if err := tx.Where("c_space_id = '' AND c_check_id = ?", checkID).Delete(&domain.AlertState{}).Error; err != nil {
		return err
	}
	return tx.Where("c_space_id = '' AND c_check_id = ?", checkID).Delete(&domain.AlertRule{}).Error
}
