package api

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/mooyang-code/moox/server/internal/service/collector/logic"
	"gorm.io/gorm"
)

// NodeTasksHandler 节点任务处理器
type NodeTasksHandler struct {
	db                  *gorm.DB
	taskInstanceService logic.CollectorTaskInstanceService
}

// NewNodeTasksHandler 创建节点任务处理器
func NewNodeTasksHandler(db *gorm.DB) *NodeTasksHandler {
	return &NodeTasksHandler{
		db:                  db,
		taskInstanceService: logic.NewCollectorTaskInstanceService(db),
	}
}

// GetNodeTasks 获取节点的任务配置
func (h *NodeTasksHandler) GetNodeTasks(c *gin.Context) {
	nodeID := c.Param("nodeID")
	if nodeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "node_id is required",
		})
		return
	}

	// 获取分配给该节点的待执行和运行中的任务
	tasks, err := h.taskInstanceService.GetTaskInstancesByNode(c, nodeID, []int{
		0, // Pending
		1, // Running
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	// 转换为配置格式
	var taskConfigs []map[string]interface{}
	for _, task := range tasks {
		// 解析执行参数
		var params map[string]interface{}
		if err := json.Unmarshal([]byte(task.ExecutionParams), &params); err != nil {
			params = make(map[string]interface{})
		}

		config := map[string]interface{}{
			"task_id":        task.InstanceID,
			"collector_type": params["collector_type"],
			"source":         params["source_name"],
			"interval":       params["interval"],
			"config":         params,
		}
		
		taskConfigs = append(taskConfigs, config)
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    taskConfigs,
	})
}

// RegisterNodeTasksRoutes 注册节点任务路由
func RegisterNodeTasksRoutes(router *gin.RouterGroup, db *gorm.DB) {
	handler := NewNodeTasksHandler(db)
	
	// 获取节点任务列表
	router.GET("/tasks/node/:nodeID", handler.GetNodeTasks)
}