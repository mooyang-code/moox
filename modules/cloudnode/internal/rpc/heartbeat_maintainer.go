package rpc

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	keepaliveProbeSource    = "keepalive_probe"
	keepaliveBatchSize      = 10
	keepaliveMaxConcurrency = keepaliveBatchSize
	// Fifty nodes are rotated in five 9-second timer ticks. One minute keeps
	// adjacent invocations overlapped while leaving room for a job to finish.
	keepaliveResidentSec = 60
)

// HeartbeatTargets are the control-plane endpoints an SCF runtime needs to
// report its own heartbeat and reach Storage.
type HeartbeatTargets struct {
	ServiceGatewayTarget    string
	StorageRPCGatewayTarget string
}

// HeartbeatMaintainer periodically communicates with published SCF nodes.
// Heartbeats remain owned by the SCF runtime; this component only invokes it.
type HeartbeatMaintainer struct {
	service   *Service
	targets   HeartbeatTargets
	now       func() time.Time
	requestID func(string, time.Time) string
	mu        sync.Mutex
	next      int
}

// NewHeartbeatMaintainer creates the CloudNode-owned SCF communication timer.
func NewHeartbeatMaintainer(service *Service, targets HeartbeatTargets) *HeartbeatMaintainer {
	targets.ServiceGatewayTarget = strings.TrimSpace(targets.ServiceGatewayTarget)
	targets.StorageRPCGatewayTarget = strings.TrimSpace(targets.StorageRPCGatewayTarget)
	return &HeartbeatMaintainer{
		service: service,
		targets: targets,
		now:     time.Now,
		requestID: func(nodeID string, at time.Time) string {
			return fmt.Sprintf("keepalive_%s_%d", nodeID, at.UTC().UnixNano())
		},
	}
}

// Handle sends best-effort asynchronous Event invocations to eligible SCF
// nodes. A small rotating batch prevents a slow provider call from starving the
// same tail nodes on every timer tick.
func (m *HeartbeatMaintainer) Handle(ctx context.Context) error {
	if m == nil || m.service == nil || m.service.catalog == nil {
		return nil
	}
	nodes, err := m.service.catalog.ListSCFEventNodes(ctx)
	if err != nil {
		log.WarnContextf(ctx, "cloudnode_scf_keepalive_list_failed error=%q", err)
		return nil
	}
	if len(nodes) == 0 {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	start := m.next % len(nodes)
	limit := min(keepaliveBatchSize, len(nodes))
	sem := make(chan struct{}, keepaliveMaxConcurrency)
	var wg sync.WaitGroup
	attempted := 0
	for attempted < limit {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			m.next = (start + max(attempted, 1)) % len(nodes)
			return nil
		}
		node := nodes[(start+attempted)%len(nodes)]
		attempted++
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			m.invoke(ctx, &node)
		}()
	}
	wg.Wait()
	m.next = (start + max(attempted, 1)) % len(nodes)
	return nil
}

func (m *HeartbeatMaintainer) invoke(ctx context.Context, node *store.CloudNode) {
	at := m.now().UTC()
	payload := keepalivePayload{
		Action:                  "keepalive",
		Source:                  keepaliveProbeSource,
		Timestamp:               at.Format(time.RFC3339Nano),
		RequestID:               m.requestID(node.NodeID, at),
		Data:                    keepaliveIdentity{NodeID: node.NodeID, ResidentSeconds: keepaliveResidentSec},
		ServiceGatewayTarget:    m.targets.ServiceGatewayTarget,
		StorageRPCGatewayTarget: m.targets.StorageRPCGatewayTarget,
	}
	_, err := m.service.invokeNode(ctx, node, payload, "Event", "")
	if err != nil {
		log.WarnContextf(ctx, "cloudnode_scf_keepalive_invoke_failed space_id=%s node_id=%s function_name=%s error=%q",
			node.SpaceID, node.NodeID, node.FunctionName, err)
		return
	}
	log.DebugContextf(ctx, "cloudnode_scf_keepalive_invoked space_id=%s node_id=%s function_name=%s request_id=%s",
		node.SpaceID, node.NodeID, node.FunctionName, payload.RequestID)
}

type keepalivePayload struct {
	Action                  string            `json:"action"`
	Source                  string            `json:"source"`
	Timestamp               string            `json:"timestamp"`
	RequestID               string            `json:"request_id"`
	Data                    keepaliveIdentity `json:"data"`
	ServiceGatewayTarget    string            `json:"service_gateway_target"`
	StorageRPCGatewayTarget string            `json:"storage_rpc_gateway_target"`
}

type keepaliveIdentity struct {
	NodeID          string `json:"node_id"`
	ResidentSeconds int    `json:"resident_seconds"`
}
