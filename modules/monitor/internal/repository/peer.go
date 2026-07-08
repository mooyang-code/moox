package repository

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type PeerRepository struct {
	db *gorm.DB
}

func NewPeerRepository(db *gorm.DB) *PeerRepository {
	return &PeerRepository{db: db}
}

func (r *PeerRepository) UpsertInstance(ctx context.Context, instance *domain.MonitorInstance) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "c_instance_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"c_base_url",
			"c_status",
			"c_last_seen_at",
			"c_snapshot",
			"c_is_local",
			"c_mtime",
		}),
	}).Create(instance).Error
}

func (r *PeerRepository) ListInstances(ctx context.Context) ([]domain.MonitorInstance, error) {
	var instances []domain.MonitorInstance
	err := r.db.WithContext(ctx).Order("c_instance_id ASC").Find(&instances).Error
	return instances, err
}

func (r *PeerRepository) MarkStale(ctx context.Context, before time.Time) error {
	return r.db.WithContext(ctx).
		Model(&domain.MonitorInstance{}).
		Where("c_last_seen_at IS NOT NULL AND c_last_seen_at < ? AND c_status = ?", before, domain.InstanceStatusActive).
		Updates(map[string]any{"c_status": domain.InstanceStatusDown}).Error
}

func (r *PeerRepository) UpsertSnapshot(ctx context.Context, snapshot *domain.PeerSnapshot) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "c_instance_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"c_base_url",
			"c_status",
			"c_snapshot",
			"c_checked_at",
			"c_mtime",
		}),
	}).Create(snapshot).Error
}

func (r *PeerRepository) ListSnapshots(ctx context.Context) ([]domain.PeerSnapshot, error) {
	var snapshots []domain.PeerSnapshot
	err := r.db.WithContext(ctx).Order("c_instance_id ASC").Find(&snapshots).Error
	return snapshots, err
}
