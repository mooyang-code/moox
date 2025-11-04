package api

import (
	apperrors "github.com/mooyang-code/moox/server/internal/errors"
	"github.com/mooyang-code/moox/server/internal/service/collector"

	"github.com/gin-gonic/gin"
)

// CollectorTaskRuleHandler 采集任务规则处理器
type CollectorTaskRuleHandler struct {
	service collector.TaskRuleService
}

// NewCollectorTaskRuleHandler 创建采集任务规则处理器
func NewCollectorTaskRuleHandler(service collector.TaskRuleService) *CollectorTaskRuleHandler {
	return &CollectorTaskRuleHandler{
		service: service,
	}
}

// GetTaskRuleList 获取任务规则列表
func (h *CollectorTaskRuleHandler) GetTaskRuleList(c *gin.Context) {
	// 获取查询参数
	dataType := c.Query("data_type")
	dataSource := c.Query("data_source")

	// 调用service层获取数据
	configs, err := h.service.GetTaskRuleList(c.Request.Context(), dataType, dataSource)
	if err != nil {
		HandleAppError(c, apperrors.Internal("查询失败", err))
		return
	}

	// 计算总数
	total := int64(len(configs))

	// 使用新的分页列表响应格式
	PaginatedListResponse(c, "查询成功", configs, total)
}

// GetTaskRuleDetail 获取任务规则详情
func (h *CollectorTaskRuleHandler) GetTaskRuleDetail(c *gin.Context) {
	ruleID := c.Param("id")
	if ruleID == "" {
		HandleAppError(c, apperrors.InvalidParam("request", "ID参数不能为空"))
		return
	}

	// 调用service层获取数据
	config, err := h.service.GetTaskRule(c.Request.Context(), ruleID)
	if err != nil {
		HandleAppError(c, apperrors.NotFound("任务配置"))
		return
	}

	SuccessResponse(c, "查询成功", []interface{}{config})
}

// CreateTaskRule 创建任务规则
func (h *CollectorTaskRuleHandler) CreateTaskRule(c *gin.Context) {
	var config collector.TaskRuleDTO
	if err := c.ShouldBindJSON(&config); err != nil {
		HandleAppError(c, apperrors.InvalidParam("request", "参数绑定失败"))
		return
	}

	// 调用service层创建数据
	err := h.service.CreateTaskRule(c.Request.Context(), &config)
	if err != nil {
		HandleAppError(c, apperrors.Internal("创建失败", err))
		return
	}

	SuccessResponse(c, "创建成功", []interface{}{config})
}

// UpdateTaskRule 更新任务规则
func (h *CollectorTaskRuleHandler) UpdateTaskRule(c *gin.Context) {
	ruleID := c.Param("id")
	if ruleID == "" {
		HandleAppError(c, apperrors.InvalidParam("request", "ID参数不能为空"))
		return
	}

	var config collector.TaskRuleDTO
	if err := c.ShouldBindJSON(&config); err != nil {
		HandleAppError(c, apperrors.InvalidParam("request", "参数绑定失败"))
		return
	}

	// 设置RuleID
	config.RuleID = ruleID

	// 调用service层更新数据
	err := h.service.UpdateTaskRule(c.Request.Context(), &config)
	if err != nil {
		HandleAppError(c, apperrors.Internal("更新失败", err))
		return
	}

	SuccessResponse(c, "更新成功", []interface{}{config})
}

// DeleteTaskRule 删除任务规则
func (h *CollectorTaskRuleHandler) DeleteTaskRule(c *gin.Context) {
	ruleID := c.Param("id")
	if ruleID == "" {
		HandleAppError(c, apperrors.InvalidParam("request", "ID参数不能为空"))
		return
	}

	// 调用service层删除数据
	err := h.service.RemoveTaskRule(c.Request.Context(), ruleID)
	if err != nil {
		HandleAppError(c, apperrors.Internal("删除失败", err))
		return
	}

	SuccessResponse(c, "删除成功", []interface{}{})
}
