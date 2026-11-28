package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// FetchPlan 状态：pending（已保存，尚未登记快照）→ applying（已登记，正在下发）→ applied（下发确认，已生效）。
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
	ID        int64  `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID   string `gorm:"column:c_space_id"`
	Status    string `gorm:"column:c_status"`
	Plan      string `gorm:"column:c_plan"`
	Progress  string `gorm:"column:c_progress"`
	LastError string `gorm:"column:c_last_error"`
	CreatedAt int64  `gorm:"column:c_ctime"`
	UpdatedAt int64  `gorm:"column:c_mtime"`
}

func (fetchPlanRow) TableName() string { return "t_collector_fetch_plans" }

func (row fetchPlanRow) record() *FetchPlanRecord {
	return &FetchPlanRecord{
		ID: row.ID, SpaceID: row.SpaceID, Status: row.Status, Plan: row.Plan, Progress: row.Progress, LastError: row.LastError,
		CreatedAt: fromUnixMilli(row.CreatedAt), UpdatedAt: fromUnixMilli(row.UpdatedAt),
	}
}

// FetchPlanRepository 保存采集计划。每个空间同一时间最多一份未生效的计划，由唯一索引保证，它通常几分钟内生效；
// 一份计划生效时删掉同空间更早的已生效计划，所以每个空间只留未生效的一份与最近已生效的一份（完整的已生效状态）。
// 旧计划没有读者：对账只读最近已生效的一份，补采的来源取当前任务、交易所符号取标的时间线。
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
// timeline 是这份计划一建立就成立的时间线行（不再采集的数据集从生效周期起停止），与计划记录在同一个事务里写入。
func (r *FetchPlanRepository) Create(ctx context.Context, spaceID, plan, progress string, timeline ...SeriesTimelineEntry) (*FetchPlanRecord, error) {
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
	now := nowUnixMilli()
	row := fetchPlanRow{SpaceID: spaceID, Status: FetchPlanPending, Plan: plan, Progress: progress, CreatedAt: now, UpdatedAt: now}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&row).Error; err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				return ErrFetchPlanOpen
			}
			return err
		}
		return appendSeriesTimeline(tx, timeline)
	})
	if err != nil {
		return nil, err
	}
	return row.record(), nil
}

// Update 更新计划的状态、进度与最近错误；状态改成已生效时，同一个事务里删掉同空间更早的已生效计划。
// timeline 是随这次进度一起成立的时间线行（Storage 刚确认登记的数据集从生效周期起的序列），与进度在同一个事务里写入。
func (r *FetchPlanRepository) Update(ctx context.Context, id int64, status, progress, lastError string, timeline ...SeriesTimelineEntry) error {
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
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&fetchPlanRow{}).Where("c_id = ? AND c_status <> ?", id, FetchPlanApplied).Updates(map[string]any{"c_status": status, "c_progress": progress, "c_last_error": lastError, "c_mtime": nowUnixMilli()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		if status == FetchPlanApplied {
			var row fetchPlanRow
			if err := tx.Where("c_id = ?", id).Take(&row).Error; err != nil {
				return err
			}
			if err := tx.Where("c_space_id = ? AND c_status = ? AND c_id < ?", row.SpaceID, FetchPlanApplied, id).Delete(&fetchPlanRow{}).Error; err != nil {
				return err
			}
		}
		return appendSeriesTimeline(tx, timeline)
	})
}

// SavePlan 更新未生效计划的内容（登记被拒、撤回某个数据集时使用）。
func (r *FetchPlanRepository) SavePlan(ctx context.Context, id int64, plan string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("fetch plan repository is not initialized")
	}
	if strings.TrimSpace(plan) == "" {
		return fmt.Errorf("plan is required")
	}
	result := r.db.WithContext(ctx).Model(&fetchPlanRow{}).Where("c_id = ? AND c_status <> ?", id, FetchPlanApplied).Updates(map[string]any{"c_plan": plan, "c_mtime": nowUnixMilli()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// DeleteOpen 删掉一份还没开始登记的未生效计划：同一个事务里确认它仍是 pending 再删。
// 计划里的数据集全部撤回之后已经没有任何变化时使用；调用方保证它没有已登记的数据集。
func (r *FetchPlanRepository) DeleteOpen(ctx context.Context, id int64) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("fetch plan repository is not initialized")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row fetchPlanRow
		found := tx.Where("c_id = ?", id).Limit(1).Find(&row)
		if found.Error != nil {
			return found.Error
		}
		if found.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		if row.Status != FetchPlanPending {
			return fmt.Errorf("fetch plan %d is %s, only a pending plan can be deleted", id, row.Status)
		}
		return tx.Where("c_id = ? AND c_status = ?", id, FetchPlanPending).Delete(&fetchPlanRow{}).Error
	})
}

// ReregisterAppliedPlan 改写一份已生效计划的内容，并在同一个事务里删掉该空间的全部时间线行。只给维护命令用：
// Storage 数据被清空后，把数据集标记为“快照未登记”，让下一轮对账重新登记（见 fetchplan.ClearRegisteredSnapshots）；
// 此前的时间线连同数据一起作废，重新登记后从新的生效周期写起。普通流程只改未生效的计划（SavePlan）。
// 该空间有未生效的计划时返回 ErrFetchPlanOpen：调用方在事务外查过一次，但两步之间对账器可能刚建了新计划，
// 这里在事务里再查一次，否则删时间线会把新计划刚写的行一起删掉。
func (r *FetchPlanRepository) ReregisterAppliedPlan(ctx context.Context, id int64, plan string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("fetch plan repository is not initialized")
	}
	if strings.TrimSpace(plan) == "" {
		return fmt.Errorf("plan is required")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row fetchPlanRow
		found := tx.Where("c_id = ? AND c_status = ?", id, FetchPlanApplied).Limit(1).Find(&row)
		if found.Error != nil {
			return found.Error
		}
		if found.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		var open int64
		if err := tx.Model(&fetchPlanRow{}).Where("c_space_id = ? AND c_status <> ?", row.SpaceID, FetchPlanApplied).Count(&open).Error; err != nil {
			return err
		}
		if open > 0 {
			return ErrFetchPlanOpen
		}
		if err := tx.Model(&fetchPlanRow{}).Where("c_id = ?", id).Updates(map[string]any{"c_plan": plan, "c_mtime": nowUnixMilli()}).Error; err != nil {
			return err
		}
		return deleteSeriesTimeline(tx, row.SpaceID)
	})
}

type fetchPlanPauseRow struct {
	ID      int64  `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID string `gorm:"column:c_space_id"`
}

func (fetchPlanPauseRow) TableName() string { return "t_collector_fetch_plan_pauses" }

// PausedSpaces 返回当前处于手工暂停状态的空间。系统级的暂停由模块生命周期（lifecycle.json）承载，不记在这里。
func (r *FetchPlanRepository) PausedSpaces(ctx context.Context) (map[string]bool, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("fetch plan repository is not initialized")
	}
	var rows []fetchPlanPauseRow
	if err := r.db.WithContext(ctx).Where("c_paused = ?", true).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(rows))
	for _, row := range rows {
		out[strings.ToLower(row.SpaceID)] = true
	}
	return out, nil
}

// SetPaused 保存空间的手工暂停开关（collector fetch-plan pause|resume）；相同取值重复保存没有副作用。
func (r *FetchPlanRepository) SetPaused(ctx context.Context, spaceID string, paused bool) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("fetch plan repository is not initialized")
	}
	spaceID = strings.ToLower(strings.TrimSpace(spaceID))
	if spaceID == "" {
		return errors.New("space_id is required")
	}
	return r.db.WithContext(ctx).Exec(`
		INSERT INTO t_collector_fetch_plan_pauses (c_space_id, c_paused) VALUES (?, ?)
		ON CONFLICT(c_space_id) DO UPDATE SET c_paused = excluded.c_paused
	`, spaceID, paused).Error
}
