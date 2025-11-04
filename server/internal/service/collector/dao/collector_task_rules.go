package dao

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/collector/model"

	"gorm.io/gorm"
)

// CollectorTaskRulesDAO 采集任务规则数据访问对象接口
type CollectorTaskRulesDAO interface {
	// ========== 基础CRUD操作 ==========
	
	// GetTaskRulesList 获取任务规则列表
	GetTaskRulesList(ctx context.Context, dataType, dataSource string) ([]*model.CollectorTaskRules, error)
	
	// GetTaskRule 获取单个任务规则
	GetTaskRule(ctx context.Context, ruleID string) (*model.CollectorTaskRules, error)
	
	// CreateTaskRule 创建任务规则
	CreateTaskRule(ctx context.Context, rule *model.CollectorTaskRules) error
	
	// UpdateTaskRule 更新任务规则
	UpdateTaskRule(ctx context.Context, rule *model.CollectorTaskRules) error
	
	// DeleteTaskRule 删除任务规则
	DeleteTaskRule(ctx context.Context, ruleID string) error
	
	// ========== 查询操作 ==========
	
	// GetEnabledTaskRules 获取所有启用的任务规则
	GetEnabledTaskRules(ctx context.Context) ([]*model.CollectorTaskRules, error)
	
	// GetTaskRulesByType 根据数据类型获取任务规则
	GetTaskRulesByType(ctx context.Context, dataType string) ([]*model.CollectorTaskRules, error)
	
	// GetTaskRulesByDataSource 根据数据源获取任务规则
	GetTaskRulesByDataSource(ctx context.Context, dataSource string) ([]*model.CollectorTaskRules, error)
	
	// GetTaskRulesByAssignmentType 根据分配类型获取任务规则
	GetTaskRulesByAssignmentType(ctx context.Context, assignmentType string) ([]*model.CollectorTaskRules, error)
	
	// GetTaskRulesByNode 根据节点获取任务规则（固定分配）
	GetTaskRulesByNode(ctx context.Context, nodeID string) ([]*model.CollectorTaskRules, error)
	
	// ========== 批量操作 ==========
	
	// BatchUpdateEnabled 批量更新启用状态
	BatchUpdateEnabled(ctx context.Context, ruleIDs []string, enabled string) error
	
	// BatchCreateTaskRules 批量创建任务规则
	BatchCreateTaskRules(ctx context.Context, rules []*model.CollectorTaskRules) error
}

type collectorTaskRulesDaoImpl struct {
	db *gorm.DB
}

// NewCollectorTaskRulesDAO 创建新的任务规则DAO实例
func NewCollectorTaskRulesDAO(db *gorm.DB) CollectorTaskRulesDAO {
	return &collectorTaskRulesDaoImpl{db: db}
}

// GetTaskRulesList 获取任务规则列表
func (d *collectorTaskRulesDaoImpl) GetTaskRulesList(ctx context.Context, dataType, dataSource string) ([]*model.CollectorTaskRules, error) {
	var rules []*model.CollectorTaskRules
	query := d.db.WithContext(ctx).Where("1=1")

	if dataType != "" {
		query = query.Where("c_data_type = ?", dataType)
	}
	if dataSource != "" {
		query = query.Where("c_data_source = ?", dataSource)
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
			"c_data_type":        rule.DataType,
			"c_data_source":      rule.DataSource,
			"c_collect_params":   rule.CollectParams,
			"c_assignment_type":  rule.AssignmentType,
			"c_assigned_nodes":   rule.AssignedNodes,
			"c_node_pattern":     rule.NodePattern,
			"c_enabled":          rule.Enabled,
			"c_mtime":            rule.ModifyTime,
		})

	if result.Error != nil {
		return fmt.Errorf("failed to update task rule: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return fmt.Errorf("task rule not found or already deleted")
	}
	return nil
}

// DeleteTaskRule 删除任务规则
func (d *collectorTaskRulesDaoImpl) DeleteTaskRule(ctx context.Context, ruleID string) error {
	result := d.db.WithContext(ctx).
		Where("c_rule_id = ?", ruleID).
		Delete(&model.CollectorTaskRules{})

	if result.Error != nil {
		return fmt.Errorf("failed to delete task rule: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return fmt.Errorf("task rule not found or already deleted")
	}
	return nil
}

// GetEnabledTaskRules 获取所有启用的任务规则
func (d *collectorTaskRulesDaoImpl) GetEnabledTaskRules(ctx context.Context) ([]*model.CollectorTaskRules, error) {
	var rules []*model.CollectorTaskRules
	result := d.db.WithContext(ctx).
		Where("c_enabled = ?", "true").
		Order("c_mtime DESC").
		Find(&rules)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to get enabled task rules: %w", result.Error)
	}
	return rules, nil
}

// GetTaskRulesByType 根据数据类型获取任务规则
func (d *collectorTaskRulesDaoImpl) GetTaskRulesByType(ctx context.Context, dataType string) ([]*model.CollectorTaskRules, error) {
	var rules []*model.CollectorTaskRules
	result := d.db.WithContext(ctx).
		Where("c_data_type = ?", dataType).
		Order("c_mtime DESC").
		Find(&rules)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to get task rules by type: %w", result.Error)
	}
	return rules, nil
}

// GetTaskRulesByDataSource 根据数据源获取任务规则
func (d *collectorTaskRulesDaoImpl) GetTaskRulesByDataSource(ctx context.Context, dataSource string) ([]*model.CollectorTaskRules, error) {
	var rules []*model.CollectorTaskRules
	result := d.db.WithContext(ctx).
		Where("c_data_source = ?", dataSource).
		Order("c_mtime DESC").
		Find(&rules)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to get task rules by data source: %w", result.Error)
	}
	return rules, nil
}

// GetTaskRulesByAssignmentType 根据分配类型获取任务规则
func (d *collectorTaskRulesDaoImpl) GetTaskRulesByAssignmentType(ctx context.Context, assignmentType string) ([]*model.CollectorTaskRules, error) {
	var rules []*model.CollectorTaskRules
	result := d.db.WithContext(ctx).
		Where("c_assignment_type = ?", assignmentType).
		Order("c_mtime DESC").
		Find(&rules)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to get task rules by assignment type: %w", result.Error)
	}
	return rules, nil
}

// GetTaskRulesByNode 获取指定节点的任务规则（固定分配）
func (d *collectorTaskRulesDaoImpl) GetTaskRulesByNode(ctx context.Context, nodeID string) ([]*model.CollectorTaskRules, error) {
	var rules []*model.CollectorTaskRules

	// 查询固定分配给该节点的任务规则
	result := d.db.WithContext(ctx).
		Where("c_assignment_type = ? AND c_assigned_nodes LIKE ?", "fixed", "%\""+nodeID+"\"").
		Order("c_mtime DESC").
		Find(&rules)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to get task rules by node: %w", result.Error)
	}
	return rules, nil
}

// BatchUpdateEnabled 批量更新启用状态
func (d *collectorTaskRulesDaoImpl) BatchUpdateEnabled(ctx context.Context, ruleIDs []string, enabled string) error {
	if len(ruleIDs) == 0 {
		return nil
	}

	result := d.db.WithContext(ctx).
		Model(&model.CollectorTaskRules{}).
		Where("c_rule_id IN ?", ruleIDs).
		Updates(map[string]interface{}{
			"c_enabled": enabled,
			"c_mtime":   time.Now(),
		})

	if result.Error != nil {
		return fmt.Errorf("failed to batch update enabled status: %w", result.Error)
	}
	return nil
}

// BatchCreateTaskRules 批量创建任务规则
func (d *collectorTaskRulesDaoImpl) BatchCreateTaskRules(ctx context.Context, rules []*model.CollectorTaskRules) error {
	if len(rules) == 0 {
		return nil
	}

	now := time.Now()
	for _, rule := range rules {
		rule.CreateTime = now
		rule.ModifyTime = now
	}

	result := d.db.WithContext(ctx).CreateInBatches(rules, 100)
	if result.Error != nil {
		return fmt.Errorf("failed to batch create task rules: %w", result.Error)
	}
	return nil
}