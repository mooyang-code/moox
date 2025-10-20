package api

import (
	"github.com/gin-gonic/gin"

	"github.com/mooyang-code/moox/server/internal/errors"
	cloudnode "github.com/mooyang-code/moox/server/internal/service/cloudnode"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/types"
)

// NodeHandler 节点管理接口处理器
type NodeHandler struct {
	service cloudnode.HeartbeatService
}

// NewNodeHandler 创建节点管理接口处理器
func NewNodeHandler(service cloudnode.HeartbeatService) *NodeHandler {
	return &NodeHandler{
		service: service,
	}
}

// RegisterNode 注册节点
// @Summary 注册节点
// @Description 注册新的监控节点
// @Tags 节点管理
// @Accept json
// @Produce json
// @Param request body NodeRegisterRequest true "节点注册请求"
// @Success 200 {object} APIResponse{data=types.HeartbeatNode}
// @Failure 400 {object} APIResponse
// @Failure 500 {object} APIResponse
// @Router /api/v1/heartbeat/nodes/register [post]
func (h *NodeHandler) RegisterNode(c *gin.Context) {
	var req NodeRegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		HandleAppError(c, errors.InvalidParam("request", err.Error()))
		return
	}

	// Convert API request to service layer request
	serviceReq := &types.RegisterNodeRequest{
		NodeID:        req.NodeID,
		NodeType:      req.NodeType,
		SourceService: req.SourceService,
		Metadata:      req.Metadata,
	}

	record, err := h.service.RegisterHeartbeatNode(c.Request.Context(), serviceReq)
	if err != nil {
		HandleAppError(c, errors.Internal("failed to register node", err))
		return
	}

	SuccessResponse(c, "node registered successfully", record)
}

// UnregisterNode 注销节点
// @Summary 注销节点
// @Description 注销指定的监控节点
// @Tags 节点管理
// @Param node_id path string true "节点ID"
// @Param node_type path string true "节点类型"
// @Success 200 {object} APIResponse
// @Failure 400 {object} APIResponse
// @Failure 500 {object} APIResponse
// @Router /api/v1/heartbeat/nodes/{node_id}/{node_type} [delete]
func (h *NodeHandler) UnregisterNode(c *gin.Context) {
	nodeID := c.Param("node_id")
	nodeType := c.Param("node_type")

	if nodeID == "" || nodeType == "" {
		HandleAppError(c, errors.InvalidParam("node_id_or_node_type", "node_id and node_type are required"))
		return
	}

	if err := h.service.UnregisterHeartbeatNode(c.Request.Context(), nodeID, nodeType); err != nil {
		HandleAppError(c, errors.Internal("failed to unregister node", err))
		return
	}

	SuccessResponse(c, "node unregistered successfully", nil)
}

// GetNode 获取节点信息
// @Summary 获取节点信息
// @Description 获取指定节点的详细信息
// @Tags 节点管理
// @Param node_id path string true "节点ID"
// @Param node_type path string true "节点类型"
// @Success 200 {object} APIResponse{data=types.HeartbeatNode}
// @Failure 400 {object} APIResponse
// @Failure 404 {object} APIResponse
// @Failure 500 {object} APIResponse
// @Router /api/v1/heartbeat/nodes/{node_id}/{node_type} [get]
func (h *NodeHandler) GetNode(c *gin.Context) {
	nodeID := c.Param("node_id")
	nodeType := c.Param("node_type")

	if nodeID == "" || nodeType == "" {
		HandleAppError(c, errors.InvalidParam("node_id_or_node_type", "node_id and node_type are required"))
		return
	}

	record, err := h.service.GetHeartbeatNode(c.Request.Context(), nodeID, nodeType)
	if err != nil {
		HandleAppError(c, errors.Internal("failed to get node", err))
		return
	}

	if record == nil {
		HandleAppError(c, errors.NotFound("node"))
		return
	}

	SuccessResponse(c, "success", record)
}

// ListNodes 列出节点
// @Summary 列出节点
// @Description 分页列出监控节点
// @Tags 节点管理
// @Param node_ids query []string false "节点ID列表"
// @Param node_types query []string false "节点类型列表"
// @Param source_service query string false "来源服务"
// @Param status query int false "节点状态"
// @Param probe_enabled query bool false "是否启用探测"
// @Param keyword query string false "关键词搜索"
// @Param page query int false "页码" default(1)
// @Param page_size query int false "页大小" default(20)
// @Param sort_by query string false "排序字段"
// @Param sort_order query string false "排序方向" Enums(asc, desc)
// @Success 200 {object} PaginatedResponse{data=[]types.HeartbeatNode}
// @Failure 400 {object} APIResponse
// @Failure 500 {object} APIResponse
// @Router /api/v1/heartbeat/nodes [get]
func (h *NodeHandler) ListNodes(c *gin.Context) {
	var req NodeListRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		HandleAppError(c, errors.InvalidParam("request", err.Error()))
		return
	}

	// 转换为内部过滤器
	filter := &types.NodeFilter{
		NodeIDs:   req.NodeIDs,
		NodeTypes: req.NodeTypes,
		Keyword:   req.Keyword,
		Page:      req.Page,
		PageSize:  req.PageSize,
		SortBy:    req.SortBy,
		SortOrder: req.SortOrder,
	}

	if req.SourceService != "" {
		filter.SourceService = &req.SourceService
	}

	if req.Status != nil {
		status := types.NodeStatus(*req.Status)
		filter.Status = &status
	}

	if req.ProbeEnabled != nil {
		filter.ProbeEnabled = req.ProbeEnabled
	}

	records, total, err := h.service.ListHeartbeatNodes(c.Request.Context(), filter)
	if err != nil {
		HandleAppError(c, errors.Internal("failed to list nodes", err))
		return
	}

	PaginatedListResponse(c, "success", records, total)
}

// UpdateNodeConfig 更新节点配置
// @Summary 更新节点配置
// @Description 更新指定节点的配置信息
// @Tags 节点管理
// @Accept json
// @Produce json
// @Param node_id path string true "节点ID"
// @Param node_type path string true "节点类型"
// @Param request body NodeConfigUpdateRequest true "节点配置更新请求"
// @Success 200 {object} APIResponse
// @Failure 400 {object} APIResponse
// @Failure 500 {object} APIResponse
// @Router /api/v1/heartbeat/nodes/{node_id}/{node_type}/config [put]
func (h *NodeHandler) UpdateNodeConfig(c *gin.Context) {
	nodeID := c.Param("node_id")
	nodeType := c.Param("node_type")

	if nodeID == "" || nodeType == "" {
		HandleAppError(c, errors.InvalidParam("node_id_or_node_type", "node_id and node_type are required"))
		return
	}

	var req NodeConfigUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		HandleAppError(c, errors.InvalidParam("request", err.Error()))
		return
	}

	// Convert API request to service layer request
	serviceReq := &types.UpdateNodeConfigRequest{
		NodeID:       nodeID,
		NodeType:     nodeType,
		ProbeEnabled: req.ProbeEnabled,
	}

	if err := h.service.UpdateHeartbeatNodeConfig(c.Request.Context(), serviceReq); err != nil {
		HandleAppError(c, errors.Internal("failed to update node config", err))
		return
	}

	SuccessResponse(c, "node config updated successfully", nil)
}
