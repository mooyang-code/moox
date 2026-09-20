// Package store contains Collector persistence adapters.
package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	stockmarket "github.com/mooyang-code/moox/modules/collector/internal/markets/stockcn"
	"gorm.io/gorm"
)

const MaxEnabledTasks = 1000

// TaskFilter describes collection task list filters.
type TaskFilter struct {
	SpaceID    string
	DataType   string
	Provider   string
	MarketType string
	Enabled    *bool
	TaskID     string
	Page       int
	PageSize   int
}

// TaskRepository persists collection tasks.
type TaskRepository struct {
	db *gorm.DB
}

// NewTaskRepository creates a repository.
func NewTaskRepository(db *gorm.DB) *TaskRepository {
	return &TaskRepository{db: db}
}

// List returns tasks matching filters.
func (r *TaskRepository) List(ctx context.Context, filter TaskFilter) ([]domain.CollectionTask, int64, error) {
	q := r.applyFilter(r.db.WithContext(ctx).Model(&domain.CollectionTask{}), filter)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, size := normalizePage(filter.Page, filter.PageSize)
	var tasks []domain.CollectionTask
	if err := q.Order("c_id DESC").Limit(size).Offset((page - 1) * size).Find(&tasks).Error; err != nil {
		return nil, 0, err
	}
	return tasks, total, nil
}

// ListEnabled returns enabled tasks in one space.
func (r *TaskRepository) ListEnabled(ctx context.Context, spaceID string) ([]domain.CollectionTask, error) {
	var tasks []domain.CollectionTask
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_enabled = ?", strings.TrimSpace(spaceID), true).
		Order("c_id ASC").
		Limit(MaxEnabledTasks + 1).
		Find(&tasks).Error
	if err == nil && len(tasks) > MaxEnabledTasks {
		return nil, fmt.Errorf("enabled task count exceeds limit %d", MaxEnabledTasks)
	}
	return tasks, err
}

// ListEnabledAll returns the complete enabled task inventory. It fails rather
// than returning a truncated snapshot because observability reconciliation must
// never publish a partial expected set.
func (r *TaskRepository) ListEnabledAll(ctx context.Context, limit int) ([]domain.CollectionTask, error) {
	if limit <= 0 || limit > MaxEnabledTasks {
		return nil, fmt.Errorf("enabled task limit must be between 1 and %d", MaxEnabledTasks)
	}
	var tasks []domain.CollectionTask
	if err := r.db.WithContext(ctx).
		Where("c_enabled = ?", true).
		Order("c_id ASC").
		Limit(limit + 1).
		Find(&tasks).Error; err != nil {
		return nil, err
	}
	if len(tasks) > limit {
		return nil, fmt.Errorf("enabled task count exceeds limit %d", limit)
	}
	return tasks, nil
}

// GetByTaskID returns a task by its business id within a space.
func (r *TaskRepository) GetByTaskID(ctx context.Context, spaceID string, taskID string) (*domain.CollectionTask, error) {
	var task domain.CollectionTask
	q := r.db.WithContext(ctx).Where("c_task_id = ?", taskID)
	if strings.TrimSpace(spaceID) != "" {
		q = q.Where("c_space_id = ?", strings.TrimSpace(spaceID))
	}
	if err := q.First(&task).Error; err != nil {
		return nil, err
	}
	return &task, nil
}

// GetByTaskName returns a task by its display name within a space.
func (r *TaskRepository) GetByTaskName(ctx context.Context, spaceID string, taskName string) (*domain.CollectionTask, error) {
	var task domain.CollectionTask
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_task_name = ?", strings.TrimSpace(spaceID), domain.NormalizeCollectionTaskName(taskName)).
		First(&task).Error
	if err != nil {
		return nil, err
	}
	return &task, nil
}

// Create inserts a new collection task.
func (r *TaskRepository) Create(ctx context.Context, task domain.CollectionTask) error {
	now := time.Now().UTC()
	task.TaskName = domain.NormalizeCollectionTaskName(task.TaskName)
	if task.TaskName != "" {
		if err := domain.ValidateCollectionTaskName(task.TaskName); err != nil {
			return err
		}
	}
	if task.TaskName == "" {
		task.TaskName = task.TaskID
	}
	if task.PrepareState == "" {
		task.PrepareState = domain.PrepareStateReady
	}
	if err := applyCollectionTaskCoverageStart(&task, now, task.Enabled); err != nil {
		return err
	}
	task.CreateTime = now
	task.ModifyTime = now
	return r.db.WithContext(ctx).Create(&task).Error
}

// ListResampleByPrepareStates returns bounded preparation work in stable order.
func (r *TaskRepository) ListResampleByPrepareStates(ctx context.Context, states []domain.CollectionTaskPrepareState, limit int) ([]domain.CollectionTask, error) {
	if limit <= 0 || limit > MaxEnabledTasks {
		return nil, fmt.Errorf("resample prepare task limit must be between 1 and %d", MaxEnabledTasks)
	}
	values := make([]string, 0, len(states))
	for _, state := range states {
		if !state.Valid() {
			return nil, fmt.Errorf("invalid task prepare state: %s", state)
		}
		values = append(values, string(state))
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("at least one task prepare state is required")
	}
	var tasks []domain.CollectionTask
	err := r.db.WithContext(ctx).
		Where("c_data_type = ? AND c_enabled = ? AND c_prepare_state IN ?", "kline_resample", true, values).
		// Always service tasks that are not ready before refreshing already-ready
		// catalogs. Otherwise a stable prefix of ready tasks can starve new or
		// failed tasks once the inventory exceeds the per-tick bound.
		Order("CASE WHEN c_prepare_state = 'ready' THEN 1 ELSE 0 END ASC, c_mtime ASC, c_id ASC").Limit(limit).Find(&tasks).Error
	return tasks, err
}

// SetPrepareState advances asynchronous target preparation without changing
// the immutable task definition.
func (r *TaskRepository) SetPrepareState(ctx context.Context, spaceID, taskID string, state domain.CollectionTaskPrepareState, lastError string) error {
	if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(taskID) == "" {
		return fmt.Errorf("space_id and task_id are required")
	}
	if !state.Valid() {
		return fmt.Errorf("invalid collection task prepare state: %s", state)
	}
	result := r.db.WithContext(ctx).Model(&domain.CollectionTask{}).
		Where("c_space_id = ? AND c_task_id = ? AND c_data_type = ?", strings.TrimSpace(spaceID), strings.TrimSpace(taskID), "kline_resample").
		Updates(map[string]any{"c_prepare_state": state, "c_last_error": strings.TrimSpace(lastError), "c_mtime": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// UpdateByTaskID updates an existing collection task.
func (r *TaskRepository) UpdateByTaskID(ctx context.Context, spaceID string, taskID string, task domain.CollectionTask) (*domain.CollectionTask, error) {
	task.TaskName = domain.NormalizeCollectionTaskName(task.TaskName)
	if task.TaskName != "" {
		if err := domain.ValidateCollectionTaskName(task.TaskName); err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(task.ResultDatasetID) == "" || strings.TrimSpace(task.ResultViewID) == "" || strings.TrimSpace(task.TaskName) == "" {
		existing, err := r.GetByTaskID(ctx, spaceID, taskID)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(task.ResultDatasetID) == "" {
			task.ResultDatasetID = existing.ResultDatasetID
		}
		if strings.TrimSpace(task.ResultViewID) == "" {
			task.ResultViewID = existing.ResultViewID
		}
		if strings.TrimSpace(task.TaskName) == "" {
			task.TaskName = existing.TaskName
		}
	}
	now := time.Now().UTC()
	if err := applyCollectionTaskCoverageStart(&task, now, task.Enabled); err != nil {
		return nil, err
	}
	updates := map[string]any{
		"c_space_id":            task.SpaceID,
		"c_task_name":           task.TaskName,
		"c_description":         task.Description,
		"c_data_type":           task.DataType,
		"c_provider":            task.Provider,
		"c_market_type":         task.MarketType,
		"c_collect_params":      task.CollectParams,
		"c_enabled":             task.Enabled,
		"c_creator":             task.Creator,
		"c_result_dataset_id":   task.ResultDatasetID,
		"c_result_view_id":      task.ResultViewID,
		"c_coverage_start_time": task.CoverageStartTime,
		"c_mtime":               now,
	}
	q := r.db.WithContext(ctx).Model(&domain.CollectionTask{}).Where("c_task_id = ?", taskID)
	if strings.TrimSpace(spaceID) != "" {
		q = q.Where("c_space_id = ?", strings.TrimSpace(spaceID))
	}
	if err := q.Updates(updates).Error; err != nil {
		return nil, err
	}
	return r.GetByTaskID(ctx, spaceID, taskID)
}

// UpdateMutableByTaskID updates only the public mutable task fields and
// Collector-owned runtime state. Task identity, collection configuration, and
// result identities are intentionally absent from the update set.
func (r *TaskRepository) UpdateMutableByTaskID(ctx context.Context, spaceID, taskID string, task domain.CollectionTask) (*domain.CollectionTask, error) {
	task.TaskName = domain.NormalizeCollectionTaskName(task.TaskName)
	if err := domain.ValidateCollectionTaskName(task.TaskName); err != nil {
		return nil, err
	}
	if !domain.CollectionTaskPrepareState(task.PrepareState).Valid() {
		return nil, fmt.Errorf("invalid collection task prepare state: %s", task.PrepareState)
	}
	if !task.Enabled {
		task.CoverageStartTime = nil
	}
	now := time.Now().UTC()
	result := r.db.WithContext(ctx).Model(&domain.CollectionTask{}).
		Where("c_space_id = ? AND c_task_id = ?", strings.TrimSpace(spaceID), strings.TrimSpace(taskID)).
		Updates(map[string]any{
			"c_task_name":           task.TaskName,
			"c_description":         task.Description,
			"c_enabled":             task.Enabled,
			"c_prepare_state":       task.PrepareState,
			"c_last_error":          task.LastError,
			"c_coverage_start_time": task.CoverageStartTime,
			"c_mtime":               now,
		})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return r.GetByTaskID(ctx, spaceID, taskID)
}

// SetEnabled changes a task enabled flag.
func (r *TaskRepository) SetEnabled(ctx context.Context, spaceID, taskID string, enabled bool) error {
	updates := map[string]any{"c_enabled": enabled, "c_mtime": time.Now().UTC()}
	if enabled {
		task, err := r.GetByTaskID(ctx, spaceID, taskID)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		// Re-enabling is a new coverage decision. Do not preserve the old
		// timestamp from the previous enable period, otherwise live_only rules
		// silently replay stale history and lookback rules never move forward.
		task.CoverageStartTime = nil
		if err := applyCollectionTaskCoverageStart(task, now, true); err != nil {
			return err
		}
		updates["c_coverage_start_time"] = task.CoverageStartTime
		updates["c_mtime"] = now
	}
	q := r.db.WithContext(ctx).Model(&domain.CollectionTask{}).Where("c_task_id = ?", taskID)
	if strings.TrimSpace(spaceID) != "" {
		q = q.Where("c_space_id = ?", strings.TrimSpace(spaceID))
	}
	return q.Updates(updates).Error
}

func (r *TaskRepository) DeleteByTaskID(ctx context.Context, spaceID, taskID string) error {
	result := r.db.WithContext(ctx).Where("c_space_id = ? AND c_task_id = ?", strings.TrimSpace(spaceID), strings.TrimSpace(taskID)).Delete(&domain.CollectionTask{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *TaskRepository) applyFilter(q *gorm.DB, filter TaskFilter) *gorm.DB {
	if v := strings.TrimSpace(filter.SpaceID); v != "" {
		q = q.Where("c_space_id = ?", v)
	}
	if v := strings.TrimSpace(filter.DataType); v != "" {
		q = q.Where("c_data_type = ?", v)
	}
	if v := strings.TrimSpace(filter.Provider); v != "" {
		q = q.Where("c_provider = ?", v)
	}
	if v := strings.TrimSpace(filter.MarketType); v != "" {
		q = q.Where("c_market_type = ?", v)
	}
	if filter.Enabled != nil {
		q = q.Where("c_enabled = ?", *filter.Enabled)
	}
	if v := strings.TrimSpace(filter.TaskID); v != "" {
		q = q.Where("c_task_id = ?", v)
	}
	return q
}

func applyCollectionTaskCoverageStart(task *domain.CollectionTask, now time.Time, enabled bool) error {
	if task == nil {
		return nil
	}
	if !enabled {
		task.CoverageStartTime = nil
		return nil
	}
	start, err := resolveCollectionTaskCoverageStart(task, now)
	if err != nil {
		return err
	}
	if start == nil {
		return nil
	}
	task.CoverageStartTime = start
	return nil
}

func resolveCollectionTaskCoverageStart(task *domain.CollectionTask, now time.Time) (*time.Time, error) {
	if task == nil {
		return nil, nil
	}
	if task.CoverageStartTime != nil && !task.CoverageStartTime.IsZero() {
		at := task.CoverageStartTime.UTC()
		return &at, nil
	}
	params, err := domain.ParseCollectParams(task.CollectParams, task.Provider, task.MarketType, task.DataType)
	if err != nil || params == nil || params.HistoryPolicy == nil {
		at := now.UTC().Truncate(time.Minute)
		return &at, nil
	}
	policy := params.HistoryPolicy
	var start time.Time
	switch policy.Mode {
	case domain.HistoryModeSince:
		at, parseErr := time.Parse(time.RFC3339Nano, policy.Since)
		if parseErr != nil {
			at = now
		}
		start = at.UTC()
	case domain.HistoryModeLookback:
		if isStockCNTask(task) {
			calendar, calendarErr := loadStockCNCalendarForTask()
			if calendarErr != nil {
				return nil, calendarErr
			}
			start, err = calendar.LookbackStart(now.UTC(), policy.Lookback)
			if err != nil {
				return nil, fmt.Errorf("resolve stockcn history lookback: %w", err)
			}
		} else {
			start = now.UTC().Add(-time.Duration(policy.Lookback) * 24 * time.Hour).Truncate(time.Minute)
		}
	default:
		start = now.UTC().Truncate(time.Minute)
	}
	return &start, nil
}

func isStockCNTask(task *domain.CollectionTask) bool {
	if task == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(task.SpaceID), "stockcn") || strings.EqualFold(strings.TrimSpace(task.MarketType), "equity")
}

func loadStockCNCalendarForTask() (*stockmarket.Calendar, error) {
	_, sourceFile, _, _ := runtime.Caller(0)
	sourceRelative := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "config", "markets", "stockcn", "calendar.yaml"))
	candidates := []string{
		strings.TrimSpace(os.Getenv("MOOX_STOCK_CN_CALENDAR_PATH")),
		"markets/stockcn/calendar.yaml",
		"config/markets/stockcn/calendar.yaml",
		"modules/collector/config/markets/stockcn/calendar.yaml",
		sourceRelative,
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		candidate = filepath.Clean(candidate)
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		calendar, err := stockmarket.LoadCalendar(candidate)
		if err != nil {
			return nil, fmt.Errorf("load stockcn calendar %s: %w", candidate, err)
		}
		return calendar, nil
	}
	return nil, fmt.Errorf("stockcn calendar config was not found")
}
