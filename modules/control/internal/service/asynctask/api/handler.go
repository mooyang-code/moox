package api

import (
	"encoding/json"
	"fmt"

	"github.com/mooyang-code/moox/modules/control/internal/common"
	"github.com/mooyang-code/moox/modules/control/internal/errors"
	"github.com/mooyang-code/moox/modules/control/internal/service/asynctask"

	"github.com/gin-gonic/gin"
	"trpc.group/trpc-go/trpc-go/log"
)

// AsyncJobHandler 异步Job处理器（Job-Task模型）
type AsyncJobHandler struct {
	service asynctask.Service
}

// NewAsyncJobHandler 创建异步Job处理器
func NewAsyncJobHandler(service asynctask.Service) *AsyncJobHandler {
	return &AsyncJobHandler{
		service: service,
	}
}

// CreateJobRequest 创建Job请求
type CreateJobRequest struct {
	Tasks []TaskRequestItem `json:"tasks" binding:"required,min=1"`
}

// TaskRequestItem 单个任务请求项
type TaskRequestItem struct {
	TaskType      string                 `json:"task_type" binding:"required"`
	RequestParams map[string]interface{} `json:"request_params" binding:"required"`
}

// CreateJobResponse 创建Job响应
type CreateJobResponse struct {
	JobID        string `json:"job_id"`
	TotalTaskCnt int    `json:"total_task_cnt"`
}

// CreateJob 创建异步Job（可包含N个Task）
// POST /api/v1/async/jobs
func (h *AsyncJobHandler) CreateJob(c *gin.Context) {
	ctx := c.Request.Context()
	var req CreateJobRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		common.HandleAppError(c, errors.InvalidParam("request_body", err.Error()))
		return
	}

	if len(req.Tasks) == 0 {
		common.HandleAppError(c, errors.InvalidParam("tasks", "tasks cannot be empty"))
		return
	}
	log.InfoContextf(ctx, "[AsyncTask] Creating async job: TaskCount=%d", len(req.Tasks))

	// 构建任务列表 - 直接提取task_type和request_params
	tasks := make([]asynctask.TaskRequest, len(req.Tasks))
	for i, taskItem := range req.Tasks {
		// 将请求参数转为JSON字符串
		paramsJSON, err := json.Marshal(taskItem.RequestParams)
		if err != nil {
			common.HandleAppError(c, errors.InvalidParam("request_params", "invalid task parameters"))
			return
		}

		tasks[i] = asynctask.TaskRequest{
			TaskType:      taskItem.TaskType,
			RequestParams: string(paramsJSON),
		}
	}

	// 创建Job（jobID由服务内部自动生成）
	createdJobID, err := h.service.AsyncJobCreate(ctx, tasks)
	if err != nil {
		log.ErrorContextf(ctx, "[AsyncTask] Failed to create async job: %v", err)
		common.HandleAppError(c, errors.Internal("failed to create job", err))
		return
	}

	log.InfoContextf(ctx, "[AsyncTask] Async job created successfully: JobID=%s", createdJobID)

	response := CreateJobResponse{
		JobID:        createdJobID,
		TotalTaskCnt: len(tasks),
	}
	// 使用数组格式返回数据
	common.SuccessResponse(c, "Job创建成功", []CreateJobResponse{response})
}

// QueryJob 查询Job状态
// GET /api/v1/async/jobs/:job_id
func (h *AsyncJobHandler) QueryJob(c *gin.Context) {
	ctx := c.Request.Context()

	// 添加 panic 恢复
	defer func() {
		if r := recover(); r != nil {
			log.ErrorContextf(ctx, "[AsyncTask] Panic in QueryJob: %v", r)
			common.HandleAppError(c, errors.Internal("internal server error", fmt.Errorf("panic: %v", r)))
		}
	}()

	jobID := c.Param("job_id")
	log.InfoContextf(ctx, "[AsyncTask] QueryJob called - jobID param: '%s'", jobID)

	if jobID == "" {
		log.ErrorContextf(ctx, "[AsyncTask] Empty job_id parameter")
		common.HandleAppError(c, errors.InvalidParam("job_id", "job_id is required"))
		return
	}

	log.InfoContextf(ctx, "[AsyncTask] Querying async job: JobID=%s", jobID)

	// 查询Job状态
	result, err := h.service.AsyncJobQuery(ctx, jobID)
	if err != nil {
		log.ErrorContextf(ctx, "[AsyncTask] Failed to query async job: %v", err)
		common.HandleAppError(c, errors.Internal("failed to query job", err))
		return
	}

	log.InfoContextf(ctx, "[AsyncTask] Async job query successful: JobID=%s, Status=%d, Progress=%d%%",
		jobID, result.JobStatus, result.Progress)

	// 使用数组格式返回数据
	common.SuccessResponse(c, "查询成功", []asynctask.JobQueryResult{*result})
}

// QueryJobWithTasks 查询Job状态（包含Task详情）
// GET /api/v1/async/jobs/:job_id/tasks
func (h *AsyncJobHandler) QueryJobWithTasks(c *gin.Context) {
	ctx := c.Request.Context()
	jobID := c.Param("job_id")

	if jobID == "" {
		common.HandleAppError(c, errors.InvalidParam("job_id", "job_id is required"))
		return
	}

	log.InfoContextf(ctx, "[AsyncTask] Querying async job with tasks: JobID=%s", jobID)

	// 查询Job状态（包含Task详情）
	result, err := h.service.AsyncJobQuery(ctx, jobID)
	if err != nil {
		log.ErrorContextf(ctx, "[AsyncTask] Failed to query async job: %v", err)
		common.HandleAppError(c, errors.Internal("failed to query job", err))
		return
	}

	log.InfoContextf(ctx, "[AsyncTask] Async job query successful: JobID=%s, TaskCount=%d",
		jobID, len(result.Tasks))

	// 使用数组格式返回数据
	common.SuccessResponse(c, "查询成功", []asynctask.JobQueryResult{*result})
}
