package store

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

func (r *PeerRepository) CountActive(ctx context.Context) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&domain.MonitorInstance{}).
		Where("c_status = ?", domain.InstanceStatusActive).Count(&count).Error
	return count, err
}

func (r *PeerRepository) IsActive(ctx context.Context, instanceID string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&domain.MonitorInstance{}).
		Where("c_instance_id = ? AND c_status = ?", instanceID, domain.InstanceStatusActive).Count(&count).Error
	return count > 0, err
}

func (r *PeerRepository) GetInstance(ctx context.Context, instanceID string) (*domain.MonitorInstance, error) {
	var instance domain.MonitorInstance
	result := r.db.WithContext(ctx).Where("c_instance_id = ?", instanceID).Limit(1).Find(&instance)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return &instance, nil
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
	_, err := r.MarkStaleTransitions(ctx, before)
	return err
}

// MarkStaleTransitions returns only peers changed from active to down by this
// call, allowing callers to emit one alert transition under concurrent pulls.
func (r *PeerRepository) MarkStaleTransitions(ctx context.Context, before time.Time) ([]string, error) {
	instances, err := r.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	transitioned := make([]string, 0)
	for _, instance := range instances {
		if instance.Status != domain.InstanceStatusActive || instance.LastSeenAt == nil || !instance.LastSeenAt.Before(before) {
			continue
		}
		result := r.db.WithContext(ctx).
			Model(&domain.MonitorInstance{}).
			Where("c_instance_id = ? AND c_status = ?", instance.InstanceID, domain.InstanceStatusActive).
			Updates(map[string]any{"c_status": domain.InstanceStatusDown})
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected > 0 {
			transitioned = append(transitioned, instance.InstanceID)
		}
	}
	return transitioned, nil
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
