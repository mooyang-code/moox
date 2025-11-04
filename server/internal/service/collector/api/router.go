package api

import (
	"github.com/mooyang-code/moox/server/internal/service/collector"

	"github.com/gin-gonic/gin"
	"trpc.group/trpc-go/trpc-go/log"
)

// RegisterCollectorRoutes 注册采集器相关路由
func RegisterCollectorRoutes(router *gin.RouterGroup, taskRuleService collector.TaskRuleService, taskInstanceService collector.TaskInstanceService) {
	// 采集任务规则路由
	taskRuleHandler := NewCollectorTaskRuleHandler(taskRuleService)
	taskRuleGroup := router.Group("/task-rule")
	{
		taskRuleGroup.GET("/list", taskRuleHandler.GetTaskRuleList)
		taskRuleGroup.GET("/:id", taskRuleHandler.GetTaskRuleDetail)
		taskRuleGroup.POST("/create", taskRuleHandler.CreateTaskRule)
		taskRuleGroup.PUT("/:id", taskRuleHandler.UpdateTaskRule)
		taskRuleGroup.DELETE("/:id", taskRuleHandler.DeleteTaskRule)
	}

	// 采集任务实例路由
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

	log.Info("[Collector] 采集器任务规则和任务实例路由注册完成")
}
