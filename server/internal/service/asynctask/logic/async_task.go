package logic

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mooyang-code/moox/server/internal/service/asynctask/dao"
	"github.com/mooyang-code/moox/server/internal/service/asynctask/model"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// AsyncTaskService 异步任务服务接口
type AsyncTaskService interface {
	// CreateAndExecuteTask 创建并执行任务
	CreateAndExecuteTask(ctx context.Context, taskID, taskType string, totalCount int, requestParams interface{}) error
	// GetTaskStatus 查询任务状态
	GetTaskStatus(ctx context.Context, taskID string) (*model.AsyncTask, error)
	// GetTask 获取任务详情
	GetTask(ctx context.Context, taskID string) (*model.AsyncTask, error)
	// GetTaskDetails 获取任务详情列表
	GetTaskDetails(ctx context.Context, taskID string, status int) ([]*model.AsyncTaskDetail, error)
	// UpdateTaskProgress 更新任务进度
	UpdateTaskProgress(ctx context.Context, taskID string, successCount, failedCount int) error
	// UpdateTaskStatus 更新任务状态
	UpdateTaskStatus(ctx context.Context, taskID string, status int, errorMessage string) error
	// CompleteTask 完成任务
	CompleteTask(ctx context.Context, taskID string, status int, resultData interface{}, errorMessage string) error
	// UpdateTaskDetailStatus 更新任务详情状态
	UpdateTaskDetailStatus(ctx context.Context, taskID, itemID string, status int, errorMessage string) error
	// BatchCreateTaskDetails 批量创建任务详情
	BatchCreateTaskDetails(ctx context.Context, taskID string, items []TaskItem) error
	// RegisterExecutor 注册任务执行器
	RegisterExecutor(taskType string, executor TaskExecutor)
}

// TaskItem 任务项
type TaskItem struct {
	ItemID   string
	ItemName string
}

type asyncTaskServiceImpl struct {
	taskDAO       dao.AsyncTaskDAO
	taskDetailDAO dao.AsyncTaskDetailDAO
	executors     map[string]TaskExecutor
	db            *gorm.DB
}

// NewAsyncTaskService 创建新的异步任务服务实例
func NewAsyncTaskService(db *gorm.DB) AsyncTaskService {
	return &asyncTaskServiceImpl{
		taskDAO:       dao.NewAsyncTaskDAO(db),
		taskDetailDAO: dao.NewAsyncTaskDetailDAO(db),
		executors:     make(map[string]TaskExecutor),
		db:            db,
	}
}

// RegisterExecutor 注册任务执行器
func (s *asyncTaskServiceImpl) RegisterExecutor(taskType string, executor TaskExecutor) {
	s.executors[taskType] = executor
}

// CreateAndExecuteTask 创建并执行任务
func (s *asyncTaskServiceImpl) CreateAndExecuteTask(ctx context.Context, taskID, taskType string, totalCount int, requestParams interface{}) error {
	log.InfoContextf(ctx, "CreateAndExecuteTask : %+v", requestParams)
	// 序列化请求参数
	paramsJSON, err := json.Marshal(requestParams)
	if err != nil {
		return fmt.Errorf("failed to marshal request params: %w", err)
	}

	// 创建任务记录
	task := &model.AsyncTask{
		TaskID:        taskID,
		TaskType:      taskType,
		TaskStatus:    model.TaskStatusProcessing,
		TotalCount:    totalCount,
		SuccessCount:  0,
		FailedCount:   0,
		RequestParams: string(paramsJSON),
	}

	if err := s.taskDAO.CreateAsyncTask(ctx, task); err != nil {
		return fmt.Errorf("failed to create async task: %w", err)
	}

	// 获取对应的执行器
	executor, ok := s.executors[taskType]
	if !ok {
		// 如果没有注册执行器，更新任务状态为失败
		s.CompleteTask(ctx, taskID, model.TaskStatusFailed, nil, "unsupported task type")
		return fmt.Errorf("unsupported task type: %s", taskType)
	}

	// 异步执行任务
	go func() {
		// 创建新的context防止父context取消影响任务执行
		taskCtx := context.Background()

		// 设置任务开始时间
		if err := s.taskDAO.SetTaskStarted(taskCtx, taskID); err != nil {
			log.ErrorContextf(taskCtx, "Failed to set task started: %v", err)
		}

		// 执行任务
		if err := executor.Execute(taskCtx, task); err != nil {
			log.ErrorContextf(taskCtx, "Failed to execute task %s: %v", taskID, err)
			// 任务执行失败，由执行器负责更新状态
		}
	}()
	return nil
}

// GetTaskStatus 查询任务状态
func (s *asyncTaskServiceImpl) GetTaskStatus(ctx context.Context, taskID string) (*model.AsyncTask, error) {
	task, err := s.taskDAO.GetAsyncTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to get task status: %w", err)
	}
	if task == nil {
		return nil, fmt.Errorf("task not found: %s", taskID)
	}
	return task, nil
}

// GetTask 获取任务详情（与GetTaskStatus相同）
func (s *asyncTaskServiceImpl) GetTask(ctx context.Context, taskID string) (*model.AsyncTask, error) {
	return s.GetTaskStatus(ctx, taskID)
}

// GetTaskDetails 获取任务详情列表
func (s *asyncTaskServiceImpl) GetTaskDetails(ctx context.Context, taskID string, status int) ([]*model.AsyncTaskDetail, error) {
	if status > 0 {
		return s.taskDetailDAO.GetTaskDetailsByStatus(ctx, taskID, status)
	}
	return s.taskDetailDAO.GetTaskDetails(ctx, taskID)
}

// UpdateTaskStatus 更新任务状态
func (s *asyncTaskServiceImpl) UpdateTaskStatus(ctx context.Context, taskID string, status int, errorMessage string) error {
	if err := s.taskDAO.UpdateTaskStatus(ctx, taskID, status, errorMessage); err != nil {
		return fmt.Errorf("failed to update task status: %w", err)
	}

	return nil
}

// UpdateTaskProgress 更新任务进度
func (s *asyncTaskServiceImpl) UpdateTaskProgress(ctx context.Context, taskID string, successCount, failedCount int) error {
	// 更新进度
	if err := s.taskDAO.UpdateTaskProgress(ctx, taskID, successCount, failedCount); err != nil {
		return fmt.Errorf("failed to update task progress: %w", err)
	}

	// 检查是否所有任务都已完成
	task, err := s.taskDAO.GetAsyncTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("failed to get task for progress check: %w", err)
	}

	if task != nil && (task.SuccessCount+task.FailedCount) >= task.TotalCount {
		// 所有任务已完成，更新任务状态
		status := model.TaskStatusSuccess
		if task.FailedCount > 0 {
			if task.SuccessCount > 0 {
				status = model.TaskStatusPartial
			} else {
				status = model.TaskStatusFailed
			}
		}

		// 生成结果数据
		resultData := map[string]interface{}{
			"total_count":   task.TotalCount,
			"success_count": task.SuccessCount,
			"failed_count":  task.FailedCount,
		}

		if err := s.CompleteTask(ctx, taskID, status, resultData, ""); err != nil {
			log.ErrorContextf(ctx, "Failed to complete task %s: %v", taskID, err)
		}
	}
	return nil
}

// CompleteTask 完成任务
func (s *asyncTaskServiceImpl) CompleteTask(ctx context.Context, taskID string, status int, resultData interface{}, errorMessage string) error {
	var resultJSON string
	if resultData != nil {
		data, err := json.Marshal(resultData)
		if err != nil {
			return fmt.Errorf("failed to marshal result data: %w", err)
		}
		resultJSON = string(data)
	}

	if err := s.taskDAO.SetTaskCompleted(ctx, taskID, status, resultJSON, errorMessage); err != nil {
		return fmt.Errorf("failed to complete task: %w", err)
	}
	return nil
}

// UpdateTaskDetailStatus 更新任务详情状态
func (s *asyncTaskServiceImpl) UpdateTaskDetailStatus(ctx context.Context, taskID, itemID string, status int, errorMessage string) error {
	// 更新详情状态
	if err := s.taskDetailDAO.UpdateTaskDetailStatus(ctx, taskID, itemID, status, errorMessage); err != nil {
		return fmt.Errorf("failed to update task detail status: %w", err)
	}

	// 统计成功和失败数量
	successCount, err := s.taskDetailDAO.CountTaskDetailsByStatus(ctx, taskID, model.TaskDetailStatusSuccess)
	if err != nil {
		return fmt.Errorf("failed to count success details: %w", err)
	}

	failedCount, err := s.taskDetailDAO.CountTaskDetailsByStatus(ctx, taskID, model.TaskDetailStatusFailed)
	if err != nil {
		return fmt.Errorf("failed to count failed details: %w", err)
	}

	// 更新主任务进度
	return s.UpdateTaskProgress(ctx, taskID, int(successCount), int(failedCount))
}

// BatchCreateTaskDetails 批量创建任务详情
func (s *asyncTaskServiceImpl) BatchCreateTaskDetails(ctx context.Context, taskID string, items []TaskItem) error {
	if len(items) == 0 {
		return nil
	}

	details := make([]*model.AsyncTaskDetail, len(items))
	for i, item := range items {
		details[i] = &model.AsyncTaskDetail{
			TaskID:   taskID,
			ItemID:   item.ItemID,
			ItemName: item.ItemName,
			Status:   model.TaskDetailStatusPending,
		}
	}

	if err := s.taskDetailDAO.BatchCreateAsyncTaskDetails(ctx, details); err != nil {
		return fmt.Errorf("failed to batch create task details: %w", err)
	}
	return nil
}
