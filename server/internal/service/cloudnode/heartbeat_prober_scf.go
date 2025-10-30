package cloudnode

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/server/internal/common"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"
)

// CloudFunctionEvent 云函数事件结构（与接收方data-collector中的CloudFunctionEvent格式一致）
type CloudFunctionEvent struct {
	Action    string                 `json:"action,omitempty"`
	Data      map[string]interface{} `json:"data,omitempty"`
	Timestamp string                 `json:"timestamp"`
	RequestID string                 `json:"request_id,omitempty"`
	Source    string                 `json:"source,omitempty"`
}

// ProbeEventData 探测事件数据结构（专用于buildEventData返回值）
type ProbeEventData struct {
	Action    string            `json:"action"`
	Data      ProbeEventDetails `json:"data"`
	Timestamp string            `json:"timestamp"`
	RequestID string            `json:"request_id"`
	Source    string            `json:"source"`
}

// ProbeEventDetails 探测事件数据详情
type ProbeEventDetails struct {
	InternalIP string `json:"internal_ip"`
	PublicIP   string `json:"public_ip"`
	ProberType string `json:"prober_type"`
	ProbeTime  string `json:"probe_time"`
	ServerIP   string `json:"server_ip"`
	ServerPort int    `json:"server_port"`
}

// SCFHeartbeatProber 云函数心跳探测器
type SCFHeartbeatProber struct {
	nodeDAO        dao.CloudNodeDAO
	accountFactory *provider.AccountFactory
}

// NewSCFHeartbeatProber 创建云函数心跳探测器
func NewSCFHeartbeatProber(nodeDAO dao.CloudNodeDAO, accountFactory *provider.AccountFactory) *SCFHeartbeatProber {
	return &SCFHeartbeatProber{
		nodeDAO:        nodeDAO,
		accountFactory: accountFactory,
	}
}

// Name 探测器名称
func (a *SCFHeartbeatProber) Name() string {
	return "scf"
}

// Probe 执行云函数探测
// 通过调用云函数来探测其健康状态
func (a *SCFHeartbeatProber) Probe(ctx context.Context, req *ProbeRequest) (*ProbeResponse, error) {
	if req.NodeID == "" {
		return nil, fmt.Errorf("node id is required for scf prober")
	}

	// 通过nodeDAO查询节点信息获取账户ID
	node, err := a.nodeDAO.GetCloudNode(ctx, req.NodeID)
	if err != nil {
		return nil, fmt.Errorf("get node info failed: %w", err)
	}
	if node == nil {
		return nil, fmt.Errorf("node %s not found", req.NodeID)
	}

	// 根据账户ID获取云厂商客户端
	cloudClient := a.accountFactory.GetCloudProviderByAccount(node.CloudAccountID)
	if cloudClient == nil {
		return nil, fmt.Errorf("get cloud provider failed for account %s", node.CloudAccountID)
	}

	// 调用云函数
	invokeResp, err := cloudClient.InvokeFunction(ctx, &provider.InvokeFunctionRequest{
		FunctionName: req.NodeID,
		Namespace:    node.Namespace,
		EventData:    a.buildEventData(req.Action),
	})
	if err != nil {
		return nil, fmt.Errorf("invoke function %s failed: %w", req.NodeID, err)
	}

	// 统一解析响应
	return a.parseProbeResponse(invokeResp), nil
}

// buildEventData 构造探测事件数据（返回结构化数据，避免map类型转换问题）
func (a *SCFHeartbeatProber) buildEventData(action string) *ProbeEventData {
	// 如果action为空，默认使用health
	if action == "" {
		action = "health"
	}

	// 生成请求ID
	requestID := fmt.Sprintf("probe_%d_%s", time.Now().UnixNano(), a.Name())
	timestamp := time.Now().Format(time.RFC3339)

	// 返回结构化的事件数据
	internalIP := common.GetInternalIP()
	PublicIP := common.GetPublicIP()
	return &ProbeEventData{
		Action:    action,
		Timestamp: timestamp,
		RequestID: requestID,
		Source:    "heartbeat_probe",
		Data: ProbeEventDetails{
			InternalIP: internalIP,
			PublicIP:   PublicIP,
			ProberType: a.Name(),
			ProbeTime:  timestamp,
			ServerIP:   internalIP,
		},
	}
}

// parseProbeResponse 统一解析探测响应
func (a *SCFHeartbeatProber) parseProbeResponse(invokeResp *provider.InvokeFunctionResponse) *ProbeResponse {
	response := &ProbeResponse{}
	if invokeResp == nil {
		return response
	}

	// 设置请求ID
	response.RequestID = invokeResp.RequestID

	// 判断调用是否成功
	success := a.determineSuccess(invokeResp)
	if !success {
		response.State = "error"
	}

	// 尝试解析云函数返回的数据，提取核心字段
	if invokeResp.Result != "" {
		if resultData, ok := a.parseJSONResult(invokeResp.Result); ok {
			a.extractProbeFields(response, resultData)
		}
	}
	if !success {
		if invokeResp.ErrorMessage != "" {
			response.State = "error: " + invokeResp.ErrorMessage
		}
	}
	return response
}

// extractProbeFields 从data-collector响应中提取ProbeResponse字段
func (a *SCFHeartbeatProber) extractProbeFields(response *ProbeResponse, resultData map[string]interface{}) {
	// 提取请求ID（顶层字段）
	if requestID, ok := resultData["request_id"].(string); ok && requestID != "" {
		response.RequestID = requestID
	}

	// 提取 data 字段
	data, exists := resultData["data"]
	if !exists {
		return
	}
	dataMap, ok := data.(map[string]interface{})
	if !ok {
		return
	}

	// 提取 state（节点状态）
	if state, ok := dataMap["state"].(string); ok && state != "" {
		response.State = state
	}

	// 提取 timestamp（节点时间戳）
	a.parseTimestamp(dataMap, response)

	// 提取 node_info 中的信息
	a.parseNodeInfo(dataMap, response)

	// 提取顶层timestamp作为备选（如果data中没有timestamp）
	if response.Timestamp == "" {
		a.parseTopLevelTimestamp(resultData, response)
	}

	// 如果状态为空，设置为unknown
	if response.State == "" {
		response.State = "unknown"
	}
}

// parseTimestamp 解析时间戳
func (a *SCFHeartbeatProber) parseTimestamp(dataMap map[string]interface{}, response *ProbeResponse) {
	timestamp, ok := dataMap["timestamp"]
	if !ok {
		return
	}

	if tsStr, ok := timestamp.(string); ok {
		response.Timestamp = tsStr
	} else if tsInt, ok := timestamp.(int64); ok {
		response.Timestamp = time.Unix(tsInt, 0).Format(time.RFC3339)
	}
}

// parseNodeInfo 解析节点信息
func (a *SCFHeartbeatProber) parseNodeInfo(dataMap map[string]interface{}, response *ProbeResponse) {
	nodeInfo, exists := dataMap["node_info"]
	if !exists {
		return
	}

	nodeInfoMap, ok := nodeInfo.(map[string]interface{})
	if !ok {
		return
	}

	// 提取 node_id
	if nodeID, ok := nodeInfoMap["node_id"].(string); ok && nodeID != "" {
		response.NodeID = nodeID
	}

	// 提取 version
	if version, ok := nodeInfoMap["version"].(string); ok && version != "" {
		response.FunctionVersion = version
	}

	// 提取 metadata 中的系统信息
	a.parseNodeMetadata(nodeInfoMap, response)
}

// parseNodeMetadata 解析节点元数据
func (a *SCFHeartbeatProber) parseNodeMetadata(nodeInfoMap map[string]interface{}, response *ProbeResponse) {
	metadata, exists := nodeInfoMap["metadata"]
	if !exists {
		return
	}

	metadataMap, ok := metadata.(map[string]interface{})
	if !ok {
		return
	}

	// 提取操作系统信息
	if os, ok := metadataMap["os"].(string); ok && os != "" {
		response.OSName = os
	}
}

// parseTopLevelTimestamp 解析顶层时间戳
func (a *SCFHeartbeatProber) parseTopLevelTimestamp(resultData map[string]interface{}, response *ProbeResponse) {
	timestamp, ok := resultData["timestamp"]
	if !ok {
		return
	}

	if tsStr, ok := timestamp.(string); ok {
		response.Timestamp = tsStr
	} else if tsInt, ok := timestamp.(int64); ok {
		response.Timestamp = time.Unix(tsInt, 0).Format(time.RFC3339)
	}
}

// determineSuccess 判断调用是否成功
func (a *SCFHeartbeatProber) determineSuccess(invokeResp *provider.InvokeFunctionResponse) bool {
	// 如果result为空，直接回退到StatusCode检查
	if invokeResp.Result == "" {
		return invokeResp.StatusCode == 0 && invokeResp.ErrorMessage == ""
	}

	// 解析JSON结果
	resultData, ok := a.parseJSONResult(invokeResp.Result)
	if !ok {
		return invokeResp.StatusCode == 0 && invokeResp.ErrorMessage == ""
	}

	// 检查success字段是否存在
	successValue, exists := resultData["success"]
	if !exists {
		return invokeResp.StatusCode == 0 && invokeResp.ErrorMessage == ""
	}

	// 检查success值是否为布尔类型
	successBool, ok := successValue.(bool)
	if !ok {
		return invokeResp.StatusCode == 0 && invokeResp.ErrorMessage == ""
	}
	return successBool
}

// parseJSONResult 解析JSON结果
func (a *SCFHeartbeatProber) parseJSONResult(result string) (map[string]interface{}, bool) {
	var resultData map[string]interface{}
	if err := json.Unmarshal([]byte(result), &resultData); err != nil {
		return nil, false
	}
	return resultData, true
}
