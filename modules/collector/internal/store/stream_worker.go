package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// StreamWorker 是 collector-stream 实例的心跳记录。
type StreamWorker struct {
	SpaceID       string
	InstanceID    string
	AppliedPlanID int64
	Status        string
	HeartbeatAt   time.Time
}

type streamWorkerRow struct {
	SpaceID       string `gorm:"column:c_space_id;primaryKey"`
	InstanceID    string `gorm:"column:c_instance_id"`
	AppliedPlanID int64  `gorm:"column:c_applied_plan_id"`
	Status        string `gorm:"column:c_status"`
	HeartbeatAt   int64  `gorm:"column:c_heartbeat_at"`
}

func (streamWorkerRow) TableName() string { return "t_collector_stream_workers" }

// StreamWorkerRepository 保存 collector-stream 的心跳。
type StreamWorkerRepository struct{ db *gorm.DB }

func NewStreamWorkerRepository(db *gorm.DB) *StreamWorkerRepository {
	return &StreamWorkerRepository{db: db}
}

// Heartbeat 记录一次心跳（覆盖该空间的那一行）。
func (r *StreamWorkerRepository) Heartbeat(ctx context.Context, worker StreamWorker) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("stream worker repository is not initialized")
	}
	row := streamWorkerRow{
		SpaceID:       strings.ToLower(worker.SpaceID),
		InstanceID:    worker.InstanceID,
		AppliedPlanID: worker.AppliedPlanID,
		Status:        worker.Status,
		HeartbeatAt:   toUnixMilli(worker.HeartbeatAt),
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "c_space_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"c_instance_id", "c_applied_plan_id", "c_status", "c_heartbeat_at"}),
	}).Create(&row).Error
}

// Get 返回该空间的实例记录；没有返回 nil。
func (r *StreamWorkerRepository) Get(ctx context.Context, spaceID string) (*StreamWorker, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("stream worker repository is not initialized")
	}
	var row streamWorkerRow
	result := r.db.WithContext(ctx).Where("c_space_id = ?", strings.ToLower(spaceID)).Limit(1).Find(&row)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	return &StreamWorker{
		SpaceID:       row.SpaceID,
		InstanceID:    row.InstanceID,
		AppliedPlanID: row.AppliedPlanID,
		Status:        row.Status,
		HeartbeatAt:   fromUnixMilli(row.HeartbeatAt),
	}, nil
}
