package api

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// RegisterCollectorRoutes 注册采集器相关路由
func RegisterCollectorRoutes(router *gin.RouterGroup, db *gorm.DB) {
	// 采集任务配置路由
	taskConfigHandler := NewCollectorTaskConfigHandler(db)
	taskConfigGroup := router.Group("/task-config")
	{
		taskConfigGroup.GET("/list", taskConfigHandler.GetTaskConfigList)
		taskConfigGroup.GET("/:id", taskConfigHandler.GetTaskConfigDetail)
		taskConfigGroup.POST("/create", taskConfigHandler.CreateTaskConfig)
		taskConfigGroup.PUT("/:id", taskConfigHandler.UpdateTaskConfig)
		taskConfigGroup.DELETE("/:id", taskConfigHandler.DeleteTaskConfig)
	}

	// 采集任务实例路由
	taskInstanceHandler := NewCollectorTaskInstanceHandler(db)
	taskInstanceGroup := router.Group("/task-instance")
	{
		taskInstanceGroup.GET("/list", taskInstanceHandler.GetTaskInstanceList)
		taskInstanceGroup.GET("/:id", taskInstanceHandler.GetTaskInstanceDetail)
		taskInstanceGroup.POST("/create", taskInstanceHandler.CreateTaskInstance)
		taskInstanceGroup.PUT("/:id", taskInstanceHandler.UpdateTaskInstance)
		taskInstanceGroup.DELETE("/:id", taskInstanceHandler.DeleteTaskInstance)
		taskInstanceGroup.POST("/:id/start", taskInstanceHandler.StartTaskInstance)
		taskInstanceGroup.POST("/:id/stop", taskInstanceHandler.StopTaskInstance)
	}

	// 节点任务路由
	nodeTasksHandler := NewNodeTasksHandler(db)
	nodeTasksGroup := router.Group("/node-tasks")
	{
		nodeTasksGroup.GET("/list", nodeTasksHandler.GetNodeTasksList)
		nodeTasksGroup.GET("/:id", nodeTasksHandler.GetNodeTasksDetail)
		nodeTasksGroup.POST("/create", nodeTasksHandler.CreateNodeTasks)
		nodeTasksGroup.PUT("/:id", nodeTasksHandler.UpdateNodeTasks)
		nodeTasksGroup.DELETE("/:id", nodeTasksHandler.DeleteNodeTasks)
	}

	log.Info("[Collector] 路由注册完成")
}