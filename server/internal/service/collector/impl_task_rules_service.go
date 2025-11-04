package collector

import (
	"context"
	"fmt"

	cloudnodedao "github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	collectordao "github.com/mooyang-code/moox/server/internal/service/collector/dao"
	"github.com/mooyang-code/moox/server/internal/service/collector/model"
)

type TaskRulesServiceImpl struct {
	taskRulesDAO collectordao.CollectorTaskRulesDAO
	nodeDAO     cloudnodedao.CloudNodeDAO
}

func NewTaskRulesServiceImpl(taskRulesDAO collectordao.CollectorTaskRulesDAO,
	nodeDAO cloudnodedao.CloudNodeDAO) TaskRuleService {
	return &TaskRulesServiceImpl{
		taskRulesDAO: taskRulesDAO,
		nodeDAO:     nodeDAO,
	}
}

func (s *TaskRulesServiceImpl) GetTaskRuleList(ctx context.Context, dataType, dataSource string) ([]*TaskRuleDTO, error) {
	rules, err := s.taskRulesDAO.GetTaskRulesList(ctx, dataType, dataSource)
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
		CreateTime:     rule.CreateTime,
		ModifyTime:     rule.ModifyTime,
	}, nil
}

func (s *TaskRulesServiceImpl) CreateTaskRule(ctx context.Context, config *TaskRuleDTO) error {
	modelRule := &model.CollectorTaskRules{
		RuleID:         config.RuleID,
		DataType:       config.DataType,
		DataSource:     config.DataSource,
		CollectParams:  config.CollectParams,
		AssignmentType: config.AssignmentType,
		AssignedNodes:  config.AssignedNodes,
		NodePattern:    config.NodePattern,
		Enabled:        config.Enabled,
	}

	return s.taskRulesDAO.CreateTaskRule(ctx, modelRule)
}

func (s *TaskRulesServiceImpl) UpdateTaskRule(ctx context.Context, config *TaskRuleDTO) error {
	modelRule := &model.CollectorTaskRules{
		ID:             config.ID,
		RuleID:         config.RuleID,
		DataType:       config.DataType,
		DataSource:     config.DataSource,
		CollectParams:  config.CollectParams,
		AssignmentType: config.AssignmentType,
		AssignedNodes:  config.AssignedNodes,
		NodePattern:    config.NodePattern,
		Enabled:        config.Enabled,
	}

	return s.taskRulesDAO.UpdateTaskRule(ctx, modelRule)
}

func (s *TaskRulesServiceImpl) RemoveTaskRule(ctx context.Context, ruleID string) error {
	if ruleID == "" {
		return fmt.Errorf("rule ID is required")
	}

	return s.taskRulesDAO.DeleteTaskRule(ctx, ruleID)
}

