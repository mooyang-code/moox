package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 采集方式与执行端。
const (
	IngestStream = "stream"
	IngestPull   = "pull"

	ExecutorSCF   = "scf"
	ExecutorLocal = "local"
)

// IngestMode 是一个数据集的采集方式与拉取、补采的执行端。
type IngestMode struct {
	SpaceID   string
	DatasetID string
	Mode      string
	Executor  string
	Force     bool
	UpdatedAt time.Time
}

type ingestModeRow struct {
	SpaceID   string    `gorm:"column:c_space_id;primaryKey"`
	DatasetID string    `gorm:"column:c_dataset_id;primaryKey"`
	Mode      string    `gorm:"column:c_mode"`
	Executor  string    `gorm:"column:c_executor"`
	Force     bool      `gorm:"column:c_force"`
	UpdatedAt time.Time `gorm:"column:c_mtime"`
}

func (ingestModeRow) TableName() string { return "t_collector_ingest_modes" }

// ValidateIngestMode 检查采集方式与执行端的取值。
func ValidateIngestMode(mode, executor string) error {
	if mode != IngestStream && mode != IngestPull {
		return fmt.Errorf("采集方式必须是 stream 或 pull，收到 %q", mode)
	}
	if executor != ExecutorSCF && executor != ExecutorLocal {
		return fmt.Errorf("执行端必须是 scf 或 local，收到 %q", executor)
	}
	return nil
}

// IngestModeRepository 保存按数据集的采集方式。
type IngestModeRepository struct{ db *gorm.DB }

func NewIngestModeRepository(db *gorm.DB) *IngestModeRepository { return &IngestModeRepository{db: db} }

// List 返回空间全部数据集的采集方式（按数据集 ID 排序）。
func (r *IngestModeRepository) List(ctx context.Context, spaceID string) (map[string]IngestMode, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("ingest mode repository is not initialized")
	}
	var rows []ingestModeRow
	if err := r.db.WithContext(ctx).Where("c_space_id = ?", strings.ToLower(strings.TrimSpace(spaceID))).Order("c_dataset_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[string]IngestMode, len(rows))
	for _, row := range rows {
		result[row.DatasetID] = IngestMode{SpaceID: row.SpaceID, DatasetID: row.DatasetID, Mode: row.Mode, Executor: row.Executor, Force: row.Force, UpdatedAt: row.UpdatedAt}
	}
	return result, nil
}

// Initialize 为第一次出现的数据集写入初值；已有的不改（之后以库为准）。
func (r *IngestModeRepository) Initialize(ctx context.Context, modes []IngestMode) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("ingest mode repository is not initialized")
	}
	if len(modes) == 0 {
		return nil
	}
	rows := make([]ingestModeRow, 0, len(modes))
	for _, mode := range modes {
		if err := ValidateIngestMode(mode.Mode, mode.Executor); err != nil {
			return err
		}
		rows = append(rows, ingestModeRow{SpaceID: strings.ToLower(mode.SpaceID), DatasetID: mode.DatasetID, Mode: mode.Mode, Executor: mode.Executor, UpdatedAt: time.Now().UTC()})
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
}

// Set 写入一批数据集的采集方式（命令切换）；force 记在每一行，交接计划生效后由 ClearForce 清零。
func (r *IngestModeRepository) Set(ctx context.Context, modes []IngestMode) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("ingest mode repository is not initialized")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, mode := range modes {
			if err := ValidateIngestMode(mode.Mode, mode.Executor); err != nil {
				return err
			}
			row := ingestModeRow{SpaceID: strings.ToLower(mode.SpaceID), DatasetID: mode.DatasetID, Mode: mode.Mode, Executor: mode.Executor, Force: mode.Force, UpdatedAt: time.Now().UTC()}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "c_space_id"}, {Name: "c_dataset_id"}}, DoUpdates: clause.AssignmentColumns([]string{"c_mode", "c_executor", "c_force", "c_mtime"})}).Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ClearForce 清掉空间里全部 --force 标记。
func (r *IngestModeRepository) ClearForce(ctx context.Context, spaceID string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("ingest mode repository is not initialized")
	}
	return r.db.WithContext(ctx).Model(&ingestModeRow{}).Where("c_space_id = ? AND c_force = 1", strings.ToLower(spaceID)).Update("c_force", false).Error
}

// StreamWorker 是 collector-stream 实例的心跳记录。
type StreamWorker struct {
	SpaceID     string
	InstanceID  string
	Applied     string
	Status      string
	HeartbeatAt time.Time
}

type streamWorkerRow struct {
	SpaceID     string    `gorm:"column:c_space_id;primaryKey"`
	InstanceID  string    `gorm:"column:c_instance_id"`
	Applied     string    `gorm:"column:c_applied"`
	Status      string    `gorm:"column:c_status"`
	HeartbeatAt time.Time `gorm:"column:c_heartbeat_at"`
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
	row := streamWorkerRow{SpaceID: strings.ToLower(worker.SpaceID), InstanceID: worker.InstanceID, Applied: worker.Applied, Status: worker.Status, HeartbeatAt: worker.HeartbeatAt.UTC()}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "c_space_id"}}, DoUpdates: clause.AssignmentColumns([]string{"c_instance_id", "c_applied", "c_status", "c_heartbeat_at"})}).Create(&row).Error
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
	return &StreamWorker{SpaceID: row.SpaceID, InstanceID: row.InstanceID, Applied: row.Applied, Status: row.Status, HeartbeatAt: row.HeartbeatAt}, nil
}
