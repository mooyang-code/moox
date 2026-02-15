package api

import (
	"github.com/gin-gonic/gin"
	"github.com/mooyang-code/moox/server/internal/service/monitor"
)

// RegisterRoutes 注册监控相关路由
func RegisterRoutes(router *gin.RouterGroup, monitorSvc monitor.Service) {
	handler := NewHandler(monitorSvc)

	// 监控管理
	monitorGroup := router.Group("/monitor")
	{
		monitorGroup.POST("/enable/:host_id", handler.EnableMonitor)
		monitorGroup.POST("/disable/:host_id", handler.DisableMonitor)
		monitorGroup.GET("/status/:host_id", handler.GetMonitorStatus)

		// 监控数据查询
		monitorGroup.GET("/current", handler.GetCurrentMetrics)
		monitorGroup.GET("/history/:host_address", handler.GetHistoryMetrics)

		// 连通性测试
		monitorGroup.POST("/test/:host_id", handler.TestNodeExporter)
	}
}
