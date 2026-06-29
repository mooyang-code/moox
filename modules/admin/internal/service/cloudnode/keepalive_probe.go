package cloudnode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/common"
	"github.com/mooyang-code/moox/modules/admin/internal/config"
	"github.com/mooyang-code/moox/modules/admin/internal/gateway"
	"github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/provider"
	"github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/types"

	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	keepaliveAction = "keepalive"
	keepaliveSource = "keepalive_probe"
)

var keepaliveProbeBatchSize = 20

// RunKeepaliveProbe 运行保活探测任务
func (s *ServiceImpl) RunKeepaliveProbe(ctx context.Context) error {
	s.init()
	if s.probeStore == nil {
		return fmt.Errorf("probe store not initialized")
	}

	nodes, err := s.nodeDAO.ListProbeEnabledNodes(ctx)
	if err != nil {
		return fmt.Errorf("load probe nodes failed: %w", err)
	}
	if len(nodes) == 0 {
		log.InfoContextf(ctx, "[Keepalive] No probe-enabled nodes found")
		return nil
	}

	batchSize := keepaliveProbeBatchSize
	if batchSize <= 0 {
		batchSize = 20
	}

	var successCount int64
	var failCount int64
	serverIP, serverPort := getServerAddress()
	for start := 0; start < len(nodes); start += batchSize {
		end := start + batchSize
		if end > len(nodes) {
			end = len(nodes)
		}

		handlers := make([]func() error, 0, end-start)
		for _, node := range nodes[start:end] {
			if node == nil {
				continue
			}
			node := node
			handlers = append(handlers, func() error {
				probeTime := time.Now()
				var ok bool
				if node.NodeType == model.NodeTypeSCFWeb {
					ok = s.probeHTTP(ctx, node, serverIP, serverPort)
				} else {
					eventData := s.buildKeepaliveEventData(ctx, serverIP, serverPort, node.NodeID)
					ok = s.invokeKeepalive(ctx, node, eventData)
				}
				s.probeStore.UpdateProbe(node.NodeID, probeTime, ok)
				if ok {
					s.updateSCFEventHeartbeatFromKeepalive(node, probeTime)
					atomic.AddInt64(&successCount, 1)
				} else {
					atomic.AddInt64(&failCount, 1)
				}
				return nil
			})
		}
		if len(handlers) == 0 {
			continue
		}
		if err := trpc.GoAndWait(handlers...); err != nil {
			log.WarnContextf(ctx, "[Keepalive] Batch probe encountered error: %v", err)
		}
	}

	log.InfoContextf(ctx, "[Keepalive] Completed keepalive probe: total=%d, success=%d, failed=%d",
		len(nodes), successCount, failCount)
	return nil
}

func (s *ServiceImpl) invokeKeepalive(ctx context.Context, node *model.CloudNode, eventData map[string]interface{}) bool {
	if node.NodeID == "" || node.CloudAccountID == "" {
		log.WarnContextf(ctx, "[Keepalive] Invalid node data: node_id=%s, account_id=%s", node.NodeID, node.CloudAccountID)
		return false
	}

	// 使用独立的超时上下文，避免与父 ctx 的 deadline 累加
	invokeCtx := trpc.CloneContext(ctx)

	client := s.providerFactory.GetCloudProviderByAccount(node.CloudAccountID)
	_, err := client.InvokeFunction(invokeCtx, &provider.InvokeFunctionRequest{
		FunctionName: node.NodeID,
		Namespace:    node.Namespace,
		Region:       node.Region,
		EventData:    eventData,
		InvokeType:   InvokeTypeSync,
	})
	if err != nil {
		log.WarnContextf(ctx, "[Keepalive] Invoke keepalive failed: node_id=%s, error=%v", node.NodeID, err)
		return false
	}
	return true
}

func (s *ServiceImpl) updateSCFEventHeartbeatFromKeepalive(node *model.CloudNode, heartbeatTime time.Time) {
	if s == nil || s.heartbeatStore == nil || node == nil || node.NodeID == "" {
		return
	}
	if node.NodeType != model.NodeTypeSCFEvent {
		return
	}

	s.heartbeatStore.UpdateHeartbeat(&types.ReportHeartbeatRequest{
		NodeID:         node.NodeID,
		NodeType:       node.NodeType,
		RunningVersion: node.RunningVersion,
		SourceService:  keepaliveSource,
		Timestamp:      &heartbeatTime,
		Metadata: map[string]interface{}{
			"action":    keepaliveAction,
			"source":    keepaliveSource,
			"region":    node.Region,
			"namespace": node.Namespace,
		},
	})
}

func (s *ServiceImpl) buildKeepaliveEventData(ctx context.Context, serverIP string, serverPort int, nodeID string) map[string]interface{} {
	payload := buildKeepaliveEventData(serverIP, serverPort, nodeID)
	if s == nil || s.deployments == nil {
		return payload
	}
	deployments, err := s.deployments.GetServiceDeployments(ctx)
	if err != nil {
		log.WarnContextf(ctx, "[Keepalive] Load service deployments failed: %v", err)
		return payload
	}
	if len(deployments) == 0 {
		return payload
	}
	payload["service_deployments"] = deployments
	applyRuntimeDeploymentOverrides(payload, deployments)
	return payload
}

func buildKeepaliveEventData(serverIP string, serverPort int, nodeID string) map[string]interface{} {
	timestamp := time.Now().Format(time.RFC3339)
	requestID := fmt.Sprintf("keepalive_%d", time.Now().UnixNano())
	internalIP := common.GetInternalIP()
	payload := map[string]interface{}{
		"action":     keepaliveAction,
		"timestamp":  timestamp,
		"request_id": requestID,
		"source":     keepaliveSource,
		// 心跳回包通道：SCF collector 通过 server_ip/server_port 顶层字段更新本地
		// ServerInfo，进而主动向控制面上报心跳拉取任务实例。历史故障：keepalive
		// 事件只下发 moox_server_url（URL 字符串），collector 不解析，导致 SCF 冷
		// 启动后 ServerInfo 为空、ReportHeartbeat 直接 return nil，任务列表永不
		// 刷新，K线停采。此处与 task 事件字段保持一致，作为主通道。
		"server_ip":       serverIP,
		"server_port":     serverPort,
		"moox_server_url": fmt.Sprintf("http://%s:%d", serverIP, serverPort),
	}
	data := map[string]interface{}{
		"internal_ip": internalIP,
		"public_ip":   serverIP,
		"prober_type": keepaliveAction,
		"probe_time":  timestamp,
	}
	if nodeID != "" {
		data["node_id"] = nodeID
	}
	payload["data"] = data
	return payload
}

func applyRuntimeDeploymentOverrides(payload map[string]interface{}, deployments map[string]interface{}) {
	if serviceGatewayURL := deploymentBaseURL(deployments, "service_gateway", "admin_gateway"); serviceGatewayURL != "" {
		payload["moox_server_url"] = serviceGatewayURL
		if host, port, ok := parseDeploymentURL(serviceGatewayURL); ok {
			payload["server_ip"] = host
			payload["server_port"] = port
			if data, ok := payload["data"].(map[string]interface{}); ok {
				data["public_ip"] = host
			}
		}
	}
	if storageURL := deploymentBaseURL(deployments, "storage_access"); storageURL != "" {
		payload["storage_server_url"] = storageURL
	}
}

func deploymentBaseURL(deployments map[string]interface{}, names ...string) string {
	for _, name := range names {
		raw, ok := deployments[name]
		if !ok {
			continue
		}
		switch item := raw.(type) {
		case map[string]interface{}:
			if baseURL, ok := item["base_url"].(string); ok && baseURL != "" {
				return strings.TrimRight(baseURL, "/")
			}
		case map[string]string:
			if baseURL := item["base_url"]; baseURL != "" {
				return strings.TrimRight(baseURL, "/")
			}
		}
	}
	return ""
}

func parseDeploymentURL(raw string) (string, int, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", 0, false
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port <= 0 {
		return "", 0, false
	}
	host := u.Hostname()
	if host == "" {
		return "", 0, false
	}
	return host, port, true
}

func getServerAddress() (string, int) {
	port := config.GetGatewayPort()
	if port == 0 {
		cfg := gateway.GetConfig()
		if cfg != nil {
			port = cfg.Gateway.Port
		}
	}
	return common.GetPublicIP(), port
}

// probeHTTP 对 scf-web 节点发起 HTTP POST /probe 请求进行探测
func (s *ServiceImpl) probeHTTP(ctx context.Context, node *model.CloudNode, serverIP string, serverPort int) bool {
	probeURL, err := extractProbeURL(node)
	if err != nil {
		log.WarnContextf(ctx, "[Keepalive] HTTP probe extract URL failed: node_id=%s, error=%v", node.NodeID, err)
		return false
	}

	// 构造与 factor-calculator /probe 接口兼容的请求体
	eventData := s.buildKeepaliveEventData(ctx, serverIP, serverPort, node.NodeID)
	body, err := json.Marshal(eventData)
	if err != nil {
		log.WarnContextf(ctx, "[Keepalive] HTTP probe marshal body failed: node_id=%s, error=%v", node.NodeID, err)
		return false
	}

	// 使用独立的超时上下文，避免与父 ctx 的 deadline 累加
	probeCtx := trpc.CloneContext(ctx)

	httpClient := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(probeCtx, http.MethodPost, probeURL, bytes.NewReader(body))
	if err != nil {
		log.WarnContextf(ctx, "[Keepalive] HTTP probe create request failed: node_id=%s, error=%v", node.NodeID, err)
		return false
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		log.WarnContextf(ctx, "[Keepalive] HTTP probe request failed: node_id=%s, url=%s, error=%v", node.NodeID, probeURL, err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.WarnContextf(ctx, "[Keepalive] HTTP probe unexpected status: node_id=%s, status=%d", node.NodeID, resp.StatusCode)
		return false
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		log.WarnContextf(ctx, "[Keepalive] HTTP probe read response failed: node_id=%s, error=%v", node.NodeID, err)
		return false
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		log.WarnContextf(ctx, "[Keepalive] HTTP probe parse response failed: node_id=%s, error=%v", node.NodeID, err)
		return false
	}

	if success, ok := result["success"].(bool); ok && success {
		log.InfoContextf(ctx, "[Keepalive] HTTP probe success: node_id=%s", node.NodeID)
		return true
	}

	log.WarnContextf(ctx, "[Keepalive] HTTP probe returned success=false: node_id=%s, response=%s", node.NodeID, string(respBody))
	return false
}

// extractProbeURL 从节点 metadata 中提取 scf-web 的探测 URL
// 解析链路: metadata["function_url_trigger"] -> JSON string -> NetConfig.ExtranetHTTPUrl -> + "/probe"
// 若节点已设置 ProbeURL 字段（手动覆盖），则优先使用 ProbeURL + "/probe"
func extractProbeURL(node *model.CloudNode) (string, error) {
	// 优先使用手动配置的 ProbeURL
	if node.ProbeURL != "" {
		return strings.TrimRight(node.ProbeURL, "/") + "/probe", nil
	}

	// 从 metadata 中解析 function_url_trigger
	var metadata map[string]interface{}
	if err := json.Unmarshal([]byte(node.Metadata), &metadata); err != nil {
		return "", fmt.Errorf("parse metadata failed: %w", err)
	}

	triggerDescRaw, ok := metadata["function_url_trigger"]
	if !ok {
		return "", fmt.Errorf("function_url_trigger not found in metadata")
	}

	triggerDescStr, ok := triggerDescRaw.(string)
	if !ok {
		return "", fmt.Errorf("function_url_trigger is not a string")
	}

	// triggerDescStr 本身是一段 JSON，解析其中的 NetConfig.ExtranetHTTPUrl
	var triggerDesc struct {
		NetConfig struct {
			ExtranetHTTPUrl string `json:"ExtranetHTTPUrl"`
		} `json:"NetConfig"`
	}
	if err := json.Unmarshal([]byte(triggerDescStr), &triggerDesc); err != nil {
		return "", fmt.Errorf("parse function_url_trigger JSON failed: %w", err)
	}

	httpURL := triggerDesc.NetConfig.ExtranetHTTPUrl
	if httpURL == "" {
		return "", fmt.Errorf("ExtranetHTTPUrl is empty in function_url_trigger")
	}

	return strings.TrimRight(httpURL, "/") + "/probe", nil
}
