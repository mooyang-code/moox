package api

import (
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"
	collectormgr "github.com/mooyang-code/moox/server/internal/service/collector/manager"

	"github.com/gin-gonic/gin"
	"trpc.group/trpc-go/trpc-go/log"
)

// RegisterCollectorRoutes 注册采集器相关路由
func RegisterCollectorRoutes(router *gin.RouterGroup, serviceFactory *collectormgr.ServiceFactory, getCloudProvider func(string) provider.Client) {
	// 采集任务配置路由
	taskConfigService := serviceFactory.CreateTaskConfigService(getCloudProvider)
	taskConfigHandler := NewCollectorTaskConfigHandler(taskConfigService)
	taskConfigGroup := router.Group("/task-config")
	{
		taskConfigGroup.GET("/list", taskConfigHandler.GetTaskConfigList)
		taskConfigGroup.GET("/:id", taskConfigHandler.GetTaskConfigDetail)
		taskConfigGroup.POST("/create", taskConfigHandler.CreateTaskConfig)
		taskConfigGroup.PUT("/:id", taskConfigHandler.UpdateTaskConfig)
		taskConfigGroup.DELETE("/:id", taskConfigHandler.DeleteTaskConfig)
	}

	// 采集任务实例路由
	taskInstanceService := serviceFactory.CreateTaskInstanceService()
	taskInstanceHandler := NewCollectorTaskInstanceHandler(taskInstanceService)
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
	nodeTasksHandler := NewNodeTasksHandler()
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
