package dao

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/collectmgr/model"

	"gorm.io/gorm"
)

// TaskRuleQuery 任务规则查询条件
type TaskRuleQuery struct {
	DataType       string // 数据类型
	DataSource     string // 数据源
	Enabled        string // 启用状态 ("true"/"false")
	AssignmentType string // 分配类型
	NodeID         string // 节点ID（固定分配时使用）
	RuleID         string // 规则ID
}

// CollectorTaskRulesDAO 采集任务规则数据访问对象接口
type CollectorTaskRulesDAO interface {
	// GetTaskRulesList 获取任务规则列表
	GetTaskRulesList(ctx context.Context, dataType, dataSource, enabled string) ([]*model.CollectorTaskRules, error)

	// GetTaskRule 获取单个任务规则
	GetTaskRule(ctx context.Context, ruleID string) (*model.CollectorTaskRules, error)

	// CreateTaskRule 创建任务规则
	CreateTaskRule(ctx context.Context, rule *model.CollectorTaskRules) error

	// UpdateTaskRule 更新任务规则
	UpdateTaskRule(ctx context.Context, rule *model.CollectorTaskRules) error

	// DisableTaskRule 关闭任务规则（设置为禁用）
	DisableTaskRule(ctx context.Context, ruleID string) error

	// SearchTaskRules 搜索任务规则，支持多种查询条件
	SearchTaskRules(ctx context.Context, query *TaskRuleQuery) ([]*model.CollectorTaskRules, error)
}

type collectorTaskRulesDaoImpl struct {
	db *gorm.DB
}

// NewCollectorTaskRulesDAO 创建新的任务规则DAO实例
func NewCollectorTaskRulesDAO(db *gorm.DB) CollectorTaskRulesDAO {
	return &collectorTaskRulesDaoImpl{db: db}
}

// GetTaskRulesList 获取任务规则列表
func (d *collectorTaskRulesDaoImpl) GetTaskRulesList(ctx context.Context, dataType, dataSource, enabled string) ([]*model.CollectorTaskRules, error) {
	var rules []*model.CollectorTaskRules
	query := d.db.WithContext(ctx).Where("1=1")
	
	if dataType != "" {
		query = query.Where("c_data_type = ?", dataType)
	}
	if dataSource != "" {
		query = query.Where("c_data_source = ?", dataSource)
	}
	if enabled != "" {
		query = query.Where("c_enabled = ?", enabled)
	}

	result := query.Order("c_mtime DESC").Find(&rules)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get task rules list: %w", result.Error)
	}
	return rules, nil
}

// GetTaskRule 根据规则ID获取单个任务规则
func (d *collectorTaskRulesDaoImpl) GetTaskRule(ctx context.Context, ruleID string) (*model.CollectorTaskRules, error) {
	var rule model.CollectorTaskRules
	result := d.db.WithContext(ctx).
		Where("c_rule_id = ?", ruleID).
		First(&rule)

	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get task rule: %w", result.Error)
	}
	return &rule, nil
}

// CreateTaskRule 创建新的任务规则
func (d *collectorTaskRulesDaoImpl) CreateTaskRule(ctx context.Context, rule *model.CollectorTaskRules) error {
	rule.CreateTime = time.Now()
	rule.ModifyTime = time.Now()

	result := d.db.WithContext(ctx).Create(rule)
	if result.Error != nil {
		return fmt.Errorf("failed to create task rule: %w", result.Error)
	}
	return nil
}

// UpdateTaskRule 更新任务规则
func (d *collectorTaskRulesDaoImpl) UpdateTaskRule(ctx context.Context, rule *model.CollectorTaskRules) error {
	rule.ModifyTime = time.Now()

	result := d.db.WithContext(ctx).
		Model(&model.CollectorTaskRules{}).
		Where("c_rule_id = ?", rule.RuleID).
		Updates(map[string]interface{}{
			"c_data_type":       rule.DataType,
			"c_data_source":     rule.DataSource,
			"c_collect_params":  rule.CollectParams,
			"c_assignment_type": rule.AssignmentType,
			"c_assigned_nodes":  rule.AssignedNodes,
			"c_node_pattern":    rule.NodePattern,
			"c_enabled":         rule.Enabled,
			"c_mtime":           rule.ModifyTime,
		})

	if result.Error != nil {
		return fmt.Errorf("failed to update task rule: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return fmt.Errorf("task rule not found or already deleted")
	}
	return nil
}

// DisableTaskRule 关闭任务规则（设置为禁用）
func (d *collectorTaskRulesDaoImpl) DisableTaskRule(ctx context.Context, ruleID string) error {
	result := d.db.WithContext(ctx).
		Model(&model.CollectorTaskRules{}).
		Where("c_rule_id = ?", ruleID).
		Updates(map[string]interface{}{
			"c_enabled": "false",
			"c_mtime":   time.Now(),
		})

	if result.Error != nil {
		return fmt.Errorf("failed to disable task rule: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return fmt.Errorf("task rule not found or already disabled")
	}
	return nil
}

// SearchTaskRules 搜索任务规则，支持多种查询条件
func (d *collectorTaskRulesDaoImpl) SearchTaskRules(ctx context.Context, query *TaskRuleQuery) ([]*model.CollectorTaskRules, error) {
	var rules []*model.CollectorTaskRules
	dbQuery := d.db.WithContext(ctx).Where("1=1")

	// 根据查询条件构建 WHERE 子句
	if query.RuleID != "" {
		dbQuery = dbQuery.Where("c_rule_id = ?", query.RuleID)
	}
	if query.DataType != "" {
		dbQuery = dbQuery.Where("c_data_type = ?", query.DataType)
	}
	if query.DataSource != "" {
		dbQuery = dbQuery.Where("c_data_source = ?", query.DataSource)
	}
	if query.Enabled != "" {
		dbQuery = dbQuery.Where("c_enabled = ?", query.Enabled)
	}
	if query.AssignmentType != "" {
		dbQuery = dbQuery.Where("c_assignment_type = ?", query.AssignmentType)
	}
	if query.NodeID != "" {
		// 查询固定分配给该节点的任务规则
		dbQuery = dbQuery.Where("c_assignment_type = ? AND c_assigned_nodes LIKE ?", "fixed", "%\""+query.NodeID+"\"%")
	}

	result := dbQuery.Order("c_mtime DESC").Find(&rules)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to search task rules: %w", result.Error)
	}
	return rules, nil
}
