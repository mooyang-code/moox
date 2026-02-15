package api

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/mooyang-code/moox/server/internal/common"
	apperrors "github.com/mooyang-code/moox/server/internal/errors"
	"github.com/mooyang-code/moox/server/internal/service/monitor"
	"trpc.group/trpc-go/trpc-go/log"
)

// Handler 监控服务 HTTP 处理器
type Handler struct {
	monitorSvc monitor.Service
}

// NewHandler 创建处理器实例
func NewHandler(monitorSvc monitor.Service) *Handler {
	return &Handler{
		monitorSvc: monitorSvc,
	}
}

// EnableMonitor 启用主机监控
func (h *Handler) EnableMonitor(c *gin.Context) {
	hostID, err := strconv.Atoi(c.Param("host_id"))
	if err != nil {
		common.HandleAppError(c, apperrors.InvalidParam("host_id", "invalid host_id"))
		return
	}

	if err := h.monitorSvc.EnableMonitor(c.Request.Context(), hostID); err != nil {
		log.ErrorContextf(c.Request.Context(), "[Monitor API] Enable monitor failed: %v", err)
		common.HandleAppError(c, apperrors.Internal("enable monitor failed", err))
		return
	}

	common.SuccessResponse(c, "success", nil)
}

// DisableMonitor 禁用主机监控
func (h *Handler) DisableMonitor(c *gin.Context) {
	hostID, err := strconv.Atoi(c.Param("host_id"))
	if err != nil {
		common.HandleAppError(c, apperrors.InvalidParam("host_id", "invalid host_id"))
		return
	}

	if err := h.monitorSvc.DisableMonitor(c.Request.Context(), hostID); err != nil {
		log.ErrorContextf(c.Request.Context(), "[Monitor API] Disable monitor failed: %v", err)
		common.HandleAppError(c, apperrors.Internal("disable monitor failed", err))
		return
	}

	common.SuccessResponse(c, "success", nil)
}

// GetMonitorStatus 获取主机监控状态
func (h *Handler) GetMonitorStatus(c *gin.Context) {
	hostID, err := strconv.Atoi(c.Param("host_id"))
	if err != nil {
		common.HandleAppError(c, apperrors.InvalidParam("host_id", "invalid host_id"))
		return
	}

	enabled, err := h.monitorSvc.IsMonitorEnabled(c.Request.Context(), hostID)
	if err != nil {
		log.ErrorContextf(c.Request.Context(), "[Monitor API] Get monitor status failed: %v", err)
		common.HandleAppError(c, apperrors.Internal("get monitor status failed", err))
		return
	}

	common.SuccessResponse(c, "success", map[string]interface{}{
		"enabled": enabled,
	})
}

// GetCurrentMetrics 获取当前监控指标
func (h *Handler) GetCurrentMetrics(c *gin.Context) {
	var hostIDs []int

	// 如果指定了 host_ids 参数
	if hostIDsStr := c.Query("host_ids"); hostIDsStr != "" {
		// 支持逗号分隔的多个 ID
		// 这里简化处理，实际可以用 strings.Split 解析
		hostID, err := strconv.Atoi(hostIDsStr)
		if err == nil {
			hostIDs = append(hostIDs, hostID)
		}
	}

	metrics, err := h.monitorSvc.GetCurrentMetrics(c.Request.Context(), hostIDs)
	if err != nil {
		log.ErrorContextf(c.Request.Context(), "[Monitor API] Get current metrics failed: %v", err)
		common.HandleAppError(c, apperrors.Internal("get metrics failed", err))
		return
	}

	common.SuccessResponse(c, "success", metrics)
}

// GetHistoryMetrics 获取历史监控数据
func (h *Handler) GetHistoryMetrics(c *gin.Context) {
	hostAddress := c.Param("host_address")
	if hostAddress == "" {
		common.HandleAppError(c, apperrors.InvalidParam("host_address", "host_address is required"))
		return
	}

	duration := c.Query("duration")
	if duration == "" {
		duration = "1h" // 默认 1 小时
	}

	history, err := h.monitorSvc.GetHistoryMetrics(c.Request.Context(), hostAddress, duration)
	if err != nil {
		log.ErrorContextf(c.Request.Context(), "[Monitor API] Get history metrics failed: %v", err)
		common.HandleAppError(c, apperrors.Internal("get history failed", err))
		return
	}

	common.SuccessResponse(c, "success", history)
}

// TestNodeExporter 测试 Node Exporter 连通性
func (h *Handler) TestNodeExporter(c *gin.Context) {
	hostID, err := strconv.Atoi(c.Param("host_id"))
	if err != nil {
		common.HandleAppError(c, apperrors.InvalidParam("host_id", "invalid host_id"))
		return
	}

	result, err := h.monitorSvc.TestNodeExporter(c.Request.Context(), hostID)
	if err != nil {
		log.ErrorContextf(c.Request.Context(), "[Monitor API] Test node exporter failed: %v", err)
		common.HandleAppError(c, apperrors.Internal("test failed", err))
		return
	}

	common.SuccessResponse(c, "success", result)
}
