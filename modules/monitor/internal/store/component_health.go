package store

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"gorm.io/gorm"
)

type ComponentHealthRepository struct{ db *gorm.DB }

func (r *ComponentHealthRepository) List(ctx context.Context) ([]domain.ComponentHealthState, error) {
	var rows []domain.ComponentHealthState
	err := r.db.WithContext(ctx).Order("c_host_id, c_component_id").Limit(1501).Find(&rows).Error
	if len(rows) > 1500 {
		return nil, fmt.Errorf("component health exceeds 1500 placements")
	}
	return rows, err
}

// Reconcile is called by the sampler, never by an overview HTTP request.
func (r *ComponentHealthRepository) Reconcile(ctx context.Context, rows []domain.ComponentHealthState, now time.Time) error {
	if now.IsZero() || len(rows) > 1500 {
		return fmt.Errorf("invalid component health observation")
	}
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		key := row.HostID + "\x00" + row.ComponentID
		if row.HostID == "" || row.ComponentID == "" || seen[key] || domain.HealthStatus(row.Status) != row.Status {
			return fmt.Errorf("invalid component health identity/status")
		}
		seen[key] = true
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE t_monitor_component_health SET c_status = c_status WHERE 0").Error; err != nil {
			return err
		}
		var old []domain.ComponentHealthState
		if err := tx.Limit(1501).Find(&old).Error; err != nil {
			return err
		}
		if len(old) > 1500 {
			return fmt.Errorf("component health exceeds 1500 placements")
		}
		previous := make(map[string]domain.ComponentHealthState, len(old))
		for _, row := range old {
			key := row.HostID + "\x00" + row.ComponentID
			previous[key] = row
			if !seen[key] && !row.ObservedAt.After(now) {
				if err := tx.Delete(&row).Error; err != nil {
					return err
				}
			}
		}
		for _, row := range rows {
			prev, exists := previous[row.HostID+"\x00"+row.ComponentID]
			if exists && prev.ObservedAt.After(now) {
				continue
			}
			row.SinceAt, row.ObservedAt = now.UTC(), now.UTC()
			if exists && prev.Status == row.Status {
				row.SinceAt = prev.SinceAt
			}
			if err := tx.Save(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
