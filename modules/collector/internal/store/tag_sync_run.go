package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 标签同步的运行结果取值。
const (
	TagSyncSuccess = "success"
	TagSyncFailed  = "failed"
)

// TagSyncRun 是一个标签上一次标的同步的运行结果。LastSuccessAt 为零表示从未成功过。
type TagSyncRun struct {
	SpaceID       string
	TagID         string
	LastRunAt     time.Time
	LastStatus    string
	LastError     string
	LastSuccessAt time.Time
}

type tagSyncRunRow struct {
	SpaceID       string     `gorm:"column:c_space_id;primaryKey"`
	TagID         string     `gorm:"column:c_tag_id;primaryKey"`
	LastRunAt     time.Time  `gorm:"column:c_last_run_at"`
	LastStatus    string     `gorm:"column:c_last_status"`
	LastError     string     `gorm:"column:c_last_error"`
	LastSuccessAt *time.Time `gorm:"column:c_last_success_at"`
	MTime         time.Time  `gorm:"column:c_mtime"`
}

func (tagSyncRunRow) TableName() string { return "t_collector_tag_sync_runs" }

func (row tagSyncRunRow) run() TagSyncRun {
	run := TagSyncRun{SpaceID: row.SpaceID, TagID: row.TagID, LastRunAt: row.LastRunAt.UTC(), LastStatus: row.LastStatus, LastError: row.LastError}
	if row.LastSuccessAt != nil {
		run.LastSuccessAt = row.LastSuccessAt.UTC()
	}
	return run
}

// TagSyncRunRepository 保存标签的标的同步运行结果。
type TagSyncRunRepository struct{ db *gorm.DB }

func NewTagSyncRunRepository(db *gorm.DB) *TagSyncRunRepository {
	return &TagSyncRunRepository{db: db}
}

func (r *TagSyncRunRepository) ready() error {
	if r == nil || r.db == nil {
		return fmt.Errorf("tag sync run repository is not initialized")
	}
	return nil
}

// Record 记录一次运行结果：成功时同时更新最近成功时间，失败时保留上一次成功时间。
func (r *TagSyncRunRepository) Record(ctx context.Context, run TagSyncRun) error {
	if err := r.ready(); err != nil {
		return err
	}
	run.SpaceID, run.TagID = strings.TrimSpace(run.SpaceID), strings.TrimSpace(run.TagID)
	if run.SpaceID == "" || run.TagID == "" {
		return fmt.Errorf("tag sync run needs space_id and tag_id")
	}
	if run.LastStatus != TagSyncSuccess && run.LastStatus != TagSyncFailed {
		return fmt.Errorf("tag sync run status %q is invalid", run.LastStatus)
	}
	runAt := run.LastRunAt.UTC()
	row := tagSyncRunRow{SpaceID: run.SpaceID, TagID: run.TagID, LastRunAt: runAt, LastStatus: run.LastStatus, LastError: run.LastError, MTime: time.Now().UTC()}
	updates := []string{"c_last_run_at", "c_last_status", "c_last_error", "c_mtime"}
	if run.LastStatus == TagSyncSuccess {
		row.LastSuccessAt = &runAt
		updates = append(updates, "c_last_success_at")
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "c_space_id"}, {Name: "c_tag_id"}},
		DoUpdates: clause.AssignmentColumns(updates),
	}).Create(&row).Error
}

// List 返回空间里所有标签的运行结果；spaceID 为空表示所有空间。
func (r *TagSyncRunRepository) List(ctx context.Context, spaceID string) ([]TagSyncRun, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	query := r.db.WithContext(ctx)
	if spaceID = strings.TrimSpace(spaceID); spaceID != "" {
		query = query.Where("c_space_id = ?", spaceID)
	}
	var rows []tagSyncRunRow
	if err := query.Order("c_space_id").Order("c_tag_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]TagSyncRun, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.run())
	}
	return out, nil
}

// Delete 删除标签的运行结果（标签被删除后清理）。
func (r *TagSyncRunRepository) Delete(ctx context.Context, spaceID, tagID string) error {
	if err := r.ready(); err != nil {
		return err
	}
	return r.db.WithContext(ctx).Where("c_space_id = ? AND c_tag_id = ?", spaceID, tagID).Delete(&tagSyncRunRow{}).Error
}
