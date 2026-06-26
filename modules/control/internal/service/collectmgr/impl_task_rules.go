package collectmgr

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/control/internal/common"
	cloudnodedao "github.com/mooyang-code/moox/modules/control/internal/service/cloudnode/dao"
	collectordao "github.com/mooyang-code/moox/modules/control/internal/service/collectmgr/dao"
	"github.com/mooyang-code/moox/modules/control/internal/service/collectmgr/dto"
	"github.com/mooyang-code/moox/modules/control/internal/service/collectmgr/model"
	"github.com/mooyang-code/moox/modules/control/internal/service/collectmgr/spacecontext"
)

// TaskRulesServiceImpl 实现采集任务规则业务服务。
type TaskRulesServiceImpl struct {
	taskRulesDAO collectordao.CollectorTaskRulesDAO
	nodeDAO      cloudnodedao.CloudNodeDAO
}

func NewTaskRulesServiceImpl(
	taskRulesDAO collectordao.CollectorTaskRulesDAO,
	nodeDAO cloudnodedao.CloudNodeDAO,
) TaskRuleService {
	return &TaskRulesServiceImpl{
		taskRulesDAO: taskRulesDAO,
		nodeDAO:      nodeDAO,
	}
}

func (s *TaskRulesServiceImpl) GetTaskRuleList(ctx context.Context, bizType, dataType, dataSource, enabled string) ([]*dto.TaskRuleDTO, error) {
	spaceID, _ := spacecontext.FromContext(ctx)
	rules, err := s.taskRulesDAO.GetTaskRulesList(ctx, spaceID, bizType, dataType, dataSource, enabled)
	if err != nil {
		return nil, fmt.Errorf("failed to get task rules list: %w", err)
	}

	var result []*dto.TaskRuleDTO
	for _, rule := range rules {
		ruleDTO := &dto.TaskRuleDTO{
			ID:             rule.ID,
			SpaceID:        rule.SpaceID,
			RuleID:         rule.RuleID,
			BizType:        rule.BizType,
			DataType:       rule.DataType,
			DataSource:     rule.DataSource,
			CollectParams:  rule.CollectParams,
			AssignmentType: rule.AssignmentType,
			AssignedNodes:  rule.AssignedNodes,
			NodePattern:    rule.NodePattern,
			NodeTags:       rule.NodeTags,
			Enabled:        rule.Enabled,
			Creator:        rule.Creator,
			CreateTime:     rule.CreateTime,
			ModifyTime:     rule.ModifyTime,
		}
		result = append(result, ruleDTO)
	}
	return result, nil
}

func (s *TaskRulesServiceImpl) GetTaskRule(ctx context.Context, ruleID string) (*dto.TaskRuleDTO, error) {
	if ruleID == "" {
		return nil, fmt.Errorf("rule ID is required")
	}

	spaceID, _ := spacecontext.FromContext(ctx)
	rule, err := s.taskRulesDAO.GetTaskRule(ctx, spaceID, ruleID)
	if err != nil {
		return nil, fmt.Errorf("failed to get task rule: %w", err)
	}
	if rule == nil {
		return nil, fmt.Errorf("task rule not found")
	}

	return &dto.TaskRuleDTO{
		ID:             rule.ID,
		SpaceID:        rule.SpaceID,
		RuleID:         rule.RuleID,
		BizType:        rule.BizType,
		DataType:       rule.DataType,
		DataSource:     rule.DataSource,
		CollectParams:  rule.CollectParams,
		AssignmentType: rule.AssignmentType,
		AssignedNodes:  rule.AssignedNodes,
		NodePattern:    rule.NodePattern,
		NodeTags:       rule.NodeTags,
		Enabled:        rule.Enabled,
		Creator:        rule.Creator,
		CreateTime:     rule.CreateTime,
		ModifyTime:     rule.ModifyTime,
	}, nil
}

func (s *TaskRulesServiceImpl) CreateTaskRule(ctx context.Context, rule *dto.TaskRuleDTO) (string, error) {
	// 从登录态注入 space_id，若 body 传入则忽略以防止越权
	spaceID, _ := spacecontext.FromContext(ctx)
	if spaceID != "" {
		rule.SpaceID = spaceID
	}

	// 生成10位随机字符串作为RuleID
	ruleID := common.GenerateID(10)
	rule.RuleID = ruleID

	modelRule := &model.CollectorTaskRules{
		SpaceID:        rule.SpaceID,
		RuleID:         rule.RuleID,
		BizType:        rule.BizType,
		DataType:       rule.DataType,
		DataSource:     rule.DataSource,
		CollectParams:  rule.CollectParams,
		AssignmentType: rule.AssignmentType,
		AssignedNodes:  rule.AssignedNodes,
		NodePattern:    rule.NodePattern,
		NodeTags:       rule.NodeTags,
		Enabled:        rule.Enabled,
		Creator:        rule.Creator,
	}

	if err := s.taskRulesDAO.CreateTaskRule(ctx, modelRule); err != nil {
		return "", err
	}

	return ruleID, nil
}

func (s *TaskRulesServiceImpl) UpdateTaskRule(ctx context.Context, rule *dto.TaskRuleDTO) error {
	if rule.RuleID == "" {
		return fmt.Errorf("rule ID is required for update")
	}

	// space_id 以登录态为准，防止越权改其他空间
	spaceID, _ := spacecontext.FromContext(ctx)
	if spaceID != "" {
		rule.SpaceID = spaceID
	}

	modelRule := &model.CollectorTaskRules{
		ID:             rule.ID,
		SpaceID:        rule.SpaceID,
		RuleID:         rule.RuleID,
		BizType:        rule.BizType,
		DataType:       rule.DataType,
		DataSource:     rule.DataSource,
		CollectParams:  rule.CollectParams,
		AssignmentType: rule.AssignmentType,
		AssignedNodes:  rule.AssignedNodes,
		NodePattern:    rule.NodePattern,
		NodeTags:       rule.NodeTags,
		Enabled:        rule.Enabled,
		Creator:        rule.Creator,
	}

	if err := s.taskRulesDAO.UpdateTaskRule(ctx, modelRule); err != nil {
		return err
	}

	return nil
}

func (s *TaskRulesServiceImpl) DisableTaskRule(ctx context.Context, ruleID string) error {
	if ruleID == "" {
		return fmt.Errorf("rule ID is required")
	}

	spaceID, _ := spacecontext.FromContext(ctx)
	if err := s.taskRulesDAO.DisableTaskRule(ctx, spaceID, ruleID); err != nil {
		return err
	}

	return nil
}
