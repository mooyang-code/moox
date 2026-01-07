package api

import (
	"github.com/mooyang-code/moox/server/internal/service/collectmgr"

	"github.com/gin-gonic/gin"
	"trpc.group/trpc-go/trpc-go/log"
)

// RegisterCollectorRoutes 注册采集器相关路由
func RegisterCollectorRoutes(router *gin.RouterGroup, taskRuleService collectmgr.TaskRuleService, taskInstanceService collectmgr.TaskInstanceService, dataTypeConfigService collectmgr.DataTypeConfigService, taskPlannerService collectmgr.TaskPlannerService) {
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
		taskInstanceGroup.GET("/cache/list", taskInstanceHandler.GetTaskInstanceListCache)
		taskInstanceGroup.GET("/:id", taskInstanceHandler.GetTaskInstanceDetail)
		taskInstanceGroup.POST("/create", taskInstanceHandler.CreateTaskInstance)
		taskInstanceGroup.PUT("/:id", taskInstanceHandler.UpdateTaskInstance)
		taskInstanceGroup.DELETE("/:id", taskInstanceHandler.DeleteTaskInstance)
		taskInstanceGroup.POST("/:id/start", taskInstanceHandler.StartTaskInstance)
		taskInstanceGroup.POST("/:id/stop", taskInstanceHandler.StopTaskInstance)
		taskInstanceGroup.POST("/:id/report-status", taskInstanceHandler.ReportTaskStatus)
		taskInstanceGroup.POST("/invalidate", taskInstanceHandler.InvalidateTaskInstance)
	}

	// 采集器数据类型配置路由
	dataTypeConfigHandler := NewCollectorDataTypeConfigHandler(dataTypeConfigService)
	dataTypeConfigGroup := router.Group("/data-type-config")
	{
		dataTypeConfigGroup.GET("/list", dataTypeConfigHandler.GetDataTypeConfigs)
		dataTypeConfigGroup.GET("/:data_type", dataTypeConfigHandler.GetDataTypeConfigWithFields)
	}

	// 任务规划器路由（手动触发重算）
	taskPlannerHandler := NewTaskPlannerHandler(taskPlannerService)
	taskPlannerGroup := router.Group("/task-planner")
	{
		taskPlannerGroup.POST("/recalculate-all", taskPlannerHandler.RecalculateAllTaskInstances)
	}

	log.Info("[Collector] 采集器任务规则、任务实例、数据类型配置和任务规划器路由注册完成")
}
