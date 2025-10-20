package api

import (
	apperrors "github.com/mooyang-code/moox/server/internal/errors"
	collectorimpl "github.com/mooyang-code/moox/server/internal/service/collector/impl"
	"github.com/mooyang-code/moox/server/internal/service/collector/model"

	"github.com/gin-gonic/gin"
)

// CollectorTaskConfigHandler 采集任务配置处理器
type CollectorTaskConfigHandler struct {
	service collectorimpl.TaskConfigService
}

// NewCollectorTaskConfigHandler 创建采集任务配置处理器
func NewCollectorTaskConfigHandler(service collectorimpl.TaskConfigService) *CollectorTaskConfigHandler {
	return &CollectorTaskConfigHandler{
		service: service,
	}
}

// GetTaskConfigList 获取任务配置列表
func (h *CollectorTaskConfigHandler) GetTaskConfigList(c *gin.Context) {
	// 获取查询参数
	projectID := c.Query("project_id")
	datasetID := c.Query("dataset_id")

	// 调用service层获取数据
	configs, err := h.service.GetTaskConfigList(c.Request.Context(), projectID, datasetID)
	if err != nil {
		HandleAppError(c, apperrors.Internal("查询失败", err))
		return
	}

	// 计算总数
	total := int64(len(configs))

	// 使用新的分页列表响应格式
	PaginatedListResponse(c, "查询成功", configs, total)
}

// GetTaskConfigDetail 获取任务配置详情
func (h *CollectorTaskConfigHandler) GetTaskConfigDetail(c *gin.Context) {
	taskID := c.Param("id")
	if taskID == "" {
		HandleAppError(c, apperrors.InvalidParam("request", "ID参数不能为空"))
		return
	}

	// 调用service层获取数据
	config, err := h.service.GetTaskConfig(c.Request.Context(), taskID)
	if err != nil {
		HandleAppError(c, apperrors.NotFound("任务配置"))
		return
	}

	SuccessResponse(c, "查询成功", []interface{}{config})
}

// CreateTaskConfig 创建任务配置
func (h *CollectorTaskConfigHandler) CreateTaskConfig(c *gin.Context) {
	var config model.CollectorTaskConfig
	if err := c.ShouldBindJSON(&config); err != nil {
		HandleAppError(c, apperrors.InvalidParam("request", "参数绑定失败"))
		return
	}

	// 调用service层创建数据
	err := h.service.CreateTaskConfig(c.Request.Context(), &config)
	if err != nil {
		HandleAppError(c, apperrors.Internal("创建失败", err))
		return
	}

	SuccessResponse(c, "创建成功", []interface{}{config})
}

// UpdateTaskConfig 更新任务配置
func (h *CollectorTaskConfigHandler) UpdateTaskConfig(c *gin.Context) {
	taskID := c.Param("id")
	if taskID == "" {
		HandleAppError(c, apperrors.InvalidParam("request", "ID参数不能为空"))
		return
	}

	var config model.CollectorTaskConfig
	if err := c.ShouldBindJSON(&config); err != nil {
		HandleAppError(c, apperrors.InvalidParam("request", "参数绑定失败"))
		return
	}

	// 设置ID
	config.TaskID = taskID

	// 调用service层更新数据
	err := h.service.UpdateTaskConfig(c.Request.Context(), &config)
	if err != nil {
		HandleAppError(c, apperrors.Internal("更新失败", err))
		return
	}

	SuccessResponse(c, "更新成功", []interface{}{config})
}

// DeleteTaskConfig 删除任务配置
func (h *CollectorTaskConfigHandler) DeleteTaskConfig(c *gin.Context) {
	taskID := c.Param("id")
	if taskID == "" {
		HandleAppError(c, apperrors.InvalidParam("request", "ID参数不能为空"))
		return
	}

	// 调用service层删除数据
	err := h.service.RemoveTaskConfig(c.Request.Context(), taskID)
	if err != nil {
		HandleAppError(c, apperrors.Internal("删除失败", err))
		return
	}

	SuccessResponse(c, "删除成功", []interface{}{})
}
