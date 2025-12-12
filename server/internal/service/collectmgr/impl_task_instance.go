package collectmgr

import (
	"context"
	"fmt"

	cloudnodedao "github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	collectordao "github.com/mooyang-code/moox/server/internal/service/collectmgr/dao"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/model"
)

type TaskInstanceServiceImpl struct {
	instanceDAO  collectordao.CollectorTaskInstanceDAO
	taskRulesDAO collectordao.CollectorTaskRulesDAO
	nodeDAO      cloudnodedao.CloudNodeDAO
	heartbeatDAO cloudnodedao.HeartbeatDAO
}

func NewTaskInstanceServiceImpl(instanceDAO collectordao.CollectorTaskInstanceDAO,
	taskRulesDAO collectordao.CollectorTaskRulesDAO,
	nodeDAO cloudnodedao.CloudNodeDAO,
	heartbeatDAO cloudnodedao.HeartbeatDAO) TaskInstanceService {
	return &TaskInstanceServiceImpl{
		instanceDAO:  instanceDAO,
		taskRulesDAO: taskRulesDAO,
		nodeDAO:      nodeDAO,
		heartbeatDAO: heartbeatDAO,
	}
}

func (s *TaskInstanceServiceImpl) CreateTaskInstance(ctx context.Context, instance *TaskInstanceDTO) error {
	modelInstance := &model.CollectorTaskInstance{
		TaskID:     instance.TaskID,
		RuleID:     instance.RuleID,
		NodeID:     instance.NodeID,
		TaskParams: instance.TaskParams,
		Status:     instance.Status,
		StartTime:  instance.StartTime,
		EndTime:    instance.EndTime,
		Result:     instance.Result,
	}

	return s.instanceDAO.CreateTaskInstance(ctx, modelInstance)
}

func (s *TaskInstanceServiceImpl) GetTaskInstance(ctx context.Context, instanceID string) (*TaskInstanceDTO, error) {
	if instanceID == "" {
		return nil, fmt.Errorf("instance ID is required")
	}

	instance, err := s.instanceDAO.GetTaskInstance(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("failed to get task instance: %w", err)
	}
	if instance == nil {
		return nil, fmt.Errorf("task instance not found")
	}

	return &TaskInstanceDTO{
		ID:         instance.ID,
		TaskID:     instance.TaskID,
		RuleID:     instance.RuleID,
		NodeID:     instance.NodeID,
		Symbol:     instance.Symbol,
		TaskParams: instance.TaskParams,
		Status:     instance.Status,
		StartTime:  instance.StartTime,
		EndTime:    instance.EndTime,
		Result:     instance.Result,
		Invalid:    instance.Invalid,
		CreateTime: instance.CreateTime,
		ModifyTime: instance.ModifyTime,
	}, nil
}

func (s *TaskInstanceServiceImpl) GetTaskInstanceList(ctx context.Context, nodeID string, limit, offset int) ([]*TaskInstanceDTO, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	if nodeID != "" {
		return s.GetTaskInstancesByNode(ctx, nodeID, nil)
	}

	return s.GetRecentInstances(ctx, 24)
}

func (s *TaskInstanceServiceImpl) UpdateTaskInstance(ctx context.Context, instanceID string, instance *TaskInstanceDTO) error {
	if instanceID == "" {
		return fmt.Errorf("instance ID is required")
	}
	if instance == nil {
		return fmt.Errorf("instance is required")
	}

	modelInstance := &model.CollectorTaskInstance{
		ID:         instance.ID,
		TaskID:     instanceID,
		RuleID:     instance.RuleID,
		NodeID:     instance.NodeID,
		TaskParams: instance.TaskParams,
		Status:     instance.Status,
		StartTime:  instance.StartTime,
		EndTime:    instance.EndTime,
		Result:     instance.Result,
	}

	return s.instanceDAO.UpdateTaskInstance(ctx, modelInstance)
}

func (s *TaskInstanceServiceImpl) RemoveTaskInstance(ctx context.Context, instanceID string) error {
	if instanceID == "" {
		return fmt.Errorf("instance ID is required")
	}

	return s.instanceDAO.DeleteTaskInstance(ctx, instanceID)
}

func (s *TaskInstanceServiceImpl) StartInstance(ctx context.Context, instanceID string) error {
	if instanceID == "" {
		return fmt.Errorf("instance ID is required")
	}

	return s.instanceDAO.StartInstance(ctx, instanceID)
}

func (s *TaskInstanceServiceImpl) CompleteInstance(ctx context.Context, instanceID string, success bool, result string) error {
	if instanceID == "" {
		return fmt.Errorf("instance ID is required")
	}

	return s.instanceDAO.CompleteInstance(ctx, instanceID, success, result)
}

// 辅助方法
func (s *TaskInstanceServiceImpl) GetTaskInstancesByNode(ctx context.Context, nodeID string, status []int) ([]*TaskInstanceDTO, error) {
	instances, err := s.instanceDAO.GetTaskInstancesByNode(ctx, nodeID, status)
	if err != nil {
		return nil, fmt.Errorf("failed to get task instances by node: %w", err)
	}

	var result []*TaskInstanceDTO
	for _, instance := range instances {
		dto := &TaskInstanceDTO{
			ID:         instance.ID,
			TaskID:     instance.TaskID,
			RuleID:     instance.RuleID,
			NodeID:     instance.NodeID,
			Symbol:     instance.Symbol,
			TaskParams: instance.TaskParams,
			Status:     instance.Status,
			StartTime:  instance.StartTime,
			EndTime:    instance.EndTime,
			Result:     instance.Result,
			Invalid:    instance.Invalid,
			CreateTime: instance.CreateTime,
			ModifyTime: instance.ModifyTime,
		}
		result = append(result, dto)
	}
	return result, nil
}

func (s *TaskInstanceServiceImpl) GetRecentInstances(ctx context.Context, hours int) ([]*TaskInstanceDTO, error) {
	if hours <= 0 {
		hours = 24
	}

	instances, err := s.instanceDAO.GetRecentInstances(ctx, hours)
	if err != nil {
		return nil, fmt.Errorf("failed to get recent instances: %w", err)
	}

	var result []*TaskInstanceDTO
	for _, instance := range instances {
		dto := &TaskInstanceDTO{
			ID:         instance.ID,
			TaskID:     instance.TaskID,
			RuleID:     instance.RuleID,
			NodeID:     instance.NodeID,
			Symbol:     instance.Symbol,
			TaskParams: instance.TaskParams,
			Status:     instance.Status,
			StartTime:  instance.StartTime,
			EndTime:    instance.EndTime,
			Result:     instance.Result,
			Invalid:    instance.Invalid,
			CreateTime: instance.CreateTime,
			ModifyTime: instance.ModifyTime,
		}
		result = append(result, dto)
	}
	return result, nil
}

// ListTaskInstances 分页查询任务实例
func (s *TaskInstanceServiceImpl) ListTaskInstances(ctx context.Context, nodeID, ruleID string, page, size int) ([]*TaskInstanceDTO, int64, error) {
	instances, total, err := s.instanceDAO.ListInstancesWithPagination(ctx, nodeID, ruleID, page, size)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list task instances: %w", err)
	}

	var result []*TaskInstanceDTO
	for _, instance := range instances {
		dto := &TaskInstanceDTO{
			ID:         instance.ID,
			TaskID:     instance.TaskID,
			RuleID:     instance.RuleID,
			NodeID:     instance.NodeID,
			Symbol:     instance.Symbol,
			TaskParams: instance.TaskParams,
			Status:     instance.Status,
			StartTime:  instance.StartTime,
			EndTime:    instance.EndTime,
			Result:     instance.Result,
			Invalid:    instance.Invalid,
			CreateTime: instance.CreateTime,
			ModifyTime: instance.ModifyTime,
		}
		result = append(result, dto)
	}
	return result, total, nil
}
