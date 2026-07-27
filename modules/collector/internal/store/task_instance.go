package store

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
	SpaceID        string
	TaskID         string
	RuleID         string
	Exchange       string
	Market         string
	DataType       string
	DatasetID      string
	SubjectID      string
	Interval       string
	LastExecNode   string
	LastExecStatus *int
	Symbol         string
	IncludeDeleted bool
	Page           int
	PageSize       int
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
	_, err := r.ReserveMany(ctx, instances)
	return err
}

// ReserveMany persists schedulable instances and returns the rows whose job item
// remains current. A pending job cannot be replaced by the next schedule window.
func (r *TaskInstanceRepository) ReserveMany(
	ctx context.Context,
	instances []domain.TaskInstance,
) ([]domain.TaskInstance, error) {
	if len(instances) == 0 {
		return nil, nil
	}
	now := time.Now().UTC()
	for i := range instances {
		if instances[i].CreateTime.IsZero() {
			instances[i].CreateTime = now
		}
		instances[i].ModifyTime = now
	}
	reserved := make([]domain.TaskInstance, 0, len(instances))
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for i := range instances {
			result := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{
					{Name: "c_space_id"},
					{Name: "c_task_id"},
				},
				DoUpdates: clause.Assignments(map[string]any{
					"c_cloud_job_item_id": clause.Expr{SQL: "excluded.c_cloud_job_item_id"},
					"c_rule_id":           clause.Expr{SQL: "excluded.c_rule_id"},
					"c_exchange":          clause.Expr{SQL: "excluded.c_exchange"},
					"c_market":            clause.Expr{SQL: "excluded.c_market"},
					"c_data_type":         clause.Expr{SQL: "excluded.c_data_type"},
					"c_dataset_id":        clause.Expr{SQL: "excluded.c_dataset_id"},
					"c_subject_id":        clause.Expr{SQL: "excluded.c_subject_id"},
					"c_symbol":            clause.Expr{SQL: "excluded.c_symbol"},
					"c_interval":          clause.Expr{SQL: "excluded.c_interval"},
					"c_task_params":       clause.Expr{SQL: "excluded.c_task_params"},
					"c_is_deleted":        clause.Expr{SQL: "excluded.c_is_deleted"},
					"c_mtime":             clause.Expr{SQL: "excluded.c_mtime"},
					"c_last_exec_node": clause.Expr{
						SQL: "CASE WHEN c_cloud_job_item_id <> excluded.c_cloud_job_item_id THEN '' ELSE c_last_exec_node END",
					},
					"c_last_exec_status": clause.Expr{
						SQL:  "CASE WHEN c_cloud_job_item_id <> excluded.c_cloud_job_item_id THEN ? ELSE c_last_exec_status END",
						Vars: []any{domain.InstanceStatusPending},
					},
					"c_last_exec_time": clause.Expr{
						SQL: "CASE WHEN c_cloud_job_item_id <> excluded.c_cloud_job_item_id THEN NULL ELSE c_last_exec_time END",
					},
					"c_result": clause.Expr{
						SQL: "CASE WHEN c_cloud_job_item_id <> excluded.c_cloud_job_item_id THEN '{}' ELSE c_result END",
					},
				}),
				Where: clause.Where{Exprs: []clause.Expression{clause.Expr{
					SQL: "c_cloud_job_item_id = excluded.c_cloud_job_item_id OR c_last_exec_status IN (?, ?)",
					Vars: []any{
						domain.InstanceStatusSuccess,
						domain.InstanceStatusFailed,
					},
				}}},
			}).Create(&instances[i])
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected > 0 {
				reserved = append(reserved, instances[i])
				continue
			}
			// A previous publish may have failed after this row was reserved, or
			// its response may have been lost. Resubmit the current deterministic
			// JobItem ID; CloudNode deduplicates it and the pending fence still
			// prevents rolling to a newer window.
			var current domain.TaskInstance
			if err := tx.Where(
				"c_space_id = ? AND c_task_id = ?",
				instances[i].SpaceID,
				instances[i].TaskID,
			).First(&current).Error; err != nil {
				return err
			}
			if current.LastExecStatus == domain.InstanceStatusPending {
				current.ExecuteAt = scheduledExecuteAt(current.CloudJobItemID)
				reserved = append(reserved, current)
			}
		}
		return nil
	})
	return reserved, err
}

func scheduledExecuteAt(jobItemID string) time.Time {
	const encodedLen = len("2006-01-02T15:04:05Z")
	if len(jobItemID) < encodedLen {
		return time.Time{}
	}
	value := jobItemID[len(jobItemID)-encodedLen:]
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

// UpdateStatus updates a task instance only when the report belongs to its current cloud job item.
func (r *TaskInstanceRepository) UpdateStatus(
	ctx context.Context,
	spaceID string,
	taskID string,
	jobItemID string,
	nodeID string,
	status int,
	result string,
) (bool, error) {
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
		Where(
			"c_space_id = ? AND c_task_id = ? AND c_cloud_job_item_id = ?",
			spaceID,
			taskID,
			jobItemID,
		).
		Updates(updates)
	if tx.Error != nil {
		return false, tx.Error
	}
	return tx.RowsAffected > 0, nil
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
