package cloudnode

import (
	"context"
	"fmt"
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
				eventData := buildKeepaliveEventData(serverIP, serverPort, node.NodeID)
				ok := s.invokeKeepalive(ctx, node, eventData)
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
