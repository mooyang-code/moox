package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/mooyang-code/moox/server/internal/service/collector/logic"
	"github.com/mooyang-code/moox/server/internal/service/collector/model"
	"gorm.io/gorm"
)

// CollectorTaskConfigHandler 采集任务配置处理器
type CollectorTaskConfigHandler struct {
	service logic.CollectorTaskConfigService
}

// NewCollectorTaskConfigHandler 创建采集任务配置处理器
func NewCollectorTaskConfigHandler(db *gorm.DB) *CollectorTaskConfigHandler {
	return &CollectorTaskConfigHandler{
		service: logic.NewCollectorTaskConfigService(db),
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
		ErrorResponse(c, http.StatusInternalServerError, "查询失败", err)
		return
	}

	SuccessResponse(c, "查询成功", configs)
}

// GetTaskConfigDetail 获取任务配置详情
func (h *CollectorTaskConfigHandler) GetTaskConfigDetail(c *gin.Context) {
	taskID := c.Param("id")
	if taskID == "" {
		ErrorResponse(c, http.StatusBadRequest, "ID参数不能为空", nil)
		return
	}

	// 调用service层获取数据
	config, err := h.service.GetTaskConfig(c.Request.Context(), taskID)
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "任务配置不存在", err)
		return
	}

	SuccessResponse(c, "查询成功", config)
}

// CreateTaskConfig 创建任务配置
func (h *CollectorTaskConfigHandler) CreateTaskConfig(c *gin.Context) {
	var config model.CollectorTaskConfig
	if err := c.ShouldBindJSON(&config); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "参数绑定失败", err)
		return
	}

	// 调用service层创建数据
	err := h.service.CreateTaskConfig(c.Request.Context(), &config)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "创建失败", err)
		return
	}

	SuccessResponse(c, "创建成功", config)
}

// UpdateTaskConfig 更新任务配置
func (h *CollectorTaskConfigHandler) UpdateTaskConfig(c *gin.Context) {
	taskID := c.Param("id")
	if taskID == "" {
		ErrorResponse(c, http.StatusBadRequest, "ID参数不能为空", nil)
		return
	}

	var config model.CollectorTaskConfig
	if err := c.ShouldBindJSON(&config); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "参数绑定失败", err)
		return
	}

	// 设置ID
	config.TaskID = taskID

	// 调用service层更新数据
	err := h.service.UpdateTaskConfig(c.Request.Context(), &config)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "更新失败", err)
		return
	}

	SuccessResponse(c, "更新成功", config)
}

// DeleteTaskConfig 删除任务配置
func (h *CollectorTaskConfigHandler) DeleteTaskConfig(c *gin.Context) {
	taskID := c.Param("id")
	if taskID == "" {
		ErrorResponse(c, http.StatusBadRequest, "ID参数不能为空", nil)
		return
	}

	// 调用service层删除数据
	err := h.service.RemoveTaskConfig(c.Request.Context(), taskID)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "删除失败", err)
		return
	}

	SuccessResponse(c, "删除成功", nil)
}