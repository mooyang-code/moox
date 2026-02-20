package cloudnode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/server/internal/common"
	"github.com/mooyang-code/moox/server/internal/config"
	"github.com/mooyang-code/moox/server/internal/gateway"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"

	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	keepaliveAction = "keepalive"
	keepaliveSource = "keepalive_probe"
)

var keepaliveProbeBatchSize = 20

// RunKeepaliveProbe 运行保活探测任务
// 仅用于保活，不参与在线判定
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
					eventData := buildKeepaliveEventData(serverIP, serverPort, node.NodeID)
					ok = s.invokeKeepalive(ctx, node, eventData)
				}
				s.probeStore.UpdateProbe(node.NodeID, probeTime, ok)
				if ok {
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

	client := s.providerFactory.GetCloudProviderByAccount(node.CloudAccountID)
	_, err := client.InvokeFunction(ctx, &provider.InvokeFunctionRequest{
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

func buildKeepaliveEventData(serverIP string, serverPort int, nodeID string) map[string]interface{} {
	timestamp := time.Now().Format(time.RFC3339)
	requestID := fmt.Sprintf("keepalive_%d", time.Now().UnixNano())
	internalIP := common.GetInternalIP()
	payload := map[string]interface{}{
		"action":      keepaliveAction,
		"timestamp":   timestamp,
		"request_id":  requestID,
		"source":      keepaliveSource,
		"server_ip":   serverIP,
		"server_port": serverPort,
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
	eventData := buildKeepaliveEventData(serverIP, serverPort, node.NodeID)
	body, err := json.Marshal(eventData)
	if err != nil {
		log.WarnContextf(ctx, "[Keepalive] HTTP probe marshal body failed: node_id=%s, error=%v", node.NodeID, err)
		return false
	}

	httpClient := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, probeURL, bytes.NewReader(body))
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
