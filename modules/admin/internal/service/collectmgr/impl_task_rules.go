package collectmgr

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/admin/internal/common"
	cloudnodedao "github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/dao"
	collectordao "github.com/mooyang-code/moox/modules/admin/internal/service/collectmgr/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/collectmgr/spacecontext"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
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

func (s *TaskRulesServiceImpl) GetTaskRuleList(ctx context.Context, spaceID, bizType, ruleID, dataType, dataSource, enabled string) ([]*pb.TaskRule, error) {
	if spaceID == "" {
		spaceID, _ = spacecontext.FromContext(ctx)
	}
	rules, err := s.taskRulesDAO.GetTaskRulesList(ctx, spaceID, bizType, ruleID, dataType, dataSource, enabled)
	if err != nil {
		return nil, fmt.Errorf("failed to get task rules list: %w", err)
	}
	result := make([]*pb.TaskRule, 0, len(rules))
	for _, rule := range rules {
		result = append(result, taskRuleModelToPB(rule))
	}
	return result, nil
}

func (s *TaskRulesServiceImpl) GetTaskRule(ctx context.Context, ruleID string) (*pb.TaskRule, error) {
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
	return taskRuleModelToPB(rule), nil
}

func (s *TaskRulesServiceImpl) CreateTaskRule(ctx context.Context, rule *pb.TaskRule) (string, error) {
	// 从登录态注入 space_id，若 body 传入则忽略以防止越权
	spaceID, _ := spacecontext.FromContext(ctx)
	if spaceID != "" {
		rule.SpaceId = spaceID
	}

	// 生成10位随机字符串作为RuleID
	ruleID := common.GenerateID(10)
	rule.RuleId = ruleID

	modelRule := taskRulePBToModel(rule)
	if err := s.taskRulesDAO.CreateTaskRule(ctx, modelRule); err != nil {
		return "", err
	}
	return ruleID, nil
}

func (s *TaskRulesServiceImpl) UpdateTaskRule(ctx context.Context, rule *pb.TaskRule) error {
	if rule.GetRuleId() == "" {
		return fmt.Errorf("rule ID is required for update")
	}

	// space_id 以登录态为准，防止越权改其他空间
	spaceID, _ := spacecontext.FromContext(ctx)
	if spaceID != "" {
		rule.SpaceId = spaceID
	}

	modelRule := taskRulePBToModel(rule)
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
	return s.taskRulesDAO.DisableTaskRule(ctx, spaceID, ruleID)
}
