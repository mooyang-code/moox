// Package store contains Collector persistence adapters.
package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"gorm.io/gorm"
)

const MaxEnabledTaskRules = 1000

// TaskRuleFilter describes rule list filters.
type TaskRuleFilter struct {
	SpaceID    string
	DataType   string
	Provider   string
	MarketType string
	Enabled    *bool
	RuleID     string
	Page       int
	PageSize   int
}

// TaskRuleRepository persists collection rules.
type TaskRuleRepository struct {
	db *gorm.DB
}

// NewTaskRuleRepository creates a repository.
func NewTaskRuleRepository(db *gorm.DB) *TaskRuleRepository {
	return &TaskRuleRepository{db: db}
}

// List returns rules matching filters.
func (r *TaskRuleRepository) List(ctx context.Context, filter TaskRuleFilter) ([]domain.TaskRule, int64, error) {
	q := r.applyFilter(r.db.WithContext(ctx).Model(&domain.TaskRule{}), filter)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, size := normalizePage(filter.Page, filter.PageSize)
	var rules []domain.TaskRule
	if err := q.Order("c_id DESC").Limit(size).Offset((page - 1) * size).Find(&rules).Error; err != nil {
		return nil, 0, err
	}
	return rules, total, nil
}

// ListEnabled returns enabled rules in one space.
func (r *TaskRuleRepository) ListEnabled(ctx context.Context, spaceID string) ([]domain.TaskRule, error) {
	var rules []domain.TaskRule
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_enabled = ?", strings.TrimSpace(spaceID), true).
		Order("c_id ASC").
		Limit(MaxEnabledTaskRules + 1).
		Find(&rules).Error
	if err == nil && len(rules) > MaxEnabledTaskRules {
		return nil, fmt.Errorf("enabled task rule count exceeds limit %d", MaxEnabledTaskRules)
	}
	return rules, err
}

// ListEnabledAll returns the complete enabled rule inventory. It fails rather
// than returning a truncated snapshot because observability reconciliation must
// never publish a partial expected set.
func (r *TaskRuleRepository) ListEnabledAll(ctx context.Context, limit int) ([]domain.TaskRule, error) {
	if limit <= 0 || limit > MaxEnabledTaskRules {
		return nil, fmt.Errorf("enabled task rule limit must be between 1 and %d", MaxEnabledTaskRules)
	}
	var rules []domain.TaskRule
	if err := r.db.WithContext(ctx).
		Where("c_enabled = ?", true).
		Order("c_id ASC").
		Limit(limit + 1).
		Find(&rules).Error; err != nil {
		return nil, err
	}
	if len(rules) > limit {
		return nil, fmt.Errorf("enabled task rule count exceeds limit %d", limit)
	}
	return rules, nil
}

// GetByRuleID returns a rule by its business id within a space.
func (r *TaskRuleRepository) GetByRuleID(ctx context.Context, spaceID string, ruleID string) (*domain.TaskRule, error) {
	var rule domain.TaskRule
	q := r.db.WithContext(ctx).Where("c_rule_id = ?", ruleID)
	if strings.TrimSpace(spaceID) != "" {
		q = q.Where("c_space_id = ?", strings.TrimSpace(spaceID))
	}
	if err := q.First(&rule).Error; err != nil {
		return nil, err
	}
	return &rule, nil
}

// Create inserts a new collector rule.
func (r *TaskRuleRepository) Create(ctx context.Context, rule domain.TaskRule) error {
	now := time.Now().UTC()
	if rule.PrepareState == "" {
		rule.PrepareState = domain.PrepareStateReady
	}
	rule.CreateTime = now
	rule.ModifyTime = now
	return r.db.WithContext(ctx).Create(&rule).Error
}

// ListResampleByPrepareStates returns bounded preparation work in stable order.
func (r *TaskRuleRepository) ListResampleByPrepareStates(ctx context.Context, states []domain.TaskRulePrepareState, limit int) ([]domain.TaskRule, error) {
	if limit <= 0 || limit > MaxEnabledTaskRules {
		return nil, fmt.Errorf("resample prepare rule limit must be between 1 and %d", MaxEnabledTaskRules)
	}
	values := make([]string, 0, len(states))
	for _, state := range states {
		if !state.Valid() {
			return nil, fmt.Errorf("invalid task rule prepare state: %s", state)
		}
		values = append(values, string(state))
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("at least one task rule prepare state is required")
	}
	var rules []domain.TaskRule
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
func (r *TaskRuleRepository) SetPrepareState(ctx context.Context, spaceID, ruleID string, state domain.TaskRulePrepareState, lastError string) error {
	if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(ruleID) == "" {
		return fmt.Errorf("space_id and rule_id are required")
	}
	if !state.Valid() {
		return fmt.Errorf("invalid task rule prepare state: %s", state)
	}
	result := r.db.WithContext(ctx).Model(&domain.TaskRule{}).
		Where("c_space_id = ? AND c_rule_id = ? AND c_data_type = ?", strings.TrimSpace(spaceID), strings.TrimSpace(ruleID), "kline_resample").
		Updates(map[string]any{"c_prepare_state": state, "c_last_error": strings.TrimSpace(lastError), "c_mtime": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// UpdateByRuleID updates an existing collector rule.
func (r *TaskRuleRepository) UpdateByRuleID(ctx context.Context, spaceID string, ruleID string, rule domain.TaskRule) (*domain.TaskRule, error) {
	updates := map[string]any{
		"c_space_id":       rule.SpaceID,
		"c_data_type":      rule.DataType,
		"c_provider":       rule.Provider,
		"c_market_type":    rule.MarketType,
		"c_collect_params": rule.CollectParams,
		"c_enabled":        rule.Enabled,
		"c_creator":        rule.Creator,
		"c_mtime":          time.Now().UTC(),
	}
	q := r.db.WithContext(ctx).Model(&domain.TaskRule{}).Where("c_rule_id = ?", ruleID)
	if strings.TrimSpace(spaceID) != "" {
		q = q.Where("c_space_id = ?", strings.TrimSpace(spaceID))
	}
	if err := q.Updates(updates).Error; err != nil {
		return nil, err
	}
	return r.GetByRuleID(ctx, spaceID, ruleID)
}

// SetEnabled changes a rule enabled flag.
func (r *TaskRuleRepository) SetEnabled(ctx context.Context, spaceID string, ruleID string, enabled bool) error {
	q := r.db.WithContext(ctx).Model(&domain.TaskRule{}).Where("c_rule_id = ?", ruleID)
	if strings.TrimSpace(spaceID) != "" {
		q = q.Where("c_space_id = ?", strings.TrimSpace(spaceID))
	}
	return q.Updates(map[string]any{"c_enabled": enabled, "c_mtime": time.Now().UTC()}).Error
}

func (r *TaskRuleRepository) applyFilter(q *gorm.DB, filter TaskRuleFilter) *gorm.DB {
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
	if v := strings.TrimSpace(filter.RuleID); v != "" {
		q = q.Where("c_rule_id = ?", v)
	}
	return q
}
