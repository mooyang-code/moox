package collector

import (
	"context"
	"fmt"
	"strconv"
	"time"

	cloudnodedao "github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	collectordao "github.com/mooyang-code/moox/server/internal/service/collector/dao"
	"github.com/mooyang-code/moox/server/internal/service/collector/model"

	"github.com/bwmarrin/snowflake"
)

type TaskRulesServiceImpl struct {
	taskRulesDAO collectordao.CollectorTaskRulesDAO
	nodeDAO      cloudnodedao.CloudNodeDAO
	snowflake    *snowflake.Node
}

func NewTaskRulesServiceImpl(taskRulesDAO collectordao.CollectorTaskRulesDAO,
	nodeDAO cloudnodedao.CloudNodeDAO) TaskRuleService {

	// 创建snowflake节点（使用时间戳作为节点ID）
	node, err := snowflake.NewNode(time.Now().Unix() % 1024)
	if err != nil {
		// 如果创建失败，使用默认值1
		node, _ = snowflake.NewNode(1)
	}

	return &TaskRulesServiceImpl{
		taskRulesDAO: taskRulesDAO,
		nodeDAO:      nodeDAO,
		snowflake:    node,
	}
}

func (s *TaskRulesServiceImpl) GetTaskRuleList(ctx context.Context, dataType, dataSource, enabled string) ([]*TaskRuleDTO, error) {
	rules, err := s.taskRulesDAO.GetTaskRulesList(ctx, dataType, dataSource, enabled)
	if err != nil {
		return nil, fmt.Errorf("failed to get task rules list: %w", err)
	}

	var result []*TaskRuleDTO
	for _, rule := range rules {
		dto := &TaskRuleDTO{
			ID:             rule.ID,
			RuleID:         rule.RuleID,
			DataType:       rule.DataType,
			DataSource:     rule.DataSource,
			CollectParams:  rule.CollectParams,
			AssignmentType: rule.AssignmentType,
			AssignedNodes:  rule.AssignedNodes,
			NodePattern:    rule.NodePattern,
			Enabled:        rule.Enabled,
			Creator:        rule.Creator,
			CreateTime:     rule.CreateTime,
			ModifyTime:     rule.ModifyTime,
		}
		result = append(result, dto)
	}
	return result, nil
}

func (s *TaskRulesServiceImpl) GetTaskRule(ctx context.Context, ruleID string) (*TaskRuleDTO, error) {
	if ruleID == "" {
		return nil, fmt.Errorf("rule ID is required")
	}

	rule, err := s.taskRulesDAO.GetTaskRule(ctx, ruleID)
	if err != nil {
		return nil, fmt.Errorf("failed to get task rule: %w", err)
	}
	if rule == nil {
		return nil, fmt.Errorf("task rule not found")
	}

	return &TaskRuleDTO{
		ID:             rule.ID,
		RuleID:         rule.RuleID,
		DataType:       rule.DataType,
		DataSource:     rule.DataSource,
		CollectParams:  rule.CollectParams,
		AssignmentType: rule.AssignmentType,
		AssignedNodes:  rule.AssignedNodes,
		NodePattern:    rule.NodePattern,
		Enabled:        rule.Enabled,
		Creator:        rule.Creator,
		CreateTime:     rule.CreateTime,
		ModifyTime:     rule.ModifyTime,
	}, nil
}

func (s *TaskRulesServiceImpl) CreateTaskRule(ctx context.Context, rule *TaskRuleDTO) (string, error) {
	// 生成snowflake ID
	sfID := s.snowflake.Generate()
	ruleID := strconv.FormatInt(sfID.Int64(), 10)
	rule.RuleID = ruleID

	modelRule := &model.CollectorTaskRules{
		RuleID:         rule.RuleID,
		DataType:       rule.DataType,
		DataSource:     rule.DataSource,
		CollectParams:  rule.CollectParams,
		AssignmentType: rule.AssignmentType,
		AssignedNodes:  rule.AssignedNodes,
		NodePattern:    rule.NodePattern,
		Enabled:        rule.Enabled,
		Creator:        rule.Creator,
	}

	if err := s.taskRulesDAO.CreateTaskRule(ctx, modelRule); err != nil {
		return "", err
	}
	return ruleID, nil
}

func (s *TaskRulesServiceImpl) UpdateTaskRule(ctx context.Context, rule *TaskRuleDTO) error {
	if rule.RuleID == "" {
		return fmt.Errorf("rule ID is required for update")
	}

	modelRule := &model.CollectorTaskRules{
		ID:             rule.ID,
		RuleID:         rule.RuleID,
		DataType:       rule.DataType,
		DataSource:     rule.DataSource,
		CollectParams:  rule.CollectParams,
		AssignmentType: rule.AssignmentType,
		AssignedNodes:  rule.AssignedNodes,
		NodePattern:    rule.NodePattern,
		Enabled:        rule.Enabled,
		Creator:        rule.Creator,
	}
	return s.taskRulesDAO.UpdateTaskRule(ctx, modelRule)
}

func (s *TaskRulesServiceImpl) DisableTaskRule(ctx context.Context, ruleID string) error {
	if ruleID == "" {
		return fmt.Errorf("rule ID is required")
	}

	return s.taskRulesDAO.DisableTaskRule(ctx, ruleID)
}
