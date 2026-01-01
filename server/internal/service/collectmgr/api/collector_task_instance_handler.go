package api

import (
	"strconv"

	apperrors "github.com/mooyang-code/moox/server/internal/errors"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr"

	"github.com/gin-gonic/gin"
)

// CollectorTaskInstanceHandler 采集任务实例处理器
type CollectorTaskInstanceHandler struct {
	service collectmgr.TaskInstanceService
}

// NewCollectorTaskInstanceHandler 创建采集任务实例处理器
func NewCollectorTaskInstanceHandler(service collectmgr.TaskInstanceService) *CollectorTaskInstanceHandler {
	return &CollectorTaskInstanceHandler{
		service: service,
	}
}

// GetTaskInstanceList 获取任务实例列表（支持分页和筛选）
func (h *CollectorTaskInstanceHandler) GetTaskInstanceList(c *gin.Context) {
	// 获取查询参数
	taskID := c.Query("task_id")
	ruleID := c.Query("rule_id")
	nodeID := c.Query("node_id")
	symbol := c.Query("symbol")

	// 解析分页参数
	page := 1
	size := 10

	if pageStr := c.Query("page"); pageStr != "" {
		if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
			page = p
		}
	}
	if sizeStr := c.Query("size"); sizeStr != "" {
		if s, err := strconv.Atoi(sizeStr); err == nil && s > 0 {
			size = s
		}
	}

	// 解析状态参数
	var status *int
	if statusStr := c.Query("status"); statusStr != "" {
		if s, err := strconv.Atoi(statusStr); err == nil {
			status = &s
		}
	}

	// 解析invalid参数
	var invalid *int
	if invalidStr := c.Query("invalid"); invalidStr != "" {
		if inv, err := strconv.Atoi(invalidStr); err == nil {
			invalid = &inv
		}
	}

	// 构建筛选器
	filter := &collectmgr.TaskInstanceFilterDTO{
		TaskID:   taskID,
		RuleID:   ruleID,
		NodeID:   nodeID,
		Symbol:   symbol,
		Status:   status,
		Invalid:  invalid,
		Page:     page,
		PageSize: size,
	}

	// 调用 service 层分页查询
	instances, total, err := h.service.ListTaskInstancesWithFilter(c.Request.Context(), filter)
	if err != nil {
		HandleAppError(c, apperrors.Internal("查询失败", err))
		return
	}

	// 使用分页列表响应格式
	PaginatedListResponse(c, "查询成功", instances, total)
}

// GetTaskInstanceDetail 获取任务实例详情
func (h *CollectorTaskInstanceHandler) GetTaskInstanceDetail(c *gin.Context) {
	instanceID := c.Param("id")
	if instanceID == "" {
		HandleAppError(c, apperrors.InvalidParam("request", "ID参数不能为空"))
		return
	}

	// 调用service层获取数据
	instance, err := h.service.GetTaskInstance(c.Request.Context(), instanceID)
	if err != nil {
		HandleAppError(c, apperrors.NotFound("任务实例"))
		return
	}

	SuccessResponse(c, "查询成功", []interface{}{instance})
}

// CreateTaskInstance 创建任务实例
func (h *CollectorTaskInstanceHandler) CreateTaskInstance(c *gin.Context) {
	var instance collectmgr.TaskInstanceDTO
	if err := c.ShouldBindJSON(&instance); err != nil {
		HandleAppError(c, apperrors.InvalidParam("request", "参数绑定失败"))
		return
	}

	// 调用service层创建数据
	err := h.service.CreateTaskInstance(c.Request.Context(), &instance)
	if err != nil {
		HandleAppError(c, apperrors.Internal("创建失败", err))
		return
	}

	SuccessResponse(c, "创建成功", []interface{}{instance})
}

// UpdateTaskInstance 更新任务实例
func (h *CollectorTaskInstanceHandler) UpdateTaskInstance(c *gin.Context) {
	instanceID := c.Param("id")
	if instanceID == "" {
		HandleAppError(c, apperrors.InvalidParam("request", "ID参数不能为空"))
		return
	}

	var instance collectmgr.TaskInstanceDTO
	if err := c.ShouldBindJSON(&instance); err != nil {
		HandleAppError(c, apperrors.InvalidParam("request", "参数绑定失败"))
		return
	}

	// 调用service层更新数据
	err := h.service.UpdateTaskInstance(c.Request.Context(), instanceID, &instance)
	if err != nil {
		HandleAppError(c, apperrors.Internal("更新失败", err))
		return
	}

	SuccessResponse(c, "更新成功", []interface{}{instance})
}

// DeleteTaskInstance 删除任务实例
func (h *CollectorTaskInstanceHandler) DeleteTaskInstance(c *gin.Context) {
	instanceID := c.Param("id")
	if instanceID == "" {
		HandleAppError(c, apperrors.InvalidParam("request", "ID参数不能为空"))
		return
	}

	// 调用service层删除数据
	err := h.service.RemoveTaskInstance(c.Request.Context(), instanceID)
	if err != nil {
		HandleAppError(c, apperrors.Internal("删除失败", err))
		return
	}

	SuccessResponse(c, "删除成功", []interface{}{})
}

// StartTaskInstance 启动任务实例
func (h *CollectorTaskInstanceHandler) StartTaskInstance(c *gin.Context) {
	instanceID := c.Param("id")
	if instanceID == "" {
		HandleAppError(c, apperrors.InvalidParam("request", "ID参数不能为空"))
		return
	}

	// 调用service层启动任务
	err := h.service.StartInstance(c.Request.Context(), instanceID)
	if err != nil {
		HandleAppError(c, apperrors.Internal("启动失败", err))
		return
	}

	SuccessResponse(c, "启动成功", []interface{}{})
}

// StopTaskInstance 停止任务实例
func (h *CollectorTaskInstanceHandler) StopTaskInstance(c *gin.Context) {
	instanceID := c.Param("id")
	if instanceID == "" {
		HandleAppError(c, apperrors.InvalidParam("request", "ID参数不能为空"))
		return
	}

	// 调用service层停止任务
	err := h.service.CompleteInstance(c.Request.Context(), instanceID, false, "手动停止")
	if err != nil {
		HandleAppError(c, apperrors.Internal("停止失败", err))
		return
	}

	SuccessResponse(c, "停止成功", []interface{}{})
}

// ReportTaskStatusRequest 上报任务状态请求
type ReportTaskStatusRequest struct {
	Status int    `json:"status" binding:"required"` // 状态码（0=待执行，1=执行中，2=成功，3=部分失败，4=失败）
	Result string `json:"result"`                    // 执行结果（可选）
}

// ReportTaskStatus 上报任务状态（客户端上报用）
func (h *CollectorTaskInstanceHandler) ReportTaskStatus(c *gin.Context) {
	instanceID := c.Param("id")
	if instanceID == "" {
		HandleAppError(c, apperrors.InvalidParam("request", "ID参数不能为空"))
		return
	}

	var req ReportTaskStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		HandleAppError(c, apperrors.InvalidParam("request", "参数绑定失败"))
		return
	}

	// 调用service层上报状态
	err := h.service.ReportTaskStatus(c.Request.Context(), instanceID, req.Status, req.Result)
	if err != nil {
		HandleAppError(c, apperrors.Internal("状态上报失败", err))
		return
	}

	SuccessResponse(c, "状态上报成功", []interface{}{})
}

// InvalidateTaskInstanceRequest 作废任务实例请求
type InvalidateTaskInstanceRequest struct {
	TaskID string `json:"task_id" binding:"required"` // 任务ID
}

// InvalidateTaskInstance 作废任务实例
func (h *CollectorTaskInstanceHandler) InvalidateTaskInstance(c *gin.Context) {
	var req InvalidateTaskInstanceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		HandleAppError(c, apperrors.InvalidParam("request", "参数绑定失败："+err.Error()))
		return
	}

	if req.TaskID == "" {
		HandleAppError(c, apperrors.InvalidParam("task_id", "任务ID不能为空"))
		return
	}

	// 调用service层作废任务
	err := h.service.InvalidateTaskInstance(c.Request.Context(), req.TaskID)
	if err != nil {
		HandleAppError(c, apperrors.Internal("作废任务失败", err))
		return
	}

	SuccessResponse(c, "作废任务成功", []interface{}{})
}
