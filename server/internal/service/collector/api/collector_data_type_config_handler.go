package api

import (
	apperrors "github.com/mooyang-code/moox/server/internal/errors"
	"github.com/mooyang-code/moox/server/internal/service/collector"

	"github.com/gin-gonic/gin"
)

// CollectorDataTypeConfigHandler 采集器数据类型配置处理器
type CollectorDataTypeConfigHandler struct {
	service collector.DataTypeConfigService
}

// NewCollectorDataTypeConfigHandler 创建采集器数据类型配置处理器
func NewCollectorDataTypeConfigHandler(service collector.DataTypeConfigService) *CollectorDataTypeConfigHandler {
	return &CollectorDataTypeConfigHandler{
		service: service,
	}
}

// GetDataTypeConfigs 获取所有数据类型配置
func (h *CollectorDataTypeConfigHandler) GetDataTypeConfigs(c *gin.Context) {
	configs, err := h.service.GetDataTypeConfigs(c.Request.Context())
	if err != nil {
		HandleAppError(c, apperrors.Internal("获取数据类型配置失败", err))
		return
	}

	SuccessResponse(c, "查询成功", configs)
}

// GetDataTypeConfigWithFields 获取数据类型配置及字段信息
func (h *CollectorDataTypeConfigHandler) GetDataTypeConfigWithFields(c *gin.Context) {
	dataType := c.Param("data_type")
	if dataType == "" {
		HandleAppError(c, apperrors.InvalidParam("request", "数据类型参数不能为空"))
		return
	}

	config, err := h.service.GetDataTypeConfigWithFields(c.Request.Context(), dataType)
	if err != nil {
		HandleAppError(c, apperrors.Internal("获取数据类型配置失败", err))
		return
	}

	SuccessResponse(c, "查询成功", []interface{}{config})
}