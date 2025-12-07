package collectmgr

import (
	"context"
	"fmt"
	"strconv"
	"time"

	cloudnodedao "github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	collectordao "github.com/mooyang-code/moox/server/internal/service/collectmgr/dao"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/dto"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/model"

	"github.com/bwmarrin/snowflake"
	"trpc.group/trpc-go/trpc-go/log"
)

type TaskRulesServiceImpl struct {
	taskRulesDAO collectordao.CollectorTaskRulesDAO
	nodeDAO      cloudnodedao.CloudNodeDAO
	taskPlanner  TaskPlannerService
	snowflake    *snowflake.Node
}

func NewTaskRulesServiceImpl(
	taskRulesDAO collectordao.CollectorTaskRulesDAO,
	nodeDAO cloudnodedao.CloudNodeDAO,
	taskPlanner TaskPlannerService,
) TaskRuleService {

	// 创建snowflake节点（使用时间戳作为节点ID）
	node, err := snowflake.NewNode(time.Now().Unix() % 1024)
	if err != nil {
		// 如果创建失败，使用默认值1
		node, _ = snowflake.NewNode(1)
	}

	return &TaskRulesServiceImpl{
		taskRulesDAO: taskRulesDAO,
		nodeDAO:      nodeDAO,
		taskPlanner:  taskPlanner,
		snowflake:    node,
	}
}

func (s *TaskRulesServiceImpl) GetTaskRuleList(ctx context.Context, dataType, dataSource, enabled string) ([]*dto.TaskRuleDTO, error) {
	rules, err := s.taskRulesDAO.GetTaskRulesList(ctx, dataType, dataSource, enabled)
	if err != nil {
		return nil, fmt.Errorf("failed to get task rules list: %w", err)
	}

	var result []*dto.TaskRuleDTO
	for _, rule := range rules {
		ruleDTO := &dto.TaskRuleDTO{
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
		result = append(result, ruleDTO)
	}
	return result, nil
}

func (s *TaskRulesServiceImpl) GetTaskRule(ctx context.Context, ruleID string) (*dto.TaskRuleDTO, error) {
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

	return &dto.TaskRuleDTO{
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

func (s *TaskRulesServiceImpl) CreateTaskRule(ctx context.Context, rule *dto.TaskRuleDTO) (string, error) {
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

	// 如果规则启用，立即同步任务实例
	if rule.Enabled == model.EnabledTrue {
		log.InfoContextf(ctx, "[TaskRules] Rule %s created and enabled, syncing instances", ruleID)
		if _, err := s.taskPlanner.SyncRuleInstances(ctx, ruleID); err != nil {
			log.ErrorContextf(ctx, "[TaskRules] Failed to sync instances for rule %s: %v", ruleID, err)
			// 不影响规则创建，只记录错误
		}
	}

	return ruleID, nil
}

func (s *TaskRulesServiceImpl) UpdateTaskRule(ctx context.Context, rule *dto.TaskRuleDTO) error {
	if rule.RuleID == "" {
		return fmt.Errorf("rule ID is required for update")
	}

	// 获取旧规则以检查状态变化
	oldRule, err := s.taskRulesDAO.GetTaskRule(ctx, rule.RuleID)
	if err != nil {
		return fmt.Errorf("failed to get old rule: %w", err)
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

	if err := s.taskRulesDAO.UpdateTaskRule(ctx, modelRule); err != nil {
		return err
	}

	// 根据启用状态变化同步实例
	if rule.Enabled == model.EnabledTrue {
		// 规则启用，同步实例（创建或更新）
		log.InfoContextf(ctx, "[TaskRules] Rule %s updated and enabled, syncing instances", rule.RuleID)
		if _, err := s.taskPlanner.SyncRuleInstances(ctx, rule.RuleID); err != nil {
			log.ErrorContextf(ctx, "[TaskRules] Failed to sync instances for rule %s: %v", rule.RuleID, err)
			// 不影响规则更新，只记录错误
		}
	} else if oldRule.Enabled == model.EnabledTrue && rule.Enabled == model.EnabledFalse {
		// 规则从启用变为禁用，使实例失效
		log.InfoContextf(ctx, "[TaskRules] Rule %s disabled, invalidating instances", rule.RuleID)
		if err := s.taskPlanner.InvalidateRuleInstances(ctx, rule.RuleID); err != nil {
			log.ErrorContextf(ctx, "[TaskRules] Failed to invalidate instances for rule %s: %v", rule.RuleID, err)
			// 不影响规则更新，只记录错误
		}
	}

	return nil
}

func (s *TaskRulesServiceImpl) DisableTaskRule(ctx context.Context, ruleID string) error {
	if ruleID == "" {
		return fmt.Errorf("rule ID is required")
	}

	if err := s.taskRulesDAO.DisableTaskRule(ctx, ruleID); err != nil {
		return err
	}

	// 禁用规则后，使所有实例失效
	log.InfoContextf(ctx, "[TaskRules] Rule %s disabled, invalidating instances", ruleID)
	if err := s.taskPlanner.InvalidateRuleInstances(ctx, ruleID); err != nil {
		log.ErrorContextf(ctx, "[TaskRules] Failed to invalidate instances for rule %s: %v", ruleID, err)
		// 不影响规则禁用，只记录错误
	}

	return nil
}
