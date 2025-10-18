package asynctask

import (
	"context"

	"github.com/mooyang-code/moox/server/internal/service/asynctask/model"
)

// TaskExecutor 任务执行器接口
type TaskExecutor interface {
	// GetTaskType 返回执行器支持的任务类型
	GetTaskType() string

	// Execute 执行任务
	Execute(ctx context.Context, task *model.AsyncTask) error

	// ValidateRequest 验证任务请求
	ValidateRequest(taskData string) error
}

// TaskExecutorRegistry 任务执行器注册中心
type TaskExecutorRegistry struct {
	executors map[string]TaskExecutor
}

// NewTaskExecutorRegistry 创建任务执行器注册中心
func NewTaskExecutorRegistry() *TaskExecutorRegistry {
	return &TaskExecutorRegistry{
		executors: make(map[string]TaskExecutor),
	}
}

// Register 注册任务执行器
func (r *TaskExecutorRegistry) Register(executor TaskExecutor) {
	r.executors[executor.GetTaskType()] = executor
}

// GetExecutor 获取任务执行器
func (r *TaskExecutorRegistry) GetExecutor(taskType string) (TaskExecutor, bool) {
	executor, exists := r.executors[taskType]
	return executor, exists
}
