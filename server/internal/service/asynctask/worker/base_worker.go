package worker

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/asynctask/logic"
	"github.com/mooyang-code/moox/server/internal/service/asynctask/model"
	"github.com/mooyang-code/moox/server/internal/service/asynctask/queue"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// BaseWorker 基础任务工作器
type BaseWorker struct {
	db                   *gorm.DB
	taskQueue            *queue.MemoryTaskQueue
	asyncTaskService     logic.AsyncTaskService
	executorRegistry     *logic.TaskExecutorRegistry
	stopCh              chan struct{}
	workerCount         int
}

// NewBaseWorker 创建基础工作器
func NewBaseWorker(db *gorm.DB, workerCount int) *BaseWorker {
	return &BaseWorker{
		db:                   db,
		taskQueue:            queue.NewMemoryTaskQueue(100),
		asyncTaskService:     logic.NewAsyncTaskService(db),
		executorRegistry:     logic.NewTaskExecutorRegistry(),
		stopCh:              make(chan struct{}),
		workerCount:         workerCount,
	}
}

// RegisterExecutor 注册任务执行器
func (w *BaseWorker) RegisterExecutor(executor logic.TaskExecutor) {
	w.executorRegistry.Register(executor)
}

// GetTaskQueue 获取任务队列
func (w *BaseWorker) GetTaskQueue() *queue.MemoryTaskQueue {
	return w.taskQueue
}

// Start 启动工作器
func (w *BaseWorker) Start(ctx context.Context) {
	log.InfoContext(ctx, "[BaseWorker] Starting worker...")

	// 启动多个goroutine并发处理
	for i := 0; i < w.workerCount; i++ {
		go w.processMessages(ctx, i)
	}
}

// Stop 停止工作器
func (w *BaseWorker) Stop() {
	log.Info("[BaseWorker] Stopping worker...")
	close(w.stopCh)
}

// processMessages 从队列中处理消息
func (w *BaseWorker) processMessages(ctx context.Context, workerID int) {
	log.InfoContextf(ctx, "[BaseWorker-%d] Worker started", workerID)

	for {
		select {
		case <-w.stopCh:
			log.InfoContextf(ctx, "[BaseWorker-%d] Worker stopped", workerID)
			return
		case msg := <-w.taskQueue.Channel():
			w.handleMessage(ctx, msg, workerID)
		case <-time.After(5 * time.Second):
			// 检查内存队列中的消息
			msg, err := w.taskQueue.Dequeue(ctx)
			if err == nil {
				w.handleMessage(ctx, msg, workerID)
			}
		}
	}
}

// handleMessage 处理单个消息
func (w *BaseWorker) handleMessage(ctx context.Context, msg queue.TaskMessage, workerID int) {
	log.InfoContextf(ctx, "[BaseWorker-%d] Processing message: TaskID=%s, Type=%s", workerID, msg.TaskID, msg.TaskType)

	// 获取任务信息
	task, err := w.asyncTaskService.GetTask(ctx, msg.TaskID)
	if err != nil {
		log.ErrorContextf(ctx, "[BaseWorker-%d] Failed to get task: %v", workerID, err)
		return
	}

	// 获取执行器
	executor, exists := w.executorRegistry.GetExecutor(task.TaskType)
	if !exists {
		log.ErrorContextf(ctx, "[BaseWorker-%d] No executor found for task type: %s", workerID, task.TaskType)
		w.updateTaskStatus(ctx, task.TaskID, model.TaskStatusFailed, "未找到任务执行器")
		return
	}

	// 更新任务状态为处理中
	if err := w.updateTaskStatus(ctx, task.TaskID, model.TaskStatusRunning, ""); err != nil {
		log.ErrorContextf(ctx, "[BaseWorker-%d] Failed to update task status: %v", workerID, err)
		return
	}

	// 执行任务
	if err := executor.Execute(ctx, task); err != nil {
		log.ErrorContextf(ctx, "[BaseWorker-%d] Task execution failed: %v", workerID, err)
		w.updateTaskStatus(ctx, task.TaskID, model.TaskStatusFailed, err.Error())
	} else {
		log.InfoContextf(ctx, "[BaseWorker-%d] Task completed successfully: %s", workerID, task.TaskID)
		w.updateTaskStatus(ctx, task.TaskID, model.TaskStatusSuccess, "")
	}
}

// updateTaskStatus 更新任务状态
func (w *BaseWorker) updateTaskStatus(ctx context.Context, taskID string, status int, errorMsg string) error {
	return w.asyncTaskService.UpdateTaskStatus(ctx, taskID, status, errorMsg)
}