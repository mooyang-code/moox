package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// FetchPlan 状态：pending（已保存，尚未登记快照）→ applying（已登记，正在下发与收尾）→ applied（全部完成，已生效）。
const (
	FetchPlanPending  = "pending"
	FetchPlanApplying = "applying"
	FetchPlanApplied  = "applied"
)

// ErrFetchPlanOpen 表示该空间已有未生效的采集计划。
var ErrFetchPlanOpen = errors.New("space already has an unapplied fetch plan")

// FetchPlanRecord 是一份采集计划的持久化记录。Plan 与 Progress 是 JSON 文本。
type FetchPlanRecord struct {
	ID        int64
	SpaceID   string
	Status    string
	Plan      string
	Progress  string
	LastError string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type fetchPlanRow struct {
	ID        int64     `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID   string    `gorm:"column:c_space_id"`
	Status    string    `gorm:"column:c_status"`
	Plan      string    `gorm:"column:c_plan"`
	Progress  string    `gorm:"column:c_progress"`
	LastError string    `gorm:"column:c_last_error"`
	CreatedAt time.Time `gorm:"column:c_ctime"`
	UpdatedAt time.Time `gorm:"column:c_mtime"`
}

func (fetchPlanRow) TableName() string { return "t_collector_fetch_plans" }

func (row fetchPlanRow) record() *FetchPlanRecord {
	return &FetchPlanRecord{ID: row.ID, SpaceID: row.SpaceID, Status: row.Status, Plan: row.Plan, Progress: row.Progress, LastError: row.LastError, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

// FetchPlanRepository 保存采集计划。每个空间同一时间最多一份未生效的计划，由唯一索引保证。
type FetchPlanRepository struct{ db *gorm.DB }

func NewFetchPlanRepository(db *gorm.DB) *FetchPlanRepository { return &FetchPlanRepository{db: db} }

// Spaces 返回保存过采集计划的全部空间（排序）：配置移除某个空间后，据此找到仍需停用定时器的空间。
func (r *FetchPlanRepository) Spaces(ctx context.Context) ([]string, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("fetch plan repository is not initialized")
	}
	var spaces []string
	if err := r.db.WithContext(ctx).Model(&fetchPlanRow{}).Distinct("c_space_id").Order("c_space_id").Pluck("c_space_id", &spaces).Error; err != nil {
		return nil, err
	}
	return spaces, nil
}

// Open 返回该空间未生效的计划；没有则返回 nil。
func (r *FetchPlanRepository) Open(ctx context.Context, spaceID string) (*FetchPlanRecord, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("fetch plan repository is not initialized")
	}
	var row fetchPlanRow
	result := r.db.WithContext(ctx).Where("c_space_id = ? AND c_status <> ?", strings.TrimSpace(spaceID), FetchPlanApplied).Order("c_id DESC").Limit(1).Find(&row)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	return row.record(), nil
}

// LatestApplied 返回该空间最近一份已生效的计划；没有则返回 nil。
func (r *FetchPlanRepository) LatestApplied(ctx context.Context, spaceID string) (*FetchPlanRecord, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("fetch plan repository is not initialized")
	}
	var row fetchPlanRow
	result := r.db.WithContext(ctx).Where("c_space_id = ? AND c_status = ?", strings.TrimSpace(spaceID), FetchPlanApplied).Order("c_id DESC").Limit(1).Find(&row)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	return row.record(), nil
}

// Create 保存新的计划，状态为 pending。该空间已有未生效计划时返回 ErrFetchPlanOpen。
func (r *FetchPlanRepository) Create(ctx context.Context, spaceID, plan, progress string) (*FetchPlanRecord, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("fetch plan repository is not initialized")
	}
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" || strings.TrimSpace(plan) == "" {
		return nil, fmt.Errorf("space_id and plan are required")
	}
	if strings.TrimSpace(progress) == "" {
		progress = "{}"
	}
	row := fetchPlanRow{SpaceID: spaceID, Status: FetchPlanPending, Plan: plan, Progress: progress, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, ErrFetchPlanOpen
		}
		return nil, err
	}
	return row.record(), nil
}

// Update 更新计划的状态、进度与最近错误。
func (r *FetchPlanRepository) Update(ctx context.Context, id int64, status, progress, lastError string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("fetch plan repository is not initialized")
	}
	switch status {
	case FetchPlanPending, FetchPlanApplying, FetchPlanApplied:
	default:
		return fmt.Errorf("unsupported fetch plan status %q", status)
	}
	if strings.TrimSpace(progress) == "" {
		progress = "{}"
	}
	result := r.db.WithContext(ctx).Model(&fetchPlanRow{}).Where("c_id = ? AND c_status <> ?", id, FetchPlanApplied).Updates(map[string]any{"c_status": status, "c_progress": progress, "c_last_error": lastError, "c_mtime": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// SavePlan 更新计划内容（重新选择生效周期时使用）。
func (r *FetchPlanRepository) SavePlan(ctx context.Context, id int64, plan string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("fetch plan repository is not initialized")
	}
	if strings.TrimSpace(plan) == "" {
		return fmt.Errorf("plan is required")
	}
	result := r.db.WithContext(ctx).Model(&fetchPlanRow{}).Where("c_id = ? AND c_status <> ?", id, FetchPlanApplied).Updates(map[string]any{"c_plan": plan, "c_mtime": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ListAll 按先后顺序返回该空间全部采集计划（含未生效的），用于还原历史上的应有标的。
func (r *FetchPlanRepository) ListAll(ctx context.Context, spaceID string) ([]*FetchPlanRecord, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("fetch plan repository is not initialized")
	}
	var rows []fetchPlanRow
	if err := r.db.WithContext(ctx).Where("c_space_id = ?", strings.TrimSpace(spaceID)).Order("c_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]*FetchPlanRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, row.record())
	}
	return records, nil
}
