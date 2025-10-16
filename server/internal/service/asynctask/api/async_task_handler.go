package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/mooyang-code/moox/server/internal/service/asynctask/logic"
)

// AsyncTaskHandler 异步任务处理器
type AsyncTaskHandler struct {
	service logic.AsyncTaskService
}

// NewAsyncTaskHandler 创建异步任务处理器
func NewAsyncTaskHandler(service logic.AsyncTaskService) *AsyncTaskHandler {
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
	var req CreateTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "参数绑定失败", err)
		return
	}

	// 生成唯一的任务ID
	taskID := uuid.New().String()

	// 根据任务类型获取总数
	totalCount := getTotalCountFromParams(req.TaskType, req.RequestParams)
	if totalCount <= 0 {
		ErrorResponse(c, http.StatusBadRequest, "无效的任务参数", nil)
		return
	}

	// 创建并执行任务
	err := h.service.CreateAndExecuteTask(c.Request.Context(), taskID, req.TaskType, totalCount, req.RequestParams)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "任务创建失败", err)
		return
	}

	response := map[string]interface{}{
		"task_id":     taskID,
		"task_type":   req.TaskType,
		"total_count": totalCount,
	}

	SuccessResponse(c, "任务创建成功", response)
}

// QueryTaskRequest 查询任务请求
type QueryTaskRequest struct {
	TaskID string `form:"task_id" binding:"required"`
}

// QueryTask 查询任务状态
func (h *AsyncTaskHandler) QueryTask(c *gin.Context) {
	var req QueryTaskRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "参数绑定失败", err)
		return
	}

	// 查询任务状态
	task, err := h.service.GetTask(c.Request.Context(), req.TaskID)
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "任务不存在", err)
		return
	}

	// 查询任务详情统计
	details, err := h.service.GetTaskDetailsSummary(c.Request.Context(), req.TaskID)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "查询任务详情失败", err)
		return
	}

	// 构建响应
	response := &AsyncTaskStatusResponse{
		TaskID:        task.TaskID,
		TaskType:      task.TaskType,
		TaskStatus:    task.TaskStatus,
		TotalCount:    task.TotalCount,
		SuccessCount:  details.SuccessCount,
		FailedCount:   details.FailedCount,
		Progress:      calculateProgress(details.SuccessCount+details.FailedCount, task.TotalCount),
		ErrorMessage:  task.ErrorMessage,
		CreatedAt:     task.CreatedAt.Format("2006-01-02 15:04:05"),
	}

	if task.CompletedTime != nil {
		completedTime := task.CompletedTime.Format("2006-01-02 15:04:05")
		response.CompletedTime = completedTime
	}

	// 如果有失败项，获取失败详情
	if details.FailedCount > 0 {
		failedItems, err := h.service.GetFailedTaskDetails(c.Request.Context(), req.TaskID)
		if err == nil {
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
	}

	SuccessResponse(c, "查询成功", response)
}

// GetTaskDetail 获取任务详情
func (h *AsyncTaskHandler) GetTaskDetail(c *gin.Context) {
	taskID := c.Param("task_id")
	if taskID == "" {
		ErrorResponse(c, http.StatusBadRequest, "任务ID不能为空", nil)
		return
	}

	task, err := h.service.GetTask(c.Request.Context(), taskID)
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "任务不存在", err)
		return
	}

	SuccessResponse(c, "查询成功", task)
}

// CancelTask 取消任务
func (h *AsyncTaskHandler) CancelTask(c *gin.Context) {
	taskID := c.Param("task_id")
	if taskID == "" {
		ErrorResponse(c, http.StatusBadRequest, "任务ID不能为空", nil)
		return
	}

	err := h.service.CancelTask(c.Request.Context(), taskID)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "取消任务失败", err)
		return
	}

	SuccessResponse(c, "任务已取消", nil)
}

// GetTaskDetails 获取任务详情列表
func (h *AsyncTaskHandler) GetTaskDetails(c *gin.Context) {
	taskID := c.Param("task_id")
	if taskID == "" {
		ErrorResponse(c, http.StatusBadRequest, "任务ID不能为空", nil)
		return
	}

	details, err := h.service.GetTaskDetails(c.Request.Context(), taskID)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "查询任务详情失败", err)
		return
	}

	SuccessResponse(c, "查询成功", details)
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