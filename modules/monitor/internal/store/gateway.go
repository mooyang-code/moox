package store

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GatewayRepository struct{ db *gorm.DB }

func (r *GatewayRepository) List(ctx context.Context) ([]domain.GatewayObservation, error) {
	var observations []domain.GatewayObservation
	err := r.db.WithContext(ctx).Order("c_host_id ASC").Find(&observations).Error
	return observations, err
}

// Reconcile receives one attempted observation per enabled host. Read failures
// preserve the last verified status and mismatch deadline. Removed/disabled
// hosts leave the active cache; their alert checks are retired by the evaluator.
func (r *GatewayRepository) Reconcile(ctx context.Context, observations []domain.GatewayObservation, now time.Time) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE t_monitor_gateway_observations SET c_host_id = c_host_id WHERE 0").Error; err != nil {
			return err
		}
		var previous []domain.GatewayObservation
		if err := tx.Find(&previous).Error; err != nil {
			return err
		}
		byID := map[string]domain.GatewayObservation{}
		for _, row := range previous {
			byID[row.HostID] = row
		}
		keep := map[string]bool{}
		for _, row := range observations {
			if row.HostID == "" || keep[row.HostID] {
				return fmt.Errorf("invalid or duplicate gateway observation %q", row.HostID)
			}
			keep[row.HostID] = true
			prior, found := byID[row.HostID]
			row.FirstObservedAt = now
			if found {
				row.FirstObservedAt = prior.FirstObservedAt
			}
			row.LastAttemptAt = now
			if row.ReadError != "" {
				row.StatusJSON, row.ExpectedHash, row.AppliedHash = prior.StatusJSON, prior.ExpectedHash, prior.AppliedHash
				row.ObservedAt, row.HashMismatchSince = prior.ObservedAt, prior.HashMismatchSince
			} else {
				row.ObservedAt = &now
				if row.ExpectedHash != "" && row.ExpectedHash != row.AppliedHash {
					row.HashMismatchSince = &now
					if prior.ExpectedHash == row.ExpectedHash && prior.HashMismatchSince != nil {
						row.HashMismatchSince = prior.HashMismatchSince
					}
				} else {
					row.HashMismatchSince = nil
				}
			}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "c_host_id"}}, UpdateAll: true}).Create(&row).Error; err != nil {
				return err
			}
		}
		for _, row := range previous {
			if !keep[row.HostID] {
				if err := tx.Delete(&row).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}
