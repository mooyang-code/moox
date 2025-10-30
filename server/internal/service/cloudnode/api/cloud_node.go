package api

import (
	"context"
	"fmt"
	"strconv"

	"github.com/mooyang-code/moox/server/internal/common"
	"github.com/mooyang-code/moox/server/internal/errors"
	cloudnodemgr "github.com/mooyang-code/moox/server/internal/service/cloudnode"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/model"

	"github.com/gin-gonic/gin"
	"trpc.group/trpc-go/trpc-go/log"
)

// CloudNodeHandler 云节点处理器（用于路由注册）
type CloudNodeHandler struct {
	service cloudnodemgr.Service
}

// NewCloudNodeHandlerWithService 使用已有的服务创建云节点处理器
func NewCloudNodeHandlerWithService(service cloudnodemgr.Service) *CloudNodeHandler {
	return &CloudNodeHandler{
		service: service,
	}
}

// SchemaID 返回表名
func (h *CloudNodeHandler) SchemaID() string {
	return model.CloudNodeTableName
}

// GetHandle 处理GET请求
func (h *CloudNodeHandler) GetHandle(ctx context.Context, params map[string]string) (*APIResponse, error) {
	// 支持根据node_id查询单个节点
	if nodeID, ok := params["node_id"]; ok && nodeID != "" {
		node, err := h.service.GetCloudNode(ctx, nodeID)
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

	// 构建分页请求参数
	req := &cloudnodemgr.NodeListRequest{
		Page:     1,
		PageSize: 20,
	}

	// 解析分页参数
	if pageStr, ok := params["page"]; ok && pageStr != "" {
		if page, err := strconv.Atoi(pageStr); err == nil && page > 0 {
			req.Page = page
		}
	}

	if pageSizeStr, ok := params["page_size"]; ok && pageSizeStr != "" {
		if pageSize, err := strconv.Atoi(pageSizeStr); err == nil && pageSize > 0 && pageSize <= 100 {
			req.PageSize = pageSize
		}
	}

	// 解析过滤参数
	if nodeType, ok := params["node_type"]; ok {
		req.NodeType = nodeType
	}

	if status, ok := params["status"]; ok {
		req.Status = status
	}

	if keyword, ok := params["keyword"]; ok {
		req.Keyword = keyword
	}

	// 获取分页节点列表
	resp, err := h.service.GetNodeList(ctx, req)
	if err != nil {
		return &APIResponse{
			Code: 500,
			Data: []interface{}{},
		}, fmt.Errorf("failed to get node list: %w", err)
	}

	// 转换为接口格式
	items := make([]interface{}, len(resp.Items))
	for i, item := range resp.Items {
		items[i] = item
	}

	return &APIResponse{
		Code:  200,
		Data:  items,
		Total: &resp.Total,
	}, nil
}

// PostHandle 处理POST请求
func (h *CloudNodeHandler) PostHandle(ctx context.Context, params map[string]string) (*APIResponse, error) {
	action := params["_action"]

	switch action {
	case "update":
		// 更新节点信息
		node := &cloudnodemgr.CloudNodeDTO{
			NodeID:              params["node_id"],
			CloudAccountID:      params["cloud_account_id"],
			Namespace:           params["namespace"],
			NodeType:            params["node_type"],
			Region:              params["region"],
			IPAddress:           params["ip_address"],
			SupportedCollectors: params["supported_collectors"],
			Metadata:            params["metadata"],
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
	
	// 构建分页请求参数
	req := &cloudnodemgr.NodeListRequest{}
	if err := c.ShouldBindQuery(req); err != nil {
		common.HandleAppError(c, errors.InvalidParam("query", err.Error()))
		return
	}

	// 设置默认分页参数
	if req.Page <= 0 {
		req.Page = 1
	}
	if req.PageSize <= 0 {
		req.PageSize = 20
	}

	resp, err := h.service.GetNodeList(ctx, req)
	if err != nil {
		common.HandleAppError(c, errors.Internal("查询云节点列表失败", err))
		return
	}

	// 添加代码包版本信息和节点状态到每个节点
	for _, node := range resp.Items {
		node.PackageVersion = "-" // 默认值

		// 如果有代码包ID，查询代码包详情
		if node.PackageID != "" {
			pkg, err := h.service.GetPackageDetail(ctx, node.PackageID)
			if err != nil {
				log.WarnContextf(ctx, "[CloudNode] 查询代码包详情失败，package_id=%s, error=%v", node.PackageID, err)
			} else if pkg != nil {
				// 组合包名和版本号
				node.PackageVersion = fmt.Sprintf("%s-%s", pkg.PackageName, pkg.Version)
			}
		}

		// 查询节点状态
		nodeStatus, err := h.service.GetNodeStatus(ctx, node.NodeID)
		if err != nil {
			log.WarnContextf(ctx, "[CloudNode] 查询节点状态失败，node_id=%s, error=%v", node.NodeID, err)
		} else if nodeStatus != nil {
			node.Status = nodeStatus
		}
	}

	// 使用新的分页列表响应格式
	common.PaginatedListResponse(c, "查询成功", resp.Items, resp.Total)
}

// GetNodeDetail 获取节点详情
func (h *CloudNodeHandler) GetNodeDetail(c *gin.Context) {
	ctx := c.Request.Context()
	nodeID := c.Query("node_id")
	if nodeID == "" {
		common.HandleAppError(c, errors.InvalidParam("node_id", "node_id is required"))
		return
	}

	node, err := h.service.GetCloudNode(ctx, nodeID)
	if err != nil {
		common.HandleAppError(c, errors.Internal("failed to get node", err))
		return
	}
	common.SuccessResponse(c, "success", []interface{}{node})
}

// UpdateNode 更新节点
func (h *CloudNodeHandler) UpdateNode(c *gin.Context) {
	ctx := c.Request.Context()
	var node cloudnodemgr.CloudNodeDTO
	if err := c.ShouldBindJSON(&node); err != nil {
		common.HandleAppError(c, errors.InvalidParam("request_body", err.Error()))
		return
	}

	if err := h.service.UpdateNode(ctx, &node); err != nil {
		common.HandleAppError(c, errors.Internal("failed to update node", err))
		return
	}
	common.SuccessResponse(c, "node updated successfully", []interface{}{})
}


