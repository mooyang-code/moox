package store

import (
	"context"
	"strings"
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
	Provider       string
	MarketType     string
	DataType       string
	DatasetID      string
	SubjectID      string
	Frequency      string
	LastExecNode   string
	LastExecStatus *int
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

// Get returns the current task instance by its stable identity.
func (r *TaskInstanceRepository) Get(ctx context.Context, spaceID, taskID string) (domain.TaskInstance, error) {
	var instance domain.TaskInstance
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_task_id = ?", spaceID, taskID).
		First(&instance).Error
	return instance, err
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

// UpsertMany creates or updates stable business instances. A periodic batch
// never changes the instance identity or resets its freshness state.
func (r *TaskInstanceRepository) UpsertMany(ctx context.Context, instances []domain.TaskInstance) error {
	if len(instances) == 0 {
		return nil
	}
	now := time.Now().UTC()
	for i := range instances {
		if instances[i].CreateTime.IsZero() {
			instances[i].CreateTime = now
		}
		if instances[i].LastExecStatus == 0 {
			instances[i].LastExecStatus = domain.InstanceStatusPending
		}
		instances[i].ModifyTime = now
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "c_space_id"}, {Name: "c_task_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"c_rule_id":     clause.Expr{SQL: "excluded.c_rule_id"},
			"c_provider":    clause.Expr{SQL: "excluded.c_provider"},
			"c_market_type": clause.Expr{SQL: "excluded.c_market_type"},
			"c_data_type":   clause.Expr{SQL: "excluded.c_data_type"},
			"c_dataset_id":  clause.Expr{SQL: "excluded.c_dataset_id"},
			"c_subject_id":  clause.Expr{SQL: "excluded.c_subject_id"},
			"c_frequency":   clause.Expr{SQL: "excluded.c_frequency"},
			"c_task_params": clause.Expr{SQL: "excluded.c_task_params"},
			"c_is_deleted":  clause.Expr{SQL: "excluded.c_is_deleted"},
			"c_mtime":       clause.Expr{SQL: "excluded.c_mtime"},
		}),
	}).Create(&instances).Error
}

// DeactivateMissingMarketFetchRuleInstances prevents the gap auditor from
// reviving symbols or frequencies removed from an enabled market rule.
func (r *TaskInstanceRepository) DeactivateMissingMarketFetchRuleInstances(ctx context.Context, spaceID, ruleID string, activeTaskIDs []string) error {
	query := r.db.WithContext(ctx).Model(&domain.TaskInstance{}).
		Where("c_space_id = ? AND c_rule_id = ? AND c_is_deleted = ?", spaceID, ruleID, false)
	if len(activeTaskIDs) > 0 {
		query = query.Where("c_task_id NOT IN ?", activeTaskIDs)
	}
	return query.Updates(map[string]any{"c_is_deleted": true, "c_mtime": time.Now().UTC()}).Error
}

func (r *TaskInstanceRepository) ListStale(ctx context.Context, spaceID string, before time.Time, limit int) ([]domain.TaskInstance, error) {
	if limit <= 0 {
		limit = 100
	}
	var instances []domain.TaskInstance
	query := r.db.WithContext(ctx).Where("c_is_deleted = ? AND (c_last_exec_time IS NULL OR c_last_exec_time < ?)", false, before.UTC())
	if strings.TrimSpace(spaceID) != "" {
		query = query.Where("c_space_id = ?", spaceID)
	}
	err := query.Order("c_last_exec_time ASC").Limit(limit).Find(&instances).Error
	return instances, err
}

// ListAll returns the enabled stable instances in a bounded deterministic
// order. Gap auditing must inspect the Storage watermark even when the last
// invocation was recent, so a last_exec_time cutoff would starve slower
// frequencies (for example 1h) behind the same few stale rows.
func (r *TaskInstanceRepository) ListAll(ctx context.Context, spaceID string, limit int) ([]domain.TaskInstance, error) {
	if limit <= 0 || limit > maxPageSize {
		limit = maxPageSize
	}
	query := r.db.WithContext(ctx).Where("c_is_deleted = ?", false)
	if strings.TrimSpace(spaceID) != "" {
		query = query.Where("c_space_id = ?", spaceID)
	}
	var instances []domain.TaskInstance
	err := query.Order("c_id ASC").Limit(limit).Find(&instances).Error
	return instances, err
}

// ListAfterID returns a bounded page after the audit cursor. The cursor keeps
// gap auditing fair when the task table grows beyond one page.
func (r *TaskInstanceRepository) ListAfterID(ctx context.Context, spaceID string, afterID, limit int) ([]domain.TaskInstance, error) {
	if limit <= 0 || limit > maxPageSize {
		limit = maxPageSize
	}
	query := r.db.WithContext(ctx).Where("c_is_deleted = ?", false)
	if strings.TrimSpace(spaceID) != "" {
		query = query.Where("c_space_id = ?", spaceID)
	}
	if afterID > 0 {
		query = query.Where("c_id > ?", afterID)
	}
	var instances []domain.TaskInstance
	err := query.Order("c_id ASC").Limit(limit).Find(&instances).Error
	return instances, err
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
	if filter.Provider != "" {
		q = q.Where("c_provider = ?", filter.Provider)
	}
	if filter.MarketType != "" {
		q = q.Where("c_market_type = ?", filter.MarketType)
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
	if filter.Frequency != "" {
		q = q.Where("c_frequency = ?", filter.Frequency)
	}
	if filter.LastExecNode != "" {
		q = q.Where("c_last_exec_node = ?", filter.LastExecNode)
	}
	if filter.LastExecStatus != nil {
		q = q.Where("c_last_exec_status = ?", *filter.LastExecStatus)
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
