package asynctask

import (
	"context"

	"github.com/mooyang-code/moox/server/internal/service/asynctask/model"
)

// Service 异步任务服务接口
type Service interface {
	// CreateAndExecuteTask 创建并执行任务
	CreateAndExecuteTask(ctx context.Context, taskID, taskType string, totalCount int, requestParams interface{}) error
	// GetTaskStatus 查询任务状态
	GetTaskStatus(ctx context.Context, taskID string) (*model.AsyncTask, error)
	// GetTask 获取任务详情
	GetTask(ctx context.Context, taskID string) (*model.AsyncTask, error)
	// GetTaskDetails 获取任务详情列表（支持所有详情）
	GetTaskDetails(ctx context.Context, taskID string) ([]*model.AsyncTaskDetail, error)
	// GetTaskDetailsByStatus 根据状态获取任务详情列表
	GetTaskDetailsByStatus(ctx context.Context, taskID string, status int) ([]*model.AsyncTaskDetail, error)
	// GetTaskDetailsSummary 获取任务详情统计
	GetTaskDetailsSummary(ctx context.Context, taskID string) (*TaskDetailsSummary, error)
	// GetFailedTaskDetails 获取失败的任务详情
	GetFailedTaskDetails(ctx context.Context, taskID string) ([]*model.AsyncTaskDetail, error)
	// CancelTask 取消任务
	CancelTask(ctx context.Context, taskID string) error
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

// TaskDetailsSummary 任务详情统计
type TaskDetailsSummary struct {
	SuccessCount int
	FailedCount  int
}

// TaskItem 任务项
type TaskItem struct {
	ItemID   string
	ItemName string
}
