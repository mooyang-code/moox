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

// CollectionTaskRepository persists collection tasks.
type CollectionTaskRepository struct {
	db *gorm.DB
}

// NewCollectionTaskRepository creates a repository.
func NewCollectionTaskRepository(db *gorm.DB) *CollectionTaskRepository {
	return &CollectionTaskRepository{db: db}
}

// List returns tasks matching filters.
func (r *CollectionTaskRepository) List(ctx context.Context, filter TaskFilter) ([]domain.CollectionTask, int64, error) {
	q := r.applyFilter(r.db.WithContext(ctx).Model(&domain.CollectionTask{}), filter)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, size := normalizePage(filter.Page, filter.PageSize)
	var rules []domain.CollectionTask
	if err := q.Order("c_id DESC").Limit(size).Offset((page - 1) * size).Find(&rules).Error; err != nil {
		return nil, 0, err
	}
	return rules, total, nil
}

// ListEnabled returns enabled tasks in one space.
func (r *CollectionTaskRepository) ListEnabled(ctx context.Context, spaceID string) ([]domain.CollectionTask, error) {
	var rules []domain.CollectionTask
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_enabled = ?", strings.TrimSpace(spaceID), true).
		Order("c_id ASC").
		Limit(MaxEnabledTasks + 1).
		Find(&rules).Error
	if err == nil && len(rules) > MaxEnabledTasks {
		return nil, fmt.Errorf("enabled task count exceeds limit %d", MaxEnabledTasks)
	}
	return rules, err
}

// ListEnabledAll returns the complete enabled task inventory. It fails rather
// than returning a truncated snapshot because observability reconciliation must
// never publish a partial expected set.
func (r *CollectionTaskRepository) ListEnabledAll(ctx context.Context, limit int) ([]domain.CollectionTask, error) {
	if limit <= 0 || limit > MaxEnabledTasks {
		return nil, fmt.Errorf("enabled task limit must be between 1 and %d", MaxEnabledTasks)
	}
	var rules []domain.CollectionTask
	if err := r.db.WithContext(ctx).
		Where("c_enabled = ?", true).
		Order("c_id ASC").
		Limit(limit + 1).
		Find(&rules).Error; err != nil {
		return nil, err
	}
	if len(rules) > limit {
		return nil, fmt.Errorf("enabled task count exceeds limit %d", limit)
	}
	return rules, nil
}

// GetByTaskID returns a task by its business id within a space.
func (r *CollectionTaskRepository) GetByTaskID(ctx context.Context, spaceID string, taskID string) (*domain.CollectionTask, error) {
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

// Create inserts a new collection task.
func (r *CollectionTaskRepository) Create(ctx context.Context, rule domain.CollectionTask) error {
	now := time.Now().UTC()
	if strings.TrimSpace(rule.TaskName) == "" {
		rule.TaskName = rule.TaskID
	}
	if rule.PrepareState == "" {
		rule.PrepareState = domain.PrepareStateReady
	}
	if err := applyCollectionTaskCoverageStart(&rule, now, rule.Enabled); err != nil {
		return err
	}
	rule.CreateTime = now
	rule.ModifyTime = now
	return r.db.WithContext(ctx).Create(&rule).Error
}

// ListResampleByPrepareStates returns bounded preparation work in stable order.
func (r *CollectionTaskRepository) ListResampleByPrepareStates(ctx context.Context, states []domain.CollectionTaskPrepareState, limit int) ([]domain.CollectionTask, error) {
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
	var rules []domain.CollectionTask
	err := r.db.WithContext(ctx).
		Where("c_data_type = ? AND c_enabled = ? AND c_prepare_state IN ?", "kline_resample", true, values).
		// Always service rules that are not ready before refreshing already-ready
		// catalogs. Otherwise a stable prefix of ready rules can starve new or
		// failed rules once the inventory exceeds the per-tick bound.
		Order("CASE WHEN c_prepare_state = 'ready' THEN 1 ELSE 0 END ASC, c_mtime ASC, c_id ASC").Limit(limit).Find(&rules).Error
	return rules, err
}

// SetPrepareState advances asynchronous target preparation without changing
// the immutable rule definition.
func (r *CollectionTaskRepository) SetPrepareState(ctx context.Context, spaceID, ruleID string, state domain.CollectionTaskPrepareState, lastError string) error {
	if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(ruleID) == "" {
		return fmt.Errorf("space_id and task_id are required")
	}
	if !state.Valid() {
		return fmt.Errorf("invalid collection task prepare state: %s", state)
	}
	result := r.db.WithContext(ctx).Model(&domain.CollectionTask{}).
		Where("c_space_id = ? AND c_task_id = ? AND c_data_type = ?", strings.TrimSpace(spaceID), strings.TrimSpace(ruleID), "kline_resample").
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
func (r *CollectionTaskRepository) UpdateByTaskID(ctx context.Context, spaceID string, ruleID string, rule domain.CollectionTask) (*domain.CollectionTask, error) {
	if strings.TrimSpace(rule.ResultDatasetID) == "" || strings.TrimSpace(rule.ResultViewID) == "" || strings.TrimSpace(rule.TaskName) == "" {
		existing, err := r.GetByTaskID(ctx, spaceID, ruleID)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(rule.ResultDatasetID) == "" {
			rule.ResultDatasetID = existing.ResultDatasetID
		}
		if strings.TrimSpace(rule.ResultViewID) == "" {
			rule.ResultViewID = existing.ResultViewID
		}
		if strings.TrimSpace(rule.TaskName) == "" {
			rule.TaskName = existing.TaskName
		}
	}
	now := time.Now().UTC()
	if err := applyCollectionTaskCoverageStart(&rule, now, rule.Enabled); err != nil {
		return nil, err
	}
	updates := map[string]any{
		"c_space_id":            rule.SpaceID,
		"c_task_name":           rule.TaskName,
		"c_description":         rule.Description,
		"c_data_type":           rule.DataType,
		"c_provider":            rule.Provider,
		"c_market_type":         rule.MarketType,
		"c_collect_params":      rule.CollectParams,
		"c_enabled":             rule.Enabled,
		"c_creator":             rule.Creator,
		"c_result_dataset_id":   rule.ResultDatasetID,
		"c_result_view_id":      rule.ResultViewID,
		"c_coverage_start_time": rule.CoverageStartTime,
		"c_mtime":               now,
	}
	q := r.db.WithContext(ctx).Model(&domain.CollectionTask{}).Where("c_task_id = ?", ruleID)
	if strings.TrimSpace(spaceID) != "" {
		q = q.Where("c_space_id = ?", strings.TrimSpace(spaceID))
	}
	if err := q.Updates(updates).Error; err != nil {
		return nil, err
	}
	return r.GetByTaskID(ctx, spaceID, ruleID)
}

// SetEnabled changes a task enabled flag.
func (r *CollectionTaskRepository) SetEnabled(ctx context.Context, spaceID string, ruleID string, enabled bool) error {
	updates := map[string]any{"c_enabled": enabled, "c_mtime": time.Now().UTC()}
	if enabled {
		rule, err := r.GetByTaskID(ctx, spaceID, ruleID)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		// Re-enabling is a new coverage decision. Do not preserve the old
		// timestamp from the previous enable period, otherwise live_only rules
		// silently replay stale history and lookback rules never move forward.
		rule.CoverageStartTime = nil
		if err := applyCollectionTaskCoverageStart(rule, now, true); err != nil {
			return err
		}
		updates["c_coverage_start_time"] = rule.CoverageStartTime
		updates["c_mtime"] = now
	}
	q := r.db.WithContext(ctx).Model(&domain.CollectionTask{}).Where("c_task_id = ?", ruleID)
	if strings.TrimSpace(spaceID) != "" {
		q = q.Where("c_space_id = ?", strings.TrimSpace(spaceID))
	}
	return q.Updates(updates).Error
}

func (r *CollectionTaskRepository) DeleteByTaskID(ctx context.Context, spaceID, taskID string) error {
	result := r.db.WithContext(ctx).Where("c_space_id = ? AND c_task_id = ?", strings.TrimSpace(spaceID), strings.TrimSpace(taskID)).Delete(&domain.CollectionTask{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *CollectionTaskRepository) applyFilter(q *gorm.DB, filter TaskFilter) *gorm.DB {
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

func applyCollectionTaskCoverageStart(rule *domain.CollectionTask, now time.Time, enabled bool) error {
	if rule == nil {
		return nil
	}
	if !enabled {
		rule.CoverageStartTime = nil
		return nil
	}
	start, err := resolveCollectionTaskCoverageStart(rule, now)
	if err != nil {
		return err
	}
	if start == nil {
		return nil
	}
	rule.CoverageStartTime = start
	return nil
}

func resolveCollectionTaskCoverageStart(rule *domain.CollectionTask, now time.Time) (*time.Time, error) {
	if rule == nil {
		return nil, nil
	}
	if rule.CoverageStartTime != nil && !rule.CoverageStartTime.IsZero() {
		at := rule.CoverageStartTime.UTC()
		return &at, nil
	}
	params, err := domain.ParseCollectParams(rule.CollectParams, rule.Provider, rule.MarketType, rule.DataType)
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
		if isStockCNRule(rule) {
			calendar, calendarErr := loadStockCNCalendarForRule()
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

func isStockCNRule(rule *domain.CollectionTask) bool {
	if rule == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(rule.SpaceID), "stockcn") || strings.EqualFold(strings.TrimSpace(rule.MarketType), "equity")
}

func loadStockCNCalendarForRule() (*stockmarket.Calendar, error) {
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
