package api

import (
	"context"
	"fmt"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/mooyang-code/moox/server/internal/common"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/logic"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"gorm.io/gorm"
)

// SCFNodeHandler SCF节点处理器
type SCFNodeHandler struct {
	service logic.SCFNodeService
}

// CloudNodeHandler 云节点处理器（用于路由注册）
type CloudNodeHandler struct {
	service logic.SCFNodeService
}

// NewSCFNodeHandler 创建SCF节点处理器
func NewSCFNodeHandler(db *gorm.DB) SchemaHandler {
	return &SCFNodeHandler{
		service: logic.NewSCFNodeService(db),
	}
}

// NewCloudNodeHandler 创建云节点处理器（别名）
func NewCloudNodeHandler(db *gorm.DB) *CloudNodeHandler {
	return &CloudNodeHandler{
		service: logic.NewSCFNodeService(db),
	}
}

// NewSCFNodeHandlerWithService 使用已有的服务创建SCF节点处理器
func NewSCFNodeHandlerWithService(service logic.SCFNodeService) SchemaHandler {
	return &SCFNodeHandler{
		service: service,
	}
}

// SchemaID 返回表名
func (h *SCFNodeHandler) SchemaID() string {
	return model.SCFNodeTableName
}

// GetHandle 处理GET请求
func (h *SCFNodeHandler) GetHandle(ctx context.Context, params map[string]string) (*APIResponse, error) {
	// 支持根据node_id查询单个节点
	if nodeID, ok := params["node_id"]; ok && nodeID != "" {
		node, err := h.service.GetNode(ctx, nodeID)
		if err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to get node: %w", err)
		}
		
		if node == nil {
			return &APIResponse{
				Code: 404,
				Data: []interface{}{},
			}, nil
		}
		
		return &APIResponse{
			Code: 200,
			Data: []interface{}{node},
		}, nil
	}
	
	// 支持按节点类型查询
	if nodeType, ok := params["node_type"]; ok && nodeType != "" {
		nodes, err := h.service.GetNodesByType(ctx, nodeType)
		if err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to get nodes by type: %w", err)
		}
		
		data := make([]interface{}, 0, len(nodes))
		for _, node := range nodes {
			data = append(data, node)
		}
		
		return &APIResponse{
			Code: 200,
			Data: data,
		}, nil
	}
	
	// 支持查询在线节点
	if status, ok := params["status"]; ok && status == "online" {
		nodes, err := h.service.GetOnlineNodes(ctx)
		if err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to get online nodes: %w", err)
		}
		
		data := make([]interface{}, 0, len(nodes))
		for _, node := range nodes {
			data = append(data, node)
		}
		
		return &APIResponse{
			Code: 200,
			Data: data,
		}, nil
	}
	
	// 获取所有节点列表
	nodes, err := h.service.GetNodeList(ctx)
	if err != nil {
		return &APIResponse{
			Code: 500,
			Data: []interface{}{},
		}, fmt.Errorf("failed to get node list: %w", err)
	}
	
	// 转换为接口切片
	data := make([]interface{}, 0, len(nodes))
	for _, node := range nodes {
		data = append(data, node)
	}
	
	return &APIResponse{
		Code: 200,
		Data: data,
	}, nil
}

// PostHandle 处理POST请求
func (h *SCFNodeHandler) PostHandle(ctx context.Context, params map[string]string) (*APIResponse, error) {
	action := params["_action"]
	
	switch action {
	case "register":
		// 注册新节点
		node := &model.SCFNode{
			CloudAccountID:      params["cloud_account_id"],
			NodeType:            params["node_type"],
			Region:              params["region"],
			IPAddress:           params["ip_address"],
			Version:             params["version"],
			SupportedCollectors: params["supported_collectors"],
			Capacity:            params["capacity"],
			CurrentLoad:         params["current_load"],
			Metadata:            params["metadata"],
		}
		
		if statusStr, ok := params["status"]; ok {
			status, err := strconv.Atoi(statusStr)
			if err == nil {
				node.Status = status
			}
		}
		
		// 调用服务层注册节点，返回生成的节点信息
		registeredNode, err := h.service.CreateNode(ctx, node, "", "")
		if err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to register node: %w", err)
		}
		
		return &APIResponse{
			Code: 200,
			Data: []interface{}{registeredNode},
		}, nil
		
	case "update":
		// 更新节点信息
		node := &model.SCFNode{
			NodeID:              params["node_id"],
			CloudAccountID:      params["cloud_account_id"],
			Namespace:           params["namespace"],
			NodeType:            params["node_type"],
			Region:              params["region"],
			IPAddress:           params["ip_address"],
			Version:             params["version"],
			SupportedCollectors: params["supported_collectors"],
			Capacity:            params["capacity"],
			CurrentLoad:         params["current_load"],
			Metadata:            params["metadata"],
		}
		
		if statusStr, ok := params["status"]; ok {
			status, err := strconv.Atoi(statusStr)
			if err == nil {
				node.Status = status
			}
		}
		
		if err := h.service.UpdateNode(ctx, node); err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to update node: %w", err)
		}
		
		return &APIResponse{
			Code: 200,
			Data: []interface{}{"success"},
		}, nil
		
	case "delete":
		// 删除节点
		nodeID := params["node_id"]
		if err := h.service.RemoveNode(ctx, nodeID, "", ""); err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to delete node: %w", err)
		}
		
		return &APIResponse{
			Code: 200,
			Data: []interface{}{"success"},
		}, nil
		
	case "heartbeat":
		// 更新心跳
		nodeID := params["node_id"]
		currentLoad := params["current_load"]
		if err := h.service.Heartbeat(ctx, nodeID, currentLoad); err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to update heartbeat: %w", err)
		}
		
		return &APIResponse{
			Code: 200,
			Data: []interface{}{"success"},
		}, nil
		
	case "update_load":
		// 更新负载信息
		nodeID := params["node_id"]
		currentLoad := params["current_load"]
		if err := h.service.UpdateNodeLoad(ctx, nodeID, currentLoad); err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to update node load: %w", err)
		}
		
		return &APIResponse{
			Code: 200,
			Data: []interface{}{"success"},
		}, nil
		
	case "update_function":
		// 更新云函数（用于非文件上传方式，例如通过文件路径）
		nodeID := params["node_id"]
		zipFilePath := params["zip_file_path"]
		if err := h.service.UpdateNodeFunction(ctx, nodeID, zipFilePath); err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to update function: %w", err)
		}
		
		return &APIResponse{
			Code: 200,
			Data: []interface{}{"success"},
		}, nil
		
	default:
		return &APIResponse{
			Code: 400,
			Data: []interface{}{},
		}, fmt.Errorf("invalid action: %s", action)
	}
}

// GetNodeList 获取节点列表
func (h *CloudNodeHandler) GetNodeList(c *gin.Context) {
	ctx := c.Request.Context()
	nodes, err := h.service.GetNodeList(ctx)
	if err != nil {
		common.ErrorResponse(c, 500, "查询云节点列表失败", err)
		return
	}
	
	// 计算总数
	total := int64(len(nodes))
	
	// 使用新的分页列表响应格式
	common.PaginatedListResponse(c, "查询成功", nodes, total)
}

// GetNodeDetail 获取节点详情
func (h *CloudNodeHandler) GetNodeDetail(c *gin.Context) {
	ctx := c.Request.Context()
	nodeID := c.Query("node_id")
	if nodeID == "" {
		c.JSON(400, gin.H{"code": 400, "message": "node_id is required"})
		return
	}
	
	node, err := h.service.GetNode(ctx, nodeID)
	if err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 200, "data": node})
}

// RegisterNode 注册节点
func (h *CloudNodeHandler) RegisterNode(c *gin.Context) {
	ctx := c.Request.Context()
	var node model.SCFNode
	if err := c.ShouldBindJSON(&node); err != nil {
		c.JSON(400, gin.H{"code": 400, "message": err.Error()})
		return
	}
	
	registeredNode, err := h.service.CreateNode(ctx, &node, "", "")
	if err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 200, "data": registeredNode})
}

// UpdateNode 更新节点
func (h *CloudNodeHandler) UpdateNode(c *gin.Context) {
	ctx := c.Request.Context()
	var node model.SCFNode
	if err := c.ShouldBindJSON(&node); err != nil {
		c.JSON(400, gin.H{"code": 400, "message": err.Error()})
		return
	}
	
	if err := h.service.UpdateNode(ctx, &node); err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 200, "message": "success"})
}

// RemoveNode 删除节点
func (h *CloudNodeHandler) RemoveNode(c *gin.Context) {
	ctx := c.Request.Context()
	nodeID := c.Query("node_id")
	if nodeID == "" {
		c.JSON(400, gin.H{"code": 400, "message": "node_id is required"})
		return
	}
	
	if err := h.service.RemoveNode(ctx, nodeID, "", ""); err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 200, "message": "success"})
}

// Heartbeat 心跳
func (h *CloudNodeHandler) Heartbeat(c *gin.Context) {
	ctx := c.Request.Context()
	var req struct {
		NodeID      string `json:"node_id"`
		CurrentLoad string `json:"current_load"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"code": 400, "message": err.Error()})
		return
	}
	
	if err := h.service.Heartbeat(ctx, req.NodeID, req.CurrentLoad); err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 200, "message": "success"})
}

// UpdateNodeLoad 更新节点负载
func (h *CloudNodeHandler) UpdateNodeLoad(c *gin.Context) {
	ctx := c.Request.Context()
	var req struct {
		NodeID      string `json:"node_id"`
		CurrentLoad string `json:"current_load"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"code": 400, "message": err.Error()})
		return
	}
	
	if err := h.service.UpdateNodeLoad(ctx, req.NodeID, req.CurrentLoad); err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 200, "message": "success"})
}

// UpdateNodeFunction 更新节点函数
func (h *CloudNodeHandler) UpdateNodeFunction(c *gin.Context) {
	ctx := c.Request.Context()
	var req struct {
		NodeID      string `json:"node_id"`
		ZipFilePath string `json:"zip_file_path"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"code": 400, "message": err.Error()})
		return
	}
	
	if err := h.service.UpdateNodeFunction(ctx, req.NodeID, req.ZipFilePath); err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 200, "message": "success"})
}