package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxTopologyBytes = 2 << 20

type TopologyRepository struct{ db *gorm.DB }

type topologyRow struct {
	ID       int    `gorm:"column:c_id;primaryKey"`
	Snapshot string `gorm:"column:c_snapshot_json"`
}

func (topologyRow) TableName() string { return "t_monitor_topology" }

// Reconcile commits registration and its probe definitions together. A failed
// discovery or probe collision cannot leave the overview with half a topology.
func (r *TopologyRepository) Reconcile(ctx context.Context, snapshot domain.TopologySnapshot, checks []domain.Check) (int, error) {
	if err := snapshot.Validate(); err != nil {
		return 0, err
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return 0, err
	}
	if len(encoded) > maxTopologyBytes {
		return 0, fmt.Errorf("topology snapshot exceeds size limit")
	}
	count := 0
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE t_monitor_topology SET c_id = c_id WHERE 0").Error; err != nil {
			return err
		}
		var err error
		count, err = reconcilePlacementChecks(tx, checks)
		if err != nil {
			return err
		}
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "c_id"}}, DoUpdates: clause.AssignmentColumns([]string{"c_snapshot_json"}),
		}).Create(&topologyRow{ID: 1, Snapshot: string(encoded)}).Error
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// Snapshot returns nil until a complete discovery has succeeded. An absent
// snapshot must never be interpreted as an authoritative empty deployment set.
func (r *TopologyRepository) Snapshot(ctx context.Context) (*domain.TopologySnapshot, error) {
	var row topologyRow
	if err := r.db.WithContext(ctx).First(&row, 1).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if len(row.Snapshot) > maxTopologyBytes {
		return nil, fmt.Errorf("stored topology exceeds size limit")
	}
	var snapshot domain.TopologySnapshot
	if err := json.Unmarshal([]byte(row.Snapshot), &snapshot); err != nil {
		return nil, fmt.Errorf("decode topology: %w", err)
	}
	if err := snapshot.Validate(); err != nil {
		return nil, fmt.Errorf("invalid stored topology: %w", err)
	}
	return &snapshot, nil
}
