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

// Reconcile 保存一份完整的部署拓扑（这台主机应该有哪些组件）；拓扑不完整时不写入，概览不会看到半份拓扑。
func (r *TopologyRepository) Reconcile(ctx context.Context, snapshot domain.TopologySnapshot) error {
	if err := snapshot.Validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if len(encoded) > maxTopologyBytes {
		return fmt.Errorf("topology snapshot exceeds size limit")
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "c_id"}}, DoUpdates: clause.AssignmentColumns([]string{"c_snapshot_json"}),
	}).Create(&topologyRow{ID: 1, Snapshot: string(encoded)}).Error
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
