package store

import (
	"context"
	"encoding/json"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func ruleFromRow(row ruleRow) (Rule, error) {
	rule := Rule{
		RuleID: row.RuleID, Name: row.Name, EventName: row.EventName, ActionType: row.ActionType,
		NotifyURL: row.NotifyURL, AllowPrivate: row.AllowPrivate, Secret: row.Secret,
		SystemAction: row.SystemAction, System: row.System, Enabled: row.Enabled,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if err := json.Unmarshal([]byte(row.ScopeJSON), &rule.Scope); err != nil {
		return Rule{}, fmt.Errorf("规则 %s 的限定范围已损坏: %w", row.RuleID, err)
	}
	return rule, nil
}

func ruleToRow(rule Rule) (ruleRow, error) {
	scope := rule.Scope
	if scope == nil {
		scope = []ScopeFilter{}
	}
	raw, err := json.Marshal(scope)
	if err != nil {
		return ruleRow{}, err
	}
	return ruleRow{
		RuleID: rule.RuleID, Name: rule.Name, EventName: rule.EventName, ScopeJSON: string(raw), ActionType: rule.ActionType,
		NotifyURL: rule.NotifyURL, AllowPrivate: rule.AllowPrivate, Secret: rule.Secret,
		SystemAction: rule.SystemAction, System: rule.System, Enabled: rule.Enabled,
		CreatedAt: rule.CreatedAt, UpdatedAt: rule.UpdatedAt,
	}, nil
}

func rulesFromRows(rows []ruleRow) ([]Rule, error) {
	rules := make([]Rule, 0, len(rows))
	for _, row := range rows {
		rule, err := ruleFromRow(row)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

// CreateRule 新建规则。
func (s *Store) CreateRule(ctx context.Context, rule Rule) error {
	row, err := ruleToRow(rule)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Create(&row).Error
}

// EnsureSystemRule 保证服务自带的系统规则存在：已经存在（按系统动作判断，包括用户停用过的）就不动它。
func (s *Store) EnsureSystemRule(ctx context.Context, rule Rule) error {
	row, err := ruleToRow(rule)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
}

// GetRule 按 ID 取规则。
func (s *Store) GetRule(ctx context.Context, ruleID string) (Rule, error) {
	var row ruleRow
	if err := s.db.WithContext(ctx).Where("c_rule_id = ?", ruleID).Take(&row).Error; err != nil {
		return Rule{}, notFound(err)
	}
	return ruleFromRow(row)
}

// ListRules 按创建时间列出规则（系统规则在前），返回总数。
func (s *Store) ListRules(ctx context.Context, offset, limit int) ([]Rule, int64, error) {
	var total int64
	if err := s.db.WithContext(ctx).Model(&ruleRow{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []ruleRow
	if err := s.db.WithContext(ctx).Order("c_system DESC, c_created_at ASC, c_rule_id ASC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	rules, err := rulesFromRows(rows)
	return rules, total, err
}

// ListEnabledRules 返回监听某个事件的全部启用规则。
func (s *Store) ListEnabledRules(ctx context.Context, eventName string) ([]Rule, error) {
	var rows []ruleRow
	if err := s.db.WithContext(ctx).Where("c_event_name = ? AND c_enabled = 1", eventName).Order("c_created_at ASC, c_rule_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rulesFromRows(rows)
}

// ListDisabledSystemRules 返回触发这个事件的、已停用的系统动作规则。停用只影响新的下架：已经在处理的记录照常处理，
// 也要照常合并改期，所以接收端要用它找到“只合并、不新建”的规则。
func (s *Store) ListDisabledSystemRules(ctx context.Context, eventName string) ([]Rule, error) {
	var rows []ruleRow
	if err := s.db.WithContext(ctx).Where("c_event_name = ? AND c_system = 1 AND c_enabled = 0", eventName).Order("c_created_at ASC, c_rule_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rulesFromRows(rows)
}

// UpdateRule 在事务里读出规则、交给 mutate 修改、再写回；mutate 返回错误则不写。
func (s *Store) UpdateRule(ctx context.Context, ruleID string, now int64, mutate func(*Rule) error) (Rule, error) {
	var updated Rule
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row ruleRow
		if err := tx.Where("c_rule_id = ?", ruleID).Take(&row).Error; err != nil {
			return notFound(err)
		}
		rule, err := ruleFromRow(row)
		if err != nil {
			return err
		}
		if err := mutate(&rule); err != nil {
			return err
		}
		rule.UpdatedAt = now
		next, err := ruleToRow(rule)
		if err != nil {
			return err
		}
		if err := tx.Save(&next).Error; err != nil {
			return err
		}
		updated = rule
		return nil
	})
	return updated, err
}

// DeleteRule 删除规则，并把它还没结束的执行记录置为已取消（规则没了，后面的重试没有意义）。
func (s *Store) DeleteRule(ctx context.Context, ruleID string, now int64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Where("c_rule_id = ? AND c_system = 0", ruleID).Delete(&ruleRow{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return tx.Model(&executionRow{}).
			Where("c_rule_id = ? AND c_status IN ?", ruleID, []string{StatusPending, StatusRunning}).
			Updates(map[string]any{"c_status": StatusCanceled, "c_next_run_at": 0, "c_finished_at": now, "c_updated_at": now}).Error
	})
}

// LastExecution 是规则最近一次执行的结果。
type LastExecution struct {
	Status string
	At     int64
}

// lastExecutions 查每条规则（参数是规则 ID 的 JSON 数组）最近创建的一条执行记录。
const lastExecutions = `SELECT e.c_rule_id, e.c_status, e.c_created_at
FROM t_event_executions AS e
WHERE e.c_rule_id IN (SELECT value FROM json_each(?)) AND e.c_execution_id = (
    SELECT c_execution_id FROM t_event_executions
    WHERE c_rule_id = e.c_rule_id
    ORDER BY c_created_at DESC, c_execution_id DESC
    LIMIT 1
)`

// LastExecutions 返回每条规则最近一次执行的状态与创建时间。
func (s *Store) LastExecutions(ctx context.Context, ruleIDs []string) (map[string]LastExecution, error) {
	result := make(map[string]LastExecution, len(ruleIDs))
	if len(ruleIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		RuleID    string `gorm:"column:c_rule_id"`
		Status    string `gorm:"column:c_status"`
		CreatedAt int64  `gorm:"column:c_created_at"`
	}
	ruleIDsJSON, err := json.Marshal(ruleIDs)
	if err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Raw(lastExecutions, string(ruleIDsJSON)).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.RuleID] = LastExecution{Status: row.Status, At: row.CreatedAt}
	}
	return result, nil
}
