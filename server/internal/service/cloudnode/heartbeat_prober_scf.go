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

	startTime := time.Now()
	internalIP := common.GetInternalIP()
	publicIP := common.GetPublicIP(ctx)

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
	eventData := a.buildEventData(req.Action, internalIP, publicIP)
	invokeResp, err := cloudClient.InvokeFunction(ctx, &provider.InvokeFunctionRequest{
		FunctionName: req.NodeID,
		Namespace:    node.Namespace,
		EventData:    eventData,
	})
	responseTime := time.Since(startTime).Milliseconds()

	// 构造基础响应
	response := a.buildBaseResponse(responseTime, internalIP, publicIP, req.Metadata)
	response.Details["account_id"] = node.CloudAccountID

	// 处理调用失败
	if err != nil {
		response.Details["error"] = err.Error()
		return response, nil
	}

	// 处理调用响应
	if invokeResp == nil {
		return response, nil
	}

	// 转换为兼容的格式
	respMap := map[string]interface{}{
		"RequestID":    invokeResp.RequestID,
		"Result":       invokeResp.Result,
		"Duration":     invokeResp.Duration,
		"StatusCode":   invokeResp.StatusCode,
		"ErrorMessage": invokeResp.ErrorMessage,
		"ErrorType":    invokeResp.ErrorType,
	}
	a.processInvokeResponse(response, respMap)
	return response, nil
}

// buildEventData 构造探测事件数据
func (a *SCFHeartbeatProber) buildEventData(action, internalIP, publicIP string) map[string]interface{} {
	// 如果action为空，默认使用health
	if action == "" {
		action = "health"
	}

	return map[string]interface{}{
		"action":      action,
		"source":      "heartbeat_probe",
		"timestamp":   time.Now().Unix(),
		"internal_ip": internalIP,
		"public_ip":   publicIP,
	}
}

// buildBaseResponse 构造基础响应
func (a *SCFHeartbeatProber) buildBaseResponse(responseTime int64, internalIP, publicIP string, metadata map[string]interface{}) *ProbeResponse {
	response := &ProbeResponse{
		Success:      false,
		StatusCode:   0,
		ResponseTime: responseTime,
		Details:      make(map[string]interface{}),
	}

	// 添加基础详细信息
	response.Details["adapter_type"] = "scf"
	response.Details["function_type"] = "serverless"
	response.Details["probe_method"] = "invoke"
	response.Details["internal_ip"] = internalIP
	response.Details["public_ip"] = publicIP

	// 添加元数据信息
	a.addMetadataToDetails(response.Details, metadata)
	return response
}

// addMetadataToDetails 添加元数据到详细信息
func (a *SCFHeartbeatProber) addMetadataToDetails(details map[string]interface{}, metadata map[string]interface{}) {
	if metadata == nil {
		return
	}

	metadataKeys := []string{"region", "namespace", "cloud_account_id", "package_id"}
	for _, key := range metadataKeys {
		if value, ok := metadata[key]; ok {
			details[key] = value
		}
	}
}

// processInvokeResponse 处理调用响应
func (a *SCFHeartbeatProber) processInvokeResponse(response *ProbeResponse, invokeResp map[string]interface{}) {
	if statusCode, ok := invokeResp["StatusCode"].(int); ok {
		response.StatusCode = statusCode
	}
	if requestID, ok := invokeResp["RequestID"].(string); ok {
		response.Details["request_id"] = requestID
	}
	if duration, ok := invokeResp["Duration"].(int64); ok {
		response.Details["duration"] = duration
	}

	// 判断调用是否成功
	success := a.determineSuccess(invokeResp)
	response.Success = success

	// 添加function_response详情
	if result, ok := invokeResp["Result"].(string); ok && result != "" {
		if resultData, ok := a.parseJSONResult(result); ok {
			if data, exists := resultData["data"]; exists {
				response.Details["function_response"] = data
			}
		}
	}

	// 处理错误信息
	if !success {
		if errorMessage, ok := invokeResp["ErrorMessage"].(string); ok && errorMessage != "" {
			response.Details["error_message"] = errorMessage
			response.Details["error_type"] = invokeResp["ErrorType"]
		}
	}

	// 检查响应时间警告（ms）
	if success {
		if duration, ok := invokeResp["Duration"].(int64); ok && duration > 5000 {
			response.Details["warning"] = "slow response time, possible cold start"
		}
	}
}

// determineSuccess 判断调用是否成功
func (a *SCFHeartbeatProber) determineSuccess(invokeResp map[string]interface{}) bool {
	// 优先解析JSON中的success字段
	result, _ := invokeResp["Result"].(string)
	if success, ok := a.parseSuccessFromJSON(result); ok {
		return success
	}

	// 回退到StatusCode检查（腾讯云SCF成功时StatusCode=0）
	statusCode, _ := invokeResp["StatusCode"].(int)
	errorMessage, _ := invokeResp["ErrorMessage"].(string)
	return statusCode == 0 && errorMessage == ""
}

// parseSuccessFromJSON 从JSON结果中解析success字段
func (a *SCFHeartbeatProber) parseSuccessFromJSON(result string) (bool, bool) {
	if result == "" {
		return false, false
	}

	resultData, ok := a.parseJSONResult(result)
	if !ok {
		return false, false
	}

	successValue, exists := resultData["success"]
	if !exists {
		return false, false
	}
	successBool, ok := successValue.(bool)
	return successBool, ok
}

// parseJSONResult 解析JSON结果
func (a *SCFHeartbeatProber) parseJSONResult(result string) (map[string]interface{}, bool) {
	var resultData map[string]interface{}
	if err := json.Unmarshal([]byte(result), &resultData); err != nil {
		return nil, false
	}
	return resultData, true
}
