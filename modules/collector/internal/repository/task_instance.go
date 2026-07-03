package repository

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	defaultPageSize = 50
	maxPageSize     = 1000
)

// TaskInstanceFilter describes task instance list filters.
type TaskInstanceFilter struct {
	SpaceID         string
	TaskID          string
	RuleID          string
	Exchange        string
	Market          string
	DataType        string
	DatasetID       string
	SubjectID       string
	Interval        string
	PlannedExecNode string
	LastExecNode    string
	LastExecStatus  *int
	Symbol          string
	IncludeDeleted  bool
	Page            int
	PageSize        int
}

// ExecutionLog records task status reports.
type ExecutionLog struct {
	ID           int        `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID      string     `gorm:"column:c_space_id"`
	TaskID       string     `gorm:"column:c_task_id"`
	NodeID       string     `gorm:"column:c_node_id"`
	Status       int        `gorm:"column:c_status"`
	Result       string     `gorm:"column:c_result"`
	ErrorMessage string     `gorm:"column:c_error_message"`
	StartedAt    *time.Time `gorm:"column:c_started_at"`
	FinishedAt   *time.Time `gorm:"column:c_finished_at"`
	CreateTime   time.Time  `gorm:"column:c_ctime"`
}

// TableName returns the execution log table.
func (l *ExecutionLog) TableName() string {
	return "t_collector_execution_logs"
}

// TaskInstanceRepository persists executable task instances.
type TaskInstanceRepository struct {
	db *gorm.DB
}

// NewTaskInstanceRepository creates a repository.
func NewTaskInstanceRepository(db *gorm.DB) *TaskInstanceRepository {
	return &TaskInstanceRepository{db: db}
}

// List returns task instances matching filters.
func (r *TaskInstanceRepository) List(ctx context.Context, filter TaskInstanceFilter) ([]domain.TaskInstance, int64, error) {
	q := r.applyFilter(r.db.WithContext(ctx).Model(&domain.TaskInstance{}), filter)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, size := normalizePage(filter.Page, filter.PageSize)
	var instances []domain.TaskInstance
	err := q.Order("c_id DESC").Limit(size).Offset((page - 1) * size).Find(&instances).Error
	return instances, total, err
}

// UpsertMany creates or updates task instances by stable task id.
func (r *TaskInstanceRepository) UpsertMany(ctx context.Context, instances []domain.TaskInstance) error {
	if len(instances) == 0 {
		return nil
	}
	now := time.Now().UTC()
	for i := range instances {
		if instances[i].CreateTime.IsZero() {
			instances[i].CreateTime = now
		}
		instances[i].ModifyTime = now
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "c_space_id"},
			{Name: "c_task_id"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"c_rule_id",
			"c_exchange",
			"c_market",
			"c_data_type",
			"c_dataset_id",
			"c_subject_id",
			"c_symbol",
			"c_interval",
			"c_planned_exec_node",
			"c_task_params",
			"c_is_deleted",
			"c_mtime",
		}),
	}).Create(&instances).Error
}

// UpdateStatus updates a task instance by Collector task id.
func (r *TaskInstanceRepository) UpdateStatus(ctx context.Context, spaceID string, taskID string, nodeID string, status int, result string) error {
	now := time.Now().UTC()
	updates := map[string]any{
		"c_last_exec_node":   nodeID,
		"c_last_exec_status": status,
		"c_last_exec_time":   now,
		"c_result":           normalizeJSON(result),
		"c_mtime":            now,
	}
	tx := r.db.WithContext(ctx).
		Model(&domain.TaskInstance{}).
		Where("c_space_id = ? AND c_task_id = ?", spaceID, taskID).
		Updates(updates)
	if tx.Error != nil {
		return tx.Error
	}
	if tx.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// AddExecutionLog appends a status report log row.
func (r *TaskInstanceRepository) AddExecutionLog(ctx context.Context, spaceID string, taskID string, nodeID string, status int, result string, errorMessage string, duration time.Duration) error {
	now := time.Now().UTC()
	startedAt := now.Add(-duration)
	log := ExecutionLog{
		SpaceID:      spaceID,
		TaskID:       taskID,
		NodeID:       nodeID,
		Status:       status,
		Result:       normalizeJSON(result),
		ErrorMessage: errorMessage,
		StartedAt:    &startedAt,
		FinishedAt:   &now,
		CreateTime:   now,
	}
	if duration <= 0 {
		log.StartedAt = nil
	}
	return r.db.WithContext(ctx).Create(&log).Error
}

func (r *TaskInstanceRepository) applyFilter(q *gorm.DB, filter TaskInstanceFilter) *gorm.DB {
	if filter.SpaceID != "" {
		q = q.Where("c_space_id = ?", filter.SpaceID)
	}
	if filter.TaskID != "" {
		q = q.Where("c_task_id LIKE ?", "%"+filter.TaskID+"%")
	}
	if filter.RuleID != "" {
		q = q.Where("c_rule_id LIKE ?", "%"+filter.RuleID+"%")
	}
	if filter.Exchange != "" {
		q = q.Where("c_exchange = ?", filter.Exchange)
	}
	if filter.Market != "" {
		q = q.Where("c_market = ?", filter.Market)
	}
	if filter.DataType != "" {
		q = q.Where("c_data_type = ?", filter.DataType)
	}
	if filter.DatasetID != "" {
		q = q.Where("c_dataset_id = ?", filter.DatasetID)
	}
	if filter.SubjectID != "" {
		q = q.Where("c_subject_id = ?", filter.SubjectID)
	}
	if filter.Interval != "" {
		q = q.Where("c_interval = ?", filter.Interval)
	}
	if filter.PlannedExecNode != "" {
		q = q.Where("c_planned_exec_node = ?", filter.PlannedExecNode)
	}
	if filter.LastExecNode != "" {
		q = q.Where("c_last_exec_node = ?", filter.LastExecNode)
	}
	if filter.LastExecStatus != nil {
		q = q.Where("c_last_exec_status = ?", *filter.LastExecStatus)
	}
	if filter.Symbol != "" {
		q = q.Where("c_symbol LIKE ?", "%"+filter.Symbol+"%")
	}
	if !filter.IncludeDeleted {
		q = q.Where("c_is_deleted = ?", false)
	}
	return q
}

func normalizePage(page int, size int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = defaultPageSize
	}
	if size > maxPageSize {
		size = maxPageSize
	}
	return page, size
}

func normalizeJSON(raw string) string {
	if raw == "" {
		return "{}"
	}
	return raw
}
