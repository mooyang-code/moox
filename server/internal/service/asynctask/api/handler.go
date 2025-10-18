package api

import (
	"context"

	"github.com/mooyang-code/moox/server/internal/common"
	"github.com/mooyang-code/moox/server/internal/errors"
	"github.com/mooyang-code/moox/server/internal/logger"
	"github.com/mooyang-code/moox/server/internal/service/asynctask"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// AsyncTaskHandler 异步任务处理器
type AsyncTaskHandler struct {
	service asynctask.Service
}

// NewAsyncTaskHandler 创建异步任务处理器
func NewAsyncTaskHandler(service asynctask.Service) *AsyncTaskHandler {
	return &AsyncTaskHandler{
		service: service,
	}
}

// CreateTaskRequest 创建任务请求
type CreateTaskRequest struct {
	TaskType      string                 `json:"task_type" binding:"required"`
	RequestParams map[string]interface{} `json:"request_params" binding:"required"`
}

// CreateTask 创建并执行异步任务
func (h *AsyncTaskHandler) CreateTask(c *gin.Context) {
	ctx := c.Request.Context()
	var req CreateTaskRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		common.HandleAppError(c, errors.InvalidParam("request_body", err.Error()))
		return
	}

	// 验证任务类型
	if req.TaskType == "" {
		common.HandleAppError(c, errors.InvalidParam("task_type", "task type is required"))
		return
	}

	// 生成唯一的任务ID
	taskID := uuid.New().String()
	logger.Infof(ctx, "Creating async task: taskID=%s, type=%s", taskID, req.TaskType)

	// 根据任务类型获取总数
	totalCount := getTotalCountFromParams(req.TaskType, req.RequestParams)
	if totalCount <= 0 {
		common.HandleAppError(c, errors.InvalidParam("request_params", "invalid task parameters or empty nodes list"))
		return
	}

	// 创建并执行任务
	err := h.service.CreateAndExecuteTask(ctx, taskID, req.TaskType, totalCount, req.RequestParams)
	if err != nil {
		logger.Errorf(ctx, "Failed to create task: %v", err)
		common.HandleAppError(c, errors.Internal("failed to create task", err))
		return
	}

	logger.Infof(ctx, "Task created successfully: taskID=%s", taskID)
	response := map[string]interface{}{
		"task_id":     taskID,
		"task_type":   req.TaskType,
		"total_count": totalCount,
	}
	common.SuccessResponse(c, "任务创建成功", response)
}

// QueryTaskRequest 查询任务请求
type QueryTaskRequest struct {
	TaskID string `form:"task_id" binding:"required"`
}

// QueryTask 查询任务状态
func (h *AsyncTaskHandler) QueryTask(c *gin.Context) {
	ctx := c.Request.Context()
	var req QueryTaskRequest

	if err := c.ShouldBindQuery(&req); err != nil {
		common.HandleAppError(c, errors.InvalidParam("query", err.Error()))
		return
	}

	// 查询任务状态
	task, err := h.service.GetTask(ctx, req.TaskID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			common.HandleAppError(c, errors.NotFound("task"))
		} else {
			logger.Errorf(ctx, "Failed to query task: %v", err)
			common.HandleAppError(c, errors.Internal("failed to query task", err))
		}
		return
	}

	// 查询任务详情统计
	details, err := h.service.GetTaskDetailsSummary(ctx, req.TaskID)
	if err != nil {
		logger.Errorf(ctx, "Failed to query task details: %v", err)
		common.HandleAppError(c, errors.Internal("failed to query task details", err))
		return
	}

	// 构建基础响应
	response := &AsyncTaskStatusResponse{
		TaskID:       task.TaskID,
		TaskType:     task.TaskType,
		TaskStatus:   task.TaskStatus,
		TotalCount:   task.TotalCount,
		SuccessCount: details.SuccessCount,
		FailedCount:  details.FailedCount,
		Progress:     calculateProgress(details.SuccessCount+details.FailedCount, task.TotalCount),
		ErrorMessage: task.ErrorMessage,
		CreatedAt:    task.CreatedAt.Format("2006-01-02 15:04:05"),
	}

	// 设置完成时间（如果存在）
	if task.CompletedTime != nil {
		response.CompletedTime = task.CompletedTime.Format("2006-01-02 15:04:05")
	}

	// 处理失败项详情（如果存在失败项）
	if details.FailedCount > 0 {
		h.attachFailedItems(ctx, req.TaskID, response)
	}

	common.SuccessResponse(c, "查询成功", response)
}

// attachFailedItems 附加失败项详情到响应中
func (h *AsyncTaskHandler) attachFailedItems(ctx context.Context, taskID string, response *AsyncTaskStatusResponse) {
	failedItems, err := h.service.GetFailedTaskDetails(ctx, taskID)
	if err != nil {
		// 获取失败项详情失败时，不影响主要响应，只记录日志
		logger.Warnf(ctx, "Failed to get failed task details: %v", err)
		return
	}

	response.FailedItems = make([]AsyncTaskDetailItem, len(failedItems))
	for i, item := range failedItems {
		response.FailedItems[i] = AsyncTaskDetailItem{
			ItemID:       item.ItemID,
			ItemName:     item.ItemName,
			Status:       item.Status,
			ErrorMessage: item.ErrorMessage,
		}
	}
}

// GetTaskDetail 获取任务详情
func (h *AsyncTaskHandler) GetTaskDetail(c *gin.Context) {
	ctx := c.Request.Context()
	taskID := c.Param("task_id")

	if taskID == "" {
		common.HandleAppError(c, errors.InvalidParam("task_id", "task_id is required"))
		return
	}

	task, err := h.service.GetTask(ctx, taskID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			common.HandleAppError(c, errors.NotFound("task"))
		} else {
			logger.Errorf(ctx, "Failed to get task detail: %v", err)
			common.HandleAppError(c, errors.Internal("failed to get task detail", err))
		}
		return
	}

	common.SuccessResponse(c, "查询成功", task)
}

// CancelTask 取消任务
func (h *AsyncTaskHandler) CancelTask(c *gin.Context) {
	ctx := c.Request.Context()
	taskID := c.Param("task_id")

	if taskID == "" {
		common.HandleAppError(c, errors.InvalidParam("task_id", "task_id is required"))
		return
	}

	logger.Infof(ctx, "Cancelling task: taskID=%s", taskID)
	err := h.service.CancelTask(ctx, taskID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			common.HandleAppError(c, errors.NotFound("task"))
		} else {
			logger.Errorf(ctx, "Failed to cancel task: %v", err)
			common.HandleAppError(c, errors.Internal("failed to cancel task", err))
		}
		return
	}

	logger.Infof(ctx, "Task cancelled successfully: taskID=%s", taskID)
	common.SuccessResponse(c, "任务已取消", nil)
}

// GetTaskDetails 获取任务详情列表
func (h *AsyncTaskHandler) GetTaskDetails(c *gin.Context) {
	ctx := c.Request.Context()
	taskID := c.Param("task_id")

	if taskID == "" {
		common.HandleAppError(c, errors.InvalidParam("task_id", "task_id is required"))
		return
	}

	details, err := h.service.GetTaskDetails(ctx, taskID)
	if err != nil {
		logger.Errorf(ctx, "Failed to get task details: %v", err)
		common.HandleAppError(c, errors.Internal("failed to get task details", err))
		return
	}

	common.SuccessResponse(c, "查询成功", details)
}

// getTotalCountFromParams 从请求参数中获取任务总数
func getTotalCountFromParams(taskType string, params map[string]interface{}) int {
	// 根据不同的任务类型解析总数
	switch taskType {
	case "BATCH_CREATE_NODE", "BATCH_UPDATE_NODE", "BATCH_DELETE_NODE", "BATCH_DEPLOY_NODE":
		// 获取nodes数组
		if nodes, ok := params["nodes"].([]interface{}); ok {
			return len(nodes)
		}
	}
	return 0
}

// calculateProgress 计算进度百分比
func calculateProgress(completed, total int) int {
	if total == 0 {
		return 0
	}
	return (completed * 100) / total
}
