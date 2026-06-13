package api

import (
	"github.com/mooyang-code/moox/modules/control/internal/service/asynctask"

	"github.com/gin-gonic/gin"
	"trpc.group/trpc-go/trpc-go/log"
)

// RegisterRoutes 注册异步任务相关的HTTP路由
func RegisterRoutes(router *gin.RouterGroup, service asynctask.Service) {
	// 创建HTTP处理器
	handler := NewAsyncJobHandler(service)

	// 注册路由
	taskGroup := router.Group("/async")
	{
		taskGroup.POST("/jobs", handler.CreateJob)
		taskGroup.GET("/jobs/:job_id", handler.QueryJob)
	}
	log.Info("[AsyncTask] HTTP路由注册完成")
}
