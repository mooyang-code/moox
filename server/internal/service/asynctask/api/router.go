package api

import (
	"github.com/mooyang-code/moox/server/internal/service/asynctask"
	"github.com/mooyang-code/moox/server/internal/service/asynctask/logic"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// RegisterAsyncTaskRoutes 注册异步任务相关路由
func RegisterAsyncTaskRoutes(router *gin.RouterGroup, db *gorm.DB) {
	// 创建异步任务服务
	service := logic.NewService(db)
	RegisterAsyncTaskRoutesWithService(router, service)
}

// RegisterAsyncTaskRoutesWithService 使用指定的异步任务服务注册路由
func RegisterAsyncTaskRoutesWithService(router *gin.RouterGroup, service asynctask.Service) {

	// 创建异步任务处理器
	taskHandler := NewAsyncTaskHandler(service)

	// 注册异步任务路由组
	taskGroup := router.Group("/async-task")
	{
		taskGroup.POST("/create", taskHandler.CreateTask)
		taskGroup.GET("/query", taskHandler.QueryTask)
		taskGroup.GET("/:task_id", taskHandler.GetTaskDetail)
		taskGroup.POST("/:task_id/cancel", taskHandler.CancelTask)
		taskGroup.GET("/:task_id/details", taskHandler.GetTaskDetails)
	}

	log.Info("[AsyncTask] 路由注册完成")
}
