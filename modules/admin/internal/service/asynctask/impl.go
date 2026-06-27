package asynctask

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/asynctask/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/asynctask/model"
	"github.com/mooyang-code/moox/modules/admin/internal/service/asynctask/queue"
	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"

	"github.com/google/uuid"
	"trpc.group/trpc-go/trpc-go/log"
)

// NewService 创建异步任务服务实例
// 接受数据库管理器，内部创建 DAO
func NewService(dbManager *database.Manager) Service {
	db := dbManager.GetDB()

	// 内部创建 DAO
	jobDAO := dao.NewAsyncJobDAO(db)
	taskDAO := dao.NewAsyncJobTaskDAO(db)

	// 创建任务队列
	taskQueue := queue.NewMemoryTaskQueue(5000)
	return newServiceImpl(jobDAO, taskDAO, taskQueue)
}

// AsyncTaskServiceImpl 异步任务服务实现
type AsyncTaskServiceImpl struct {
	jobDAO    dao.AsyncJobDAO
	taskDAO   dao.AsyncJobTaskDAO
	taskQueue queue.TaskQueue

	// Job完成后处理相关
	completionHandlers []JobCompletionHandler
	handlersMu         sync.RWMutex
	processedJobs      sync.Map // jobID -> bool，记录已触发后处理的Job
}

// newServiceImpl 创建异步任务服务实例
func newServiceImpl(jobDAO dao.AsyncJobDAO, taskDAO dao.AsyncJobTaskDAO,
	taskQueue queue.TaskQueue) Service {
	return &AsyncTaskServiceImpl{
		jobDAO:    jobDAO,
		taskDAO:   taskDAO,
		taskQueue: taskQueue,
	}
}

// AsyncJobCreate 创建异步Job（可包含N个Task）
func (s *AsyncTaskServiceImpl) AsyncJobCreate(ctx context.Context, tasks []*pb.TaskRequestItem) (string, error) {
	if len(tasks) == 0 {
		return "", fmt.Errorf("no tasks provided")
	}

	sanitizedTasks, err := sanitizeTaskRequests(ctx, tasks)
	if err != nil {
		return "", err
	}
	tasks = sanitizedTasks

	// 生成JobID
	jobID := uuid.New().String()
	log.InfoContextf(ctx, "[AsyncTask] AsyncJobCreate: JobID=%s, TaskCount=%d", jobID, len(tasks))

	// 验证所有任务类型都已注册
	for _, task := range tasks {
		if !HasExecutor(task.GetTaskType()) {
			return "", fmt.Errorf("no executor registered for task type: %s", task.GetTaskType())
		}
	}

	// 1. 创建Job记录
	job := &model.AsyncJob{
		JobID:          jobID,
		RequestParams:  "", // 可以存储整体请求参数
		TotalTaskCnt:   len(tasks),
		SuccessTaskCnt: 0,
		FailedTaskCnt:  0,
		IsStarted:      0,
	}

	// 如果需要保存整体请求参数
	if len(tasks) > 0 {
		paramsJSON, _ := json.Marshal(tasks)
		job.RequestParams = string(paramsJSON)
	}
	if err := s.jobDAO.CreateAsyncJob(ctx, job); err != nil {
		return "", fmt.Errorf("failed to create async job: %w", err)
	}

	// 2. 创建N个Task记录
	taskModels := make([]*model.AsyncJobTask, len(tasks))
	for i, taskReq := range tasks {
		taskID := fmt.Sprintf("%s-task-%d", jobID, i)
		taskModels[i] = &model.AsyncJobTask{
			TaskID:        taskID,
			JobID:         jobID,
			TaskType:      taskReq.GetTaskType(),
			TaskStatus:    TaskStatusPending, // 初始状态为待处理
			RequestParams: taskReq.GetRequestParams(),
		}
	}

	if err := s.taskDAO.BatchCreateAsyncJobTasks(ctx, taskModels); err != nil {
		return "", fmt.Errorf("failed to batch create tasks: %w", err)
	}

	// 3. 将所有Task入队列
	for _, taskModel := range taskModels {
		taskMessage := queue.TaskMessage{
			TaskID:    taskModel.TaskID,
			TaskType:  taskModel.TaskType,
			CreatedAt: time.Now(),
			Data: map[string]interface{}{
				"job_id":         jobID,
				"request_params": taskModel.RequestParams,
			},
		}

		if err := s.taskQueue.Enqueue(taskMessage); err != nil {
			log.ErrorContextf(ctx, "[AsyncTask] Failed to enqueue task %s: %v", taskModel.TaskID, err)
			// 入队列失败我们不做其它逻辑，等待超时，等待超时后任务会自动失败
			continue
		}
	}
	log.InfoContextf(ctx, "[AsyncTask] AsyncJobCreate completed: JobID=%s, TaskCount=%d", jobID, len(tasks))
	return jobID, nil
}

// AsyncJobQuery 查询Job状态
func (s *AsyncTaskServiceImpl) AsyncJobQuery(ctx context.Context, jobID string) (*pb.QueryAsyncJobRsp, error) {
	log.InfoContextf(ctx, "[AsyncTask] AsyncJobQuery: JobID=%s", jobID)

	// 1. 查询Job
	job, err := s.jobDAO.GetAsyncJob(ctx, jobID)
	if err != nil {
		return nil, fmt.Errorf("failed to get async job: %w", err)
	}
	if job == nil {
		return nil, fmt.Errorf("job not found: %s", jobID)
	}

	// 2. 查询所有Task（可选，根据需要）
	tasks, err := s.taskDAO.GetTasksByJobID(ctx, jobID)
	if err != nil {
		log.ErrorContextf(ctx, "[AsyncTask] Failed to get tasks for job %s: %v", jobID, err)
		// 即使获取Task失败，也返回Job信息
		tasks = nil
	}

	// 3. 构建返回结果
	result := BuildQueryAsyncJobRsp(job, tasks)

	log.InfoContextf(ctx, "[AsyncTask] AsyncJobQuery completed: JobID=%s, Status=%d, Progress=%d%%",
		jobID, result.GetJobStatus(), result.GetProgress())
	return result, nil
}

// RegisterCompletionHandler 注册Job完成处理器
// 该处理器会在Job完成时被异步调用
func (s *AsyncTaskServiceImpl) RegisterCompletionHandler(handler JobCompletionHandler) {
	s.handlersMu.Lock()
	defer s.handlersMu.Unlock()
	s.completionHandlers = append(s.completionHandlers, handler)
	log.Infof("[AsyncTask] Registered completion handler")
}
