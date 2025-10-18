package worker

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/asynctask"
	"github.com/mooyang-code/moox/server/internal/service/asynctask/logic"
	"github.com/mooyang-code/moox/server/internal/service/asynctask/model"
	"github.com/mooyang-code/moox/server/internal/service/asynctask/queue"

	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// TaskHandler 任务处理器接口（可扩展）
type TaskHandler interface {
	// HandleTask 处理任务
	HandleTask(ctx context.Context, task *model.AsyncTask) error
}

// BaseWorker 基础任务工作器
type BaseWorker struct {
	db               *gorm.DB
	taskQueue        *queue.MemoryTaskQueue
	asyncTaskService asynctask.Service
	executorRegistry *asynctask.TaskExecutorRegistry
	stopCh           chan struct{}
	workerCount      int
	timeout          time.Duration // 任务处理超时时间
}

// Config Worker配置
type Config struct {
	DB          *gorm.DB
	WorkerCount int
	Timeout     time.Duration // 0表示不超时
}

// NewBaseWorker 创建基础工作器
func NewBaseWorker(cfg Config) *BaseWorker {
	if cfg.WorkerCount <= 0 {
		cfg.WorkerCount = 3
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Minute // 默认10分钟超时
	}

	return &BaseWorker{
		db:               cfg.DB,
		taskQueue:        queue.NewMemoryTaskQueue(100),
		asyncTaskService: logic.NewService(cfg.DB),
		executorRegistry: asynctask.NewTaskExecutorRegistry(),
		stopCh:           make(chan struct{}),
		workerCount:      cfg.WorkerCount,
		timeout:          cfg.Timeout,
	}
}

// 兼容旧版本的构造函数
// Deprecated: 使用 NewBaseWorker(Config{...}) 替代
func NewBaseWorkerLegacy(db *gorm.DB, workerCount int) *BaseWorker {
	return NewBaseWorker(Config{
		DB:          db,
		WorkerCount: workerCount,
	})
}

// RegisterExecutor 注册任务执行器
func (w *BaseWorker) RegisterExecutor(executor asynctask.TaskExecutor) {
	w.executorRegistry.Register(executor)
}

// GetTaskQueue 获取任务队列
func (w *BaseWorker) GetTaskQueue() *queue.MemoryTaskQueue {
	return w.taskQueue
}

// GetAsyncTaskService 获取异步任务服务
func (w *BaseWorker) GetAsyncTaskService() asynctask.Service {
	return w.asyncTaskService
}

// Start 启动工作器
func (w *BaseWorker) Start(ctx context.Context) {
	log.InfoContext(ctx, "[BaseWorker] Starting worker with %d goroutines...", w.workerCount)

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
	defer log.InfoContextf(ctx, "[BaseWorker-%d] Worker stopped", workerID)

	for {
		select {
		case <-w.stopCh:
			return

		case msg := <-w.taskQueue.Channel():
			w.handleMessage(ctx, msg, workerID)

		case <-time.After(5 * time.Second):
			// 定期检查内存队列中的消息（避免channel阻塞）
			msg, err := w.taskQueue.Dequeue(ctx)
			if err == nil && msg.TaskID != "" {
				w.handleMessage(ctx, msg, workerID)
			}
		}
	}
}

// handleMessage 处理单个消息
func (w *BaseWorker) handleMessage(ctx context.Context, msg queue.TaskMessage, workerID int) {
	log.InfoContextf(ctx, "[BaseWorker-%d] Processing message: TaskID=%s, Type=%s",
		workerID, msg.TaskID, msg.TaskType)

	// 创建带超时的context
	handleCtx := ctx
	var cancel context.CancelFunc
	if w.timeout > 0 {
		handleCtx, cancel = context.WithTimeout(ctx, w.timeout)
		defer cancel()
	}

	// 获取任务信息
	task, err := w.asyncTaskService.GetTask(handleCtx, msg.TaskID)
	if err != nil {
		log.ErrorContextf(ctx, "[BaseWorker-%d] Failed to get task: %v", workerID, err)
		return
	}

	// 获取执行器
	executor, exists := w.executorRegistry.GetExecutor(task.TaskType)
	if !exists {
		log.ErrorContextf(ctx, "[BaseWorker-%d] No executor found for task type: %s",
			workerID, task.TaskType)
		w.updateTaskStatus(ctx, task.TaskID, model.TaskStatusFailed, "未找到任务执行器")
		return
	}

	// 更新任务状态为处理中
	if err := w.updateTaskStatus(handleCtx, task.TaskID, model.TaskStatusRunning, ""); err != nil {
		log.ErrorContextf(ctx, "[BaseWorker-%d] Failed to update task status: %v", workerID, err)
		return
	}

	// 执行任务
	if err := executor.Execute(handleCtx, task); err != nil {
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

// GetWorkerCount 获取Worker数量
func (w *BaseWorker) GetWorkerCount() int {
	return w.workerCount
}

// SetTimeout 设置超时时间
func (w *BaseWorker) SetTimeout(timeout time.Duration) {
	w.timeout = timeout
}
