package asynctask

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/control/internal/service/asynctask/model"
	"github.com/mooyang-code/moox/modules/control/internal/service/asynctask/queue"

	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// StartWorker 启动任务消费者
func (s *AsyncTaskServiceImpl) StartWorker(ctx context.Context, workerCount int) error {
	log.InfoContextf(ctx, "[AsyncTask] Starting %d workers...", workerCount)

	if s.taskQueue == nil {
		return fmt.Errorf("task queue is not initialized")
	}

	// 启动多个worker（永久运行）
	for i := 0; i < workerCount; i++ {
		workerID := i
		go s.runWorker(ctx, workerID)
	}

	log.InfoContextf(ctx, "[AsyncTask] All %d workers started", workerCount)
	return nil
}

// runWorker Worker运行逻辑
func (s *AsyncTaskServiceImpl) runWorker(ctx context.Context, workerID int) {
	log.InfoContextf(ctx, "[AsyncTask] Worker %d started", workerID)

	for {
		select {
		case <-ctx.Done():
			log.InfoContextf(ctx, "[AsyncTask] Worker %d stopped", workerID)
			return
		default:
			// 从队列中取任务
			taskMsg, err := s.taskQueue.Dequeue(ctx)
			if err != nil {
				if errors.Is(err, queue.ErrQueueEmpty) {
					// 队列为空，休眠一下
					time.Sleep(100 * time.Millisecond)
					continue
				}
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					log.InfoContextf(ctx, "[AsyncTask] Worker %d context canceled", workerID)
					return
				}
				log.ErrorContextf(ctx, "[AsyncTask] Worker %d dequeue error: %v", workerID, err)
				time.Sleep(1 * time.Second)
				continue
			}

			// 处理任务
			s.processTask(ctx, workerID, taskMsg)
		}
	}
}

// processTask 处理单个任务
func (s *AsyncTaskServiceImpl) processTask(ctx context.Context, workerID int, taskMsg queue.TaskMessage) {
	log.InfoContextf(ctx, "[AsyncTask] Worker %d processing task: %s (type: %s)", workerID, taskMsg.TaskID, taskMsg.TaskType)

	// 1. 获取Task详情
	task, err := s.taskDAO.GetAsyncJobTask(ctx, taskMsg.TaskID)
	if err != nil || task == nil {
		log.ErrorContextf(ctx, "[AsyncTask] Worker %d failed to get task %s: %v", workerID, taskMsg.TaskID, err)
		return
	}

	// 2. 设置Job为已启动
	if err := s.jobDAO.SetJobStarted(ctx, task.JobID); err != nil {
		log.ErrorContextf(ctx, "[AsyncTask] Worker %d failed to set job started %s: %v", workerID, task.JobID, err)
	}

	// 3. 设置Task为处理中
	if err := s.taskDAO.SetTaskStarted(ctx, task.TaskID); err != nil {
		log.ErrorContextf(ctx, "[AsyncTask] Worker %d failed to set task started %s: %v", workerID, task.TaskID, err)
	}

	// 4. 获取并执行任务执行器
	executor, exists := GetExecutor(task.TaskType)
	if !exists {
		errorMsg := fmt.Sprintf("no executor registered for task type: %s", task.TaskType)
		log.ErrorContextf(ctx, "[AsyncTask] Worker %d: %s", workerID, errorMsg)
		s.completeTask(ctx, task, TaskStatusFailed, "", errorMsg)
		return
	}

	// 5. 执行业务逻辑
	resultData, err := executor.Execute(ctx, task.TaskID, task.RequestParams)
	if err != nil {
		log.ErrorContextf(ctx, "[AsyncTask] Worker %d task %s execution failed: %v", workerID, task.TaskID, err)
		s.completeTask(ctx, task, TaskStatusFailed, resultData, err.Error())
		return
	}

	// 6. 执行成功
	log.InfoContextf(ctx, "[AsyncTask] Worker %d task %s execution succeeded", workerID, task.TaskID)
	s.completeTask(ctx, task, TaskStatusSuccess, resultData, "")
}

// completeTask 完成任务并更新计数器
func (s *AsyncTaskServiceImpl) completeTask(ctx context.Context, task *model.AsyncJobTask, status int, resultData, errorMessage string) {
	// 1. 更新Task状态
	if err := s.taskDAO.SetTaskCompleted(ctx, task.TaskID, status, resultData, errorMessage); err != nil {
		log.ErrorContextf(ctx, "[AsyncTask] Failed to set task completed %s: %v", task.TaskID, err)
		return
	}

	// 2. 原子更新Job计数器
	var err error
	if status == TaskStatusSuccess {
		err = s.jobDAO.IncrementSuccessCount(ctx, task.JobID)
	} else {
		err = s.jobDAO.IncrementFailedCount(ctx, task.JobID)
	}

	if err != nil {
		log.ErrorContextf(ctx, "[AsyncTask] Failed to increment job counter for %s: %v", task.JobID, err)
	}

	// 3. 检查Job是否完成，触发后处理
	s.tryTriggerJobCompletion(ctx, task)
}

// tryTriggerJobCompletion 尝试触发Job完成后处理
func (s *AsyncTaskServiceImpl) tryTriggerJobCompletion(ctx context.Context, task *model.AsyncJobTask) {
	// 查询Job状态
	job, err := s.jobDAO.GetAsyncJob(ctx, task.JobID)
	if err != nil {
		log.ErrorContextf(ctx, "[AsyncTask] Failed to get job %s: %v", task.JobID, err)
		return
	}
	if job == nil {
		log.ErrorContextf(ctx, "[AsyncTask] Job %s not found", task.JobID)
		return
	}

	// 检查Job是否完成
	if !job.IsCompleted() {
		return
	}

	// CAS操作：只有第一个Worker能成功
	_, alreadyProcessed := s.processedJobs.LoadOrStore(task.JobID, true)
	if alreadyProcessed {
		log.InfoContextf(ctx, "[AsyncTask] Job %s already processed", task.JobID)
		return
	}

	log.InfoContextf(ctx, "[AsyncTask] Job %s completed, triggering post-processing", task.JobID)
	// 异步触发后处理（避免阻塞Worker）
	// 使用框架的上下文拷贝，保留trace信息
	go s.handleJobCompletion(trpc.CloneContext(ctx), job, task)
}

// handleJobCompletion 处理Job完成事件
func (s *AsyncTaskServiceImpl) handleJobCompletion(ctx context.Context, job *model.AsyncJob, task *model.AsyncJobTask) {
	// 延迟清理Map（避免内存泄漏）
	defer func() {
		time.AfterFunc(24*time.Hour, func() {
			s.processedJobs.Delete(job.JobID)
		})
	}()

	log.InfoContextf(ctx, "[AsyncTask] Handling job completion: JobID=%s, TaskType=%s", job.JobID, task.TaskType)

	// 调用注册的Handler
	s.handlersMu.RLock()
	handlers := s.completionHandlers
	s.handlersMu.RUnlock()

	if len(handlers) == 0 {
		log.InfoContextf(ctx, "[AsyncTask] No completion handlers registered")
		return
	}

	for _, handler := range handlers {
		if !handler.CanHandle(task.TaskType) {
			continue
		}

		log.InfoContextf(ctx, "[AsyncTask] Calling completion handler for job %s", job.JobID)
		if err := handler.OnJobCompleted(ctx, job, task); err != nil {
			log.ErrorContextf(ctx, "[AsyncTask] Handler failed for job %s: %v", job.JobID, err)
		}
	}
}
