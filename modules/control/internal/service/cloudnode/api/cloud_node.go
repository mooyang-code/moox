package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/modules/control/internal/common"
	"github.com/mooyang-code/moox/modules/control/internal/errors"
	cloudnodemgr "github.com/mooyang-code/moox/modules/control/internal/service/cloudnode"
	cloudnodeconfig "github.com/mooyang-code/moox/modules/control/internal/service/cloudnode/config"
	"github.com/mooyang-code/moox/modules/control/internal/service/cloudnode/model"
	cloudnodetypes "github.com/mooyang-code/moox/modules/control/internal/service/cloudnode/types"

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

		var data []interface{}
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

		var data []interface{}
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
		if pageSize, err := strconv.Atoi(pageSizeStr); err == nil && pageSize > 0 && pageSize <= 500 {
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

	// 直接使用 ShouldBindJSON，因为前端总是发送 JSON
	if err := c.ShouldBindJSON(req); err != nil {
		log.ErrorContextf(ctx, "[GetNodeList] Failed to bind JSON parameters: %v", err)
		common.HandleAppError(c, errors.InvalidParam("request_body", err.Error()))
		return
	}

	// 打印接收到的参数用于调试
	log.InfoContextf(ctx, "[GetNodeList] Received params - Page: %d, PageSize: %d, NodeID: %s,"+
		" CloudAccountID: %s, Namespace: %s, Region: %s, NodeType: %s, BizType: %s, Tag: %s, Status: %s",
		req.Page, req.PageSize, req.NodeID, req.CloudAccountID, req.Namespace, req.Region, req.NodeType, req.BizType, req.Tag, req.Status)

	// 设置默认分页参数
	if req.Page <= 0 {
		req.Page = 1
	}
	if req.PageSize <= 0 {
		req.PageSize = 20
	}

	log.InfoContextf(ctx, "[GetNodeList] After default - Page: %d, PageSize: %d", req.Page, req.PageSize)

	resp, err := h.service.GetNodeList(ctx, req)
	if err != nil {
		common.HandleAppError(c, errors.Internal("查询云节点列表失败", err))
		return
	}

	// 添加代码包版本信息和节点状态到每个节点
	items := make([]map[string]interface{}, len(resp.Items))
	for i, node := range resp.Items {
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

		statusText := calcNodeStatusText(node.LastHeartbeat, node.TimeoutThreshold)
		raw, err := json.Marshal(node)
		if err != nil {
			log.WarnContextf(ctx, "[CloudNode] Failed to marshal node: %v", err)
			items[i] = map[string]interface{}{
				"node_id": node.NodeID,
				"status":  statusText,
			}
			continue
		}

		var item map[string]interface{}
		if err := json.Unmarshal(raw, &item); err != nil {
			log.WarnContextf(ctx, "[CloudNode] Failed to unmarshal node: %v", err)
			items[i] = map[string]interface{}{
				"node_id": node.NodeID,
				"status":  statusText,
			}
			continue
		}

		item["status"] = statusText
		items[i] = item
	}

	// 使用新的分页列表响应格式
	common.PaginatedListResponse(c, "查询成功", items, resp.Total)
}

func calcNodeStatus(lastHeartbeat *time.Time, timeoutThreshold int) *cloudnodetypes.NodeStatus {
	status := cloudnodetypes.NodeStatusOffline
	if lastHeartbeat == nil {
		return &status
	}

	if timeoutThreshold <= 0 {
		timeoutThreshold = cloudnodeconfig.Get().Heartbeat.DefaultTimeoutThreshold
	}

	if time.Since(*lastHeartbeat) > time.Duration(timeoutThreshold)*time.Second {
		status = cloudnodetypes.NodeStatusOffline
	} else {
		status = cloudnodetypes.NodeStatusOnline
	}
	return &status
}

func calcNodeStatusText(lastHeartbeat *time.Time, timeoutThreshold int) string {
	status := calcNodeStatus(lastHeartbeat, timeoutThreshold)
	if status == nil {
		return "offline"
	}

	switch *status {
	case cloudnodetypes.NodeStatusOnline:
		return "online"
	default:
		return "offline"
	}
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

// SCFDeployInfoResponse SCF 部署所需信息（供 scf-publish 等发布工具消费）。
// 聚合 t_cloud_nodes 中的 SCF 函数定位字段与关联云账户 ID，避免发布工具
// 硬编码 function/namespace/region/account_id。
type SCFDeployInfoResponse struct {
	NodeID         string `json:"node_id"`
	FunctionName   string `json:"function_name"`   // SCF 函数名（= node_id）
	Namespace      string `json:"namespace"`       // SCF 命名空间
	Region         string `json:"region"`          // SCF region
	NodeType       string `json:"node_type"`       // 节点类型
	CloudAccountID string `json:"cloud_account_id"` // 关联云账户 ID（用于查询 COS 凭证）
}

// GetSCFDeployInfo 获取 SCF 节点部署信息。
// GET /api/v1/cloud_node/scf-deploy-info?node_id=xxx
//
// 供 scf-publish 通过 /api/service/cloudnode/GetSCFDeployInfo 调用，
// 受 gateway service_auth HMAC 签名鉴权保护。
func (h *CloudNodeHandler) GetSCFDeployInfo(c *gin.Context) {
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
	if node == nil {
		c.JSON(404, gin.H{"code": 404, "message": "node not found"})
		return
	}

	resp := &SCFDeployInfoResponse{
		NodeID:         node.NodeID,
		FunctionName:   node.NodeID, // SCF 函数名即 node_id
		Namespace:      node.Namespace,
		Region:         node.Region,
		NodeType:       node.NodeType,
		CloudAccountID: node.CloudAccountID,
	}
	c.JSON(200, gin.H{"code": 200, "data": resp})
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

// InvokeFunction 调用云节点对应的云函数
func (h *CloudNodeHandler) InvokeFunction(c *gin.Context) {
	ctx := c.Request.Context()

	var req InvokeFunctionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.HandleAppError(c, errors.InvalidParam("request_body", err.Error()))
		return
	}
	if req.NodeID == "" {
		common.HandleAppError(c, errors.InvalidParam("node_id", "node_id is required"))
		return
	}
	if req.EventData == nil {
		common.HandleAppError(c, errors.InvalidParam("event_data", "event_data is required"))
		return
	}

	resp, err := h.service.InvokeFunction(ctx, req.NodeID, req.EventData)
	if err != nil {
		common.HandleAppError(c, errors.Internal("调用云函数失败", err))
		return
	}

	common.SuccessResponse(c, "调用成功", []interface{}{resp})
}
