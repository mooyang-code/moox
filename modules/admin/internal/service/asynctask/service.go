package asynctask

import (
	"context"
	"fmt"
	"sync"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
)

// 全局变量：任务执行器注册表
var (
	executorsMu  sync.RWMutex
	executorsMap = make(map[string]Executor) // key为executor.Type()
)

// Service 异步任务服务接口（Job-Task模型）
//
// 设计说明：service 层直接以 admingen PB 类型作为入参/出参，
// 不再维护中间 DTO（TaskRequest/JobQueryResult/TaskQueryResult 已删除）。
// dao/model 层仍保留内部 model（带 gorm tag，负责 DB 映射），
// service 实现内部做一次性 model→PB 转换。
type Service interface {
	// ========== 任务管理 ==========

	// AsyncJobCreate 创建异步Job（可包含N个Task）
	AsyncJobCreate(ctx context.Context, tasks []*pb.TaskRequestItem) (string, error)

	// AsyncJobQuery 查询Job状态和Task详情
	AsyncJobQuery(ctx context.Context, jobID string) (*pb.QueryAsyncJobRsp, error)

	// ========== 工作进程管理 ==========

	// StartWorker 启动任务消费者（Worker）
	StartWorker(ctx context.Context, workerCount int) error

	// ========== Job完成后处理 ==========

	// RegisterCompletionHandler 注册Job完成处理器
	// 处理器会在Job完成时被异步调用
	RegisterCompletionHandler(handler JobCompletionHandler)
}

// Executor 定义任务执行器接口
type Executor interface {
	// Name 返回执行器外显名称（用于UI显示）
	Name() string
	// Type 返回执行器类型（用于任务匹配）
	Type() string
	// Execute 执行任务处理
	Execute(ctx context.Context, taskID string, requestParams string) (resultData string, err error)
}

// ========== 任务执行器注册表全局函数 ==========

// RegisterExecutor 注册任务执行器
func RegisterExecutor(executor Executor) error {
	if executor == nil {
		return fmt.Errorf("executor cannot be nil")
	}

	taskType := executor.Type()
	if taskType == "" {
		return fmt.Errorf("executor type cannot be empty")
	}

	if executor.Name() == "" {
		return fmt.Errorf("executor name cannot be empty")
	}

	executorsMu.Lock()
	defer executorsMu.Unlock()

	executorsMap[taskType] = executor
	return nil
}

// GetExecutor 获取任务执行器
func GetExecutor(taskType string) (Executor, bool) {
	executorsMu.RLock()
	defer executorsMu.RUnlock()

	executor, exists := executorsMap[taskType]
	return executor, exists
}

// HasExecutor 检查是否注册了指定类型的执行器
func HasExecutor(taskType string) bool {
	executorsMu.RLock()
	defer executorsMu.RUnlock()

	_, exists := executorsMap[taskType]
	return exists
}

// GetDisplayName 获取任务类型的显示名称
func GetDisplayName(taskType string) string {
	executorsMu.RLock()
	defer executorsMu.RUnlock()

	if executor, exists := executorsMap[taskType]; exists {
		return executor.Name()
	}
	return ""
}

// ListExecutors 列出所有执行器
func ListExecutors() map[string]Executor {
	executorsMu.RLock()
	defer executorsMu.RUnlock()

	result := make(map[string]Executor)
	for taskType, executor := range executorsMap {
		result[taskType] = executor
	}
	return result
}

// ExecuteTask 执行指定类型的任务
func ExecuteTask(ctx context.Context, taskType, taskID, requestParams string) (string, error) {
	executor, exists := GetExecutor(taskType)
	if !exists {
		return "", fmt.Errorf("no executor registered for task type: %s", taskType)
	}

	return executor.Execute(ctx, taskID, requestParams)
}
