package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	setupRemoteAddress     = "127.0.0.1:11110"
	sysDeployRemoteAddress = "127.0.0.1:11109"
	maxResponseBytes       = 1 << 20
	TradeGatewayHTTPSPort  = 11001
)

var storageDeploymentNames = []string{
	"storage-primary", "storage-view",
}

type Forwarder interface {
	ForwardLocal(context.Context, string) (net.Listener, error)
}

type Client struct {
	forwarder Forwarder
	timeout   time.Duration
}

type Space struct {
	SpaceID        string `json:"space_id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	Owner          string `json:"owner"`
	Market         string `json:"market"`
	Timezone       string `json:"timezone"`
	Status         string `json:"status"`
	AttributesJSON string `json:"attributes_json"`
}

type ApplyResult struct {
	Action          string `json:"action"`
	Users           int    `json:"users"`
	Secrets         int    `json:"secrets"`
	Hosts           int    `json:"hosts"`
	Spaces          int    `json:"spaces"`
	SpacesCreated   int    `json:"spaces_created"`
	SpacesUnchanged int    `json:"spaces_unchanged"`
}

type StatusResult struct {
	State     string `json:"state"`
	Users     int    `json:"users"`
	Secrets   int    `json:"secrets"`
	Hosts     int    `json:"hosts"`
	Spaces    int    `json:"spaces"`
	Missing   int    `json:"missing"`
	Conflicts int    `json:"conflicts"`
}

type StoragePlacementResult struct {
	Deployments int `json:"deployments"`
}

// TradeDeploymentSnapshot preserves the control-plane rows that existed
// before a Trade upgrade. Rollback restores these exact rows instead of
// disabling a previously healthy route.
type TradeDeploymentSnapshot struct {
	Rows                map[string]*pb.ServiceDeployment
	ControlTradeConsole *pb.ServiceDeployment
	GatewayNode         *pb.GatewayNode
}

func (c *Client) SnapshotTradeDeployments(ctx context.Context, nodeID string) (TradeDeploymentSnapshot, error) {
	nodeID = strings.TrimSpace(nodeID)
	if c == nil || c.forwarder == nil || nodeID == "" {
		return TradeDeploymentSnapshot{}, fmt.Errorf("trade_snapshot_invalid")
	}
	snapshot := TradeDeploymentSnapshot{Rows: make(map[string]*pb.ServiceDeployment, 3)}
	for _, name := range []string{"moox_trade", "trade_owner", "trade_console"} {
		response := &pb.GetServiceDeploymentRsp{}
		if err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "GetServiceDeployment", &pb.GetServiceDeploymentReq{NodeId: nodeID, ServiceName: name}, response); err != nil {
			return TradeDeploymentSnapshot{}, fmt.Errorf("trade_snapshot_failed")
		}
		if response.GetRetInfo().GetCode() == pb.ErrorCode_NOT_FOUND {
			continue
		}
		if err := checkRetInfo(response.GetRetInfo()); err != nil || response.GetDeployment() == nil {
			return TradeDeploymentSnapshot{}, fmt.Errorf("trade_snapshot_failed")
		}
		snapshot.Rows[name] = proto.Clone(response.GetDeployment()).(*pb.ServiceDeployment)
	}
	control := &pb.GetServiceDeploymentRsp{}
	if err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "GetServiceDeployment", &pb.GetServiceDeploymentReq{NodeId: "control", ServiceName: "trade_console"}, control); err != nil {
		return TradeDeploymentSnapshot{}, fmt.Errorf("trade_snapshot_failed")
	}
	if control.GetRetInfo().GetCode() == pb.ErrorCode_SUCCESS && control.GetDeployment() != nil {
		snapshot.ControlTradeConsole = proto.Clone(control.GetDeployment()).(*pb.ServiceDeployment)
	} else if control.GetRetInfo().GetCode() != pb.ErrorCode_NOT_FOUND {
		return TradeDeploymentSnapshot{}, fmt.Errorf("trade_snapshot_failed")
	}
	nodes := &pb.ListGatewayNodesRsp{}
	if err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "ListGatewayNodes", &pb.ListGatewayNodesReq{NodeId: nodeID}, nodes); err != nil || checkRetInfo(nodes.GetRetInfo()) != nil {
		return TradeDeploymentSnapshot{}, fmt.Errorf("trade_snapshot_failed")
	}
	for _, node := range nodes.GetNodes() {
		if node != nil && node.GetNodeId() == nodeID {
			snapshot.GatewayNode = proto.Clone(node).(*pb.GatewayNode)
			break
		}
	}
	return snapshot, nil
}

func (c *Client) RestoreTradeDeployments(ctx context.Context, nodeID string, snapshot TradeDeploymentSnapshot) error {
	nodeID = strings.TrimSpace(nodeID)
	if c == nil || c.forwarder == nil || nodeID == "" {
		return fmt.Errorf("trade_snapshot_invalid")
	}
	var restoreErr error
	for _, name := range []string{"moox_trade", "trade_owner", "trade_console"} {
		if row := snapshot.Rows[name]; row != nil {
			row = proto.Clone(row).(*pb.ServiceDeployment)
			if err := c.upsertDeployment(ctx, row); err != nil {
				restoreErr = errors.Join(restoreErr, fmt.Errorf("restore %s: %w", name, err))
			}
			continue
		}
		if err := c.disableServiceDeployment(ctx, nodeID, name); err != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("disable new %s: %w", name, err))
		}
	}
	if row := snapshot.ControlTradeConsole; row != nil {
		if err := c.upsertDeployment(ctx, row); err != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("restore control trade_console: %w", err))
		}
	} else if err := c.disableServiceDeployment(ctx, "control", "trade_console"); err != nil {
		restoreErr = errors.Join(restoreErr, fmt.Errorf("disable new control trade_console: %w", err))
	}
	if snapshot.GatewayNode != nil {
		node := proto.Clone(snapshot.GatewayNode).(*pb.GatewayNode)
		response := &pb.UpdateGatewayNodeRsp{}
		if err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "UpdateGatewayNode", &pb.UpdateGatewayNodeReq{NodeId: nodeID, Node: node}, response); err != nil || checkRetInfo(response.GetRetInfo()) != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("restore gateway node: gateway_node_update_failed"))
		}
	} else {
		// The setup API has no delete operation. Disable a node created by the
		// failed attempt so it cannot receive a stale or partial route snapshot.
		nodes := &pb.ListGatewayNodesRsp{}
		if err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "ListGatewayNodes", &pb.ListGatewayNodesReq{NodeId: nodeID}, nodes); err != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("find new gateway node: %w", err))
		} else if err := checkRetInfo(nodes.GetRetInfo()); err != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("find new gateway node: %w", err))
		} else {
			for _, node := range nodes.GetNodes() {
				if node == nil || node.GetNodeId() != nodeID {
					continue
				}
				copy := proto.Clone(node).(*pb.GatewayNode)
				copy.Status = "disabled"
				response := &pb.UpdateGatewayNodeRsp{}
				if err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "UpdateGatewayNode", &pb.UpdateGatewayNodeReq{NodeId: nodeID, Node: copy}, response); err != nil || checkRetInfo(response.GetRetInfo()) != nil {
					restoreErr = errors.Join(restoreErr, fmt.Errorf("disable new gateway node: gateway_node_update_failed"))
				}
				break
			}
		}
	}
	return restoreErr
}

// ApplyTradeConsolePlacement records the browser-facing TradeConsole endpoint
// on the control node. Trade may run on a dedicated execution node, while the
// Admin browser gateway still resolves the canonical trade_console row from
// its own node. Keeping this placement explicit prevents a control deploy from
// silently restoring the loopback seed (127.0.0.1:11200).
func (c *Client) ApplyTradeConsolePlacement(ctx context.Context, host string) error {
	return c.applyTradeConsolePlacement(ctx, host, "")
}

// ApplyTradeConsolePlacementForNode additionally records the authenticated
// remote Gateway origin used by Admin's browser BFF. The direct TradeConsole
// listener stays loopback-only on the execution node.
func (c *Client) ApplyTradeConsolePlacementForNode(ctx context.Context, host, nodeID string) error {
	return c.applyTradeConsolePlacement(ctx, host, nodeID)
}

func (c *Client) applyTradeConsolePlacement(ctx context.Context, host, nodeID string) error {
	host = strings.TrimSpace(host)
	if c == nil || c.forwarder == nil || host == "" {
		return fmt.Errorf("trade_placement_invalid")
	}
	if !validTradePlacementHost(host) {
		return fmt.Errorf("trade_placement_invalid")
	}
	getResponse := &pb.GetServiceDeploymentRsp{}
	if err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "GetServiceDeployment",
		&pb.GetServiceDeploymentReq{NodeId: "control", ServiceName: "trade_console"}, getResponse); err != nil {
		return fmt.Errorf("trade_placement_failed")
	}
	var deployment *pb.ServiceDeployment
	if getResponse.GetRetInfo().GetCode() == pb.ErrorCode_NOT_FOUND {
		deployment = &pb.ServiceDeployment{}
	} else {
		if err := checkRetInfo(getResponse.GetRetInfo()); err != nil || getResponse.GetDeployment() == nil {
			return fmt.Errorf("trade_placement_failed")
		}
		deployment = proto.Clone(getResponse.GetDeployment()).(*pb.ServiceDeployment)
	}
	deployment.NodeId = "control"
	deployment.ServiceName = "trade_console"
	deployment.ServiceKind = "trade"
	deployment.Protocol = "http"
	deployment.Host = host
	deployment.Port = 11200
	deployment.GatewayPath = "trpc.moox.trade.TradeConsoleService"
	deployment.Scope = "internal"
	deployment.Status = "active"
	deployment.GatewayEnabled = false
	if strings.TrimSpace(nodeID) != "" {
		deployment.ExtraConfig = tradeConsoleGatewayExtra(deployment.ExtraConfig, host, nodeID)
	}
	if err := c.upsertDeployment(ctx, deployment); err != nil {
		return fmt.Errorf("trade_placement_failed")
	}
	return nil
}

func validTradePlacementHost(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsLoopback() && !ip.IsUnspecified() && !ip.IsMulticast() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast()
	}
	if len(host) == 0 || len(host) > 253 || strings.Contains(host, "..") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-') {
				return false
			}
		}
	}
	return true
}

func tradeConsoleGatewayExtra(raw, host, nodeID string) string {
	values := make(map[string]any)
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &values)
	}
	values["gateway_url"] = "https://" + net.JoinHostPort(host, strconv.Itoa(TradeGatewayHTTPSPort))
	values["gateway_node"] = strings.TrimSpace(nodeID)
	encoded, err := json.Marshal(values)
	if err != nil {
		return raw
	}
	return string(encoded)
}

// VerifyTradeOwnerRoute confirms that Admin has compiled the dedicated
// Strategy-only route for the target Gateway node. A registry row alone is
// not enough: the Gateway may still hold a stale/disabled route snapshot.
func (c *Client) VerifyTradeOwnerRoute(ctx context.Context, nodeID string) error {
	nodeID = strings.TrimSpace(nodeID)
	if c == nil || c.forwarder == nil || nodeID == "" {
		return fmt.Errorf("trade_route_invalid")
	}
	response := &pb.GetGatewayNodeRoutesRsp{}
	if err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "GetGatewayNodeRoutes", &pb.GetGatewayNodeRoutesReq{NodeId: nodeID}, response); err != nil {
		return fmt.Errorf("trade_route_probe_failed")
	}
	if err := checkRetInfo(response.GetRetInfo()); err != nil || response.GetDisabled() || response.GetRouteHash() == "" {
		return fmt.Errorf("trade_route_probe_failed")
	}
	// GetGatewayNodeRoutes is the desired snapshot compiled by Admin. The
	// destination Gateway refreshes on a timer, so poll its applied hash and
	// heartbeat for a bounded window instead of declaring a healthy deployment
	// failed merely because the first refresh has not run yet.
	deadline := time.Now().Add(30 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	for {
		nodes := &pb.ListGatewayNodesRsp{}
		if err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "ListGatewayNodes", &pb.ListGatewayNodesReq{NodeId: nodeID}, nodes); err == nil && checkRetInfo(nodes.GetRetInfo()) == nil {
			var observed *pb.GatewayNode
			for _, candidate := range nodes.GetNodes() {
				if candidate != nil && candidate.GetNodeId() == nodeID {
					observed = candidate
					break
				}
			}
			if observed != nil && observed.GetStatus() == "enabled" && observed.GetAppliedRouteHash() == response.GetRouteHash() && observed.GetLastError() == "" && observed.GetLastSeenAt() != "" {
				lastSeen, parseErr := time.Parse(time.RFC3339Nano, observed.GetLastSeenAt())
				if parseErr == nil && time.Since(lastSeen) <= 5*time.Minute && !lastSeen.After(time.Now().Add(30*time.Second)) {
					break
				}
			}
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("trade_route_probe_failed")
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("trade_route_probe_failed")
		case <-timer.C:
		}
	}
	ownerFound, consoleFound := false, false
	for _, route := range response.GetRoutes() {
		if route == nil || route.GetAddress() != "127.0.0.1:11200" {
			continue
		}
		switch route.GetServiceId() {
		case "trade_owner":
			if route.GetServicePath() != "trpc.moox.trade.TradeConsoleService" {
				return fmt.Errorf("trade_route_probe_failed")
			}
			if len(route.GetAllowedCallers()) != 1 || route.GetAllowedCallers()[0] != "strategy" {
				return fmt.Errorf("trade_route_probe_failed")
			}
			for _, method := range []string{"GetLogicalAccount", "ClaimLogicalAccountOwner", "ReleaseLogicalAccountOwner", "RebindLogicalAccountOwner"} {
				if !containsString(route.GetAllowedMethods(), method) {
					return fmt.Errorf("trade_route_probe_failed")
				}
			}
			ownerFound = true
		case "trade_console":
			if route.GetServicePath() != "trpc.moox.trade.TradeConsoleAdminService" {
				return fmt.Errorf("trade_route_probe_failed")
			}
			if len(route.GetAllowedCallers()) != 1 || route.GetAllowedCallers()[0] != "admin-gateway" {
				return fmt.Errorf("trade_route_probe_failed")
			}
			for _, method := range tradeConsoleGatewayMethods {
				if !containsString(route.GetAllowedMethods(), method) {
					return fmt.Errorf("trade_route_probe_failed")
				}
			}
			for _, method := range []string{"ClaimLogicalAccountOwner", "ReleaseLogicalAccountOwner", "RebindLogicalAccountOwner"} {
				if containsString(route.GetAllowedMethods(), method) {
					return fmt.Errorf("trade_route_probe_failed")
				}
			}
			consoleFound = true
		}
	}
	if ownerFound && consoleFound {
		return nil
	}
	return fmt.Errorf("trade_route_probe_failed")
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func New(forwarder Forwarder) *Client {
	return &Client{forwarder: forwarder, timeout: 30 * time.Second}
}

func (c *Client) Apply(ctx context.Context, snapshot *setupconfig.Snapshot) (ApplyResult, error) {
	return c.ApplyWithSpaces(ctx, snapshot, nil)
}

func (c *Client) ApplyWithSpaces(
	ctx context.Context,
	snapshot *setupconfig.Snapshot,
	spaces []Space,
) (ApplyResult, error) {
	if snapshot == nil || c.forwarder == nil {
		return ApplyResult{}, fmt.Errorf("setup_client_invalid")
	}
	if err := snapshot.VerifyUnchanged(); err != nil {
		return ApplyResult{}, fmt.Errorf("config_changed")
	}
	request := applyRequest(snapshot.Manifest, spaces)
	response := &pb.ApplySetupRsp{}
	if err := c.forwardedPost(ctx, "ApplySetup", request, response); err != nil {
		return ApplyResult{}, err
	}
	if err := snapshot.VerifyUnchanged(); err != nil {
		return ApplyResult{}, fmt.Errorf("config_changed")
	}
	if err := checkRetInfo(response.GetRetInfo()); err != nil {
		return ApplyResult{}, err
	}
	return ApplyResult{
		Action: response.GetAction(), Users: int(response.GetUsers()),
		Secrets: int(response.GetSecrets()), Hosts: int(response.GetHosts()),
		Spaces: int(response.GetSpaces()), SpacesCreated: int(response.GetSpacesCreated()),
		SpacesUnchanged: int(response.GetSpacesUnchanged()),
	}, nil
}

func (c *Client) Status(ctx context.Context, snapshot *setupconfig.Snapshot) (StatusResult, error) {
	return c.StatusWithSpaces(ctx, snapshot, nil)
}

func (c *Client) StatusWithSpaces(
	ctx context.Context,
	snapshot *setupconfig.Snapshot,
	spaces []Space,
) (StatusResult, error) {
	if snapshot == nil || c.forwarder == nil {
		return StatusResult{}, fmt.Errorf("setup_client_invalid")
	}
	if err := snapshot.VerifyUnchanged(); err != nil {
		return StatusResult{}, fmt.Errorf("config_changed")
	}
	request := statusRequest(snapshot.Manifest, spaces)
	response := &pb.GetSetupStatusRsp{}
	if err := c.forwardedPost(ctx, "GetSetupStatus", request, response); err != nil {
		return StatusResult{}, err
	}
	if err := snapshot.VerifyUnchanged(); err != nil {
		return StatusResult{}, fmt.Errorf("config_changed")
	}
	if err := checkRetInfo(response.GetRetInfo()); err != nil {
		return StatusResult{}, err
	}
	return StatusResult{
		State: response.GetState(), Users: int(response.GetUsers()), Secrets: int(response.GetSecrets()),
		Hosts: int(response.GetHosts()), Spaces: int(response.GetSpaces()),
		Missing: int(response.GetMissing()), Conflicts: int(response.GetConflicts()),
	}, nil
}

func (c *Client) ApplyStoragePlacement(ctx context.Context, nodeID, host string) (StoragePlacementResult, error) {
	if _, err := c.PrepareStoragePlacement(ctx, nodeID, host); err != nil {
		return StoragePlacementResult{}, err
	}
	// The HTTP gateway remains on control for CloudNode and other control-plane
	// RPCs. Only the native Storage ingress moves to the Storage host.
	getResponse := &pb.GetServiceDeploymentRsp{}
	if err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "GetServiceDeployment",
		&pb.GetServiceDeploymentReq{NodeId: "control", ServiceName: "service_gateway_native"}, getResponse); err != nil || checkRetInfo(getResponse.GetRetInfo()) != nil || getResponse.GetDeployment() == nil {
		return StoragePlacementResult{}, fmt.Errorf("storage_placement_failed")
	}
	deployment := proto.Clone(getResponse.GetDeployment()).(*pb.ServiceDeployment)
	deployment.Host = strings.TrimSpace(host)
	if err := c.upsertDeployment(ctx, deployment); err != nil {
		return StoragePlacementResult{}, fmt.Errorf("storage_placement_failed")
	}
	return StoragePlacementResult{Deployments: len(storageDeploymentNames)}, nil
}

// ActivateStoragePlacement enables the local Storage routes after a control
// host receives the separately managed Storage package. Initial control-plane
// setup intentionally marks these deployments disabled when Storage has not
// been installed yet; leaving them disabled means the native gateway cannot
// resolve PrimaryStore/Metadata requests even though the processes are ready.
func (c *Client) ActivateStoragePlacement(ctx context.Context, nodeID string) (StoragePlacementResult, error) {
	nodeID = strings.TrimSpace(nodeID)
	if c == nil || c.forwarder == nil || nodeID == "" {
		return StoragePlacementResult{}, fmt.Errorf("storage_placement_invalid")
	}
	for _, serviceName := range storageDeploymentNames {
		getResponse := &pb.GetServiceDeploymentRsp{}
		if err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "GetServiceDeployment",
			&pb.GetServiceDeploymentReq{NodeId: nodeID, ServiceName: serviceName}, getResponse); err != nil {
			return StoragePlacementResult{}, fmt.Errorf("storage_placement_failed")
		}
		if err := checkRetInfo(getResponse.GetRetInfo()); err != nil || getResponse.GetDeployment() == nil {
			return StoragePlacementResult{}, fmt.Errorf("storage_placement_failed")
		}
		deployment := proto.Clone(getResponse.GetDeployment()).(*pb.ServiceDeployment)
		deployment.Status = "active"
		deployment.GatewayEnabled = true
		if err := c.upsertDeployment(ctx, deployment); err != nil {
			return StoragePlacementResult{}, fmt.Errorf("storage_placement_failed")
		}
	}
	return StoragePlacementResult{Deployments: len(storageDeploymentNames)}, nil
}

// PrepareStoragePlacement creates the remote Gateway node and its local
// routes before the Gateway starts. It deliberately leaves SCF discovery on
// the current native endpoint until Storage readiness has succeeded.
func (c *Client) PrepareStoragePlacement(ctx context.Context, nodeID, host string) (StoragePlacementResult, error) {
	nodeID, host = strings.TrimSpace(nodeID), strings.TrimSpace(host)
	if c == nil || c.forwarder == nil || nodeID == "" || host == "" {
		return StoragePlacementResult{}, fmt.Errorf("storage_placement_invalid")
	}
	if err := c.ensureGatewayNode(ctx, nodeID, host); err != nil {
		return StoragePlacementResult{}, fmt.Errorf("storage_placement_failed")
	}
	for _, serviceName := range storageDeploymentNames {
		getResponse := &pb.GetServiceDeploymentRsp{}
		if err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "GetServiceDeployment",
			&pb.GetServiceDeploymentReq{NodeId: "control", ServiceName: serviceName}, getResponse); err != nil {
			return StoragePlacementResult{}, fmt.Errorf("storage_placement_failed")
		}
		if err := checkRetInfo(getResponse.GetRetInfo()); err != nil || getResponse.GetDeployment() == nil {
			return StoragePlacementResult{}, fmt.Errorf("storage_placement_failed")
		}
		deployment := proto.Clone(getResponse.GetDeployment()).(*pb.ServiceDeployment)
		deployment.Id = 0
		deployment.NodeId = nodeID
		// Each remote Gateway reaches its local Storage processes through loopback.
		// The public address belongs only to the Gateway node, not to its routes.
		deployment.Host = "127.0.0.1"
		if err := c.upsertDeployment(ctx, deployment); err != nil {
			return StoragePlacementResult{}, fmt.Errorf("storage_placement_failed")
		}
	}
	return StoragePlacementResult{Deployments: len(storageDeploymentNames)}, nil
}

func (c *Client) ensureGatewayNode(ctx context.Context, nodeID, host string) error {
	return c.ensureGatewayNodeAt(ctx, nodeID, host, TradeGatewayHTTPSPort)
}

func (c *Client) ensureGatewayNodeAt(ctx context.Context, nodeID, host string, port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("gateway_node_port_invalid")
	}
	response := &pb.ListGatewayNodesRsp{}
	if err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "ListGatewayNodes", &pb.ListGatewayNodesReq{NodeId: nodeID}, response); err != nil || checkRetInfo(response.GetRetInfo()) != nil {
		return fmt.Errorf("gateway_node_lookup_failed")
	}
	desiredAddress := "https://" + net.JoinHostPort(host, strconv.Itoa(port))
	node := &pb.GatewayNode{NodeId: nodeID, Name: nodeID, PublicAddress: desiredAddress, Status: "enabled"}
	if len(response.GetNodes()) == 0 {
		created := &pb.CreateGatewayNodeRsp{}
		if err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "CreateGatewayNode", &pb.CreateGatewayNodeReq{Node: node}, created); err != nil || checkRetInfo(created.GetRetInfo()) != nil {
			return fmt.Errorf("gateway_node_create_failed")
		}
		return nil
	}
	// Preserve operator-managed fields (notably HostId and Name) when
	// reconciling an existing node. Sending a sparse replacement would clear
	// the SSH host association and could unexpectedly re-enable a disabled node.
	existing := response.GetNodes()[0]
	if existing != nil {
		node = proto.Clone(existing).(*pb.GatewayNode)
		node.NodeId = nodeID
		node.PublicAddress = desiredAddress
	}
	updated := &pb.UpdateGatewayNodeRsp{}
	if err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "UpdateGatewayNode", &pb.UpdateGatewayNodeReq{NodeId: nodeID, Node: node}, updated); err != nil || checkRetInfo(updated.GetRetInfo()) != nil {
		return fmt.Errorf("gateway_node_update_failed")
	}
	return nil
}

func (c *Client) upsertDeployment(ctx context.Context, deployment *pb.ServiceDeployment) error {
	getResponse := &pb.GetServiceDeploymentRsp{}
	err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "GetServiceDeployment", &pb.GetServiceDeploymentReq{NodeId: deployment.GetNodeId(), ServiceName: deployment.GetServiceName()}, getResponse)
	if err == nil && checkRetInfo(getResponse.GetRetInfo()) == nil {
		response := &pb.UpdateServiceDeploymentRsp{}
		if err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "UpdateServiceDeployment", &pb.UpdateServiceDeploymentReq{NodeId: deployment.GetNodeId(), ServiceName: deployment.GetServiceName(), Deployment: deployment}, response); err != nil || checkRetInfo(response.GetRetInfo()) != nil {
			return fmt.Errorf("deployment_update_failed")
		}
		return nil
	}
	response := &pb.CreateServiceDeploymentRsp{}
	if err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "CreateServiceDeployment", &pb.CreateServiceDeploymentReq{Deployment: deployment}, response); err != nil || checkRetInfo(response.GetRetInfo()) != nil {
		return fmt.Errorf("deployment_create_failed")
	}
	return nil
}

func (c *Client) forwardedPost(ctx context.Context, method string, request, response proto.Message) error {
	return c.forwardedPostTo(ctx, setupRemoteAddress, "trpc.moox.admin.Setup", method, request, response)
}

func (c *Client) forwardedPostTo(ctx context.Context, remoteAddress, service, method string, request, response proto.Message) error {
	forwardContext, cancel := context.WithCancel(ctx)
	defer cancel()
	listener, err := c.forwarder.ForwardLocal(forwardContext, remoteAddress)
	if err != nil {
		return fmt.Errorf("setup_not_reachable")
	}
	defer listener.Close()
	endpoint := "http://" + listener.Addr().String() + "/" + service + "/" + method
	return postProto(ctx, endpoint, request, response, c.timeout)
}

func postProto(ctx context.Context, endpoint string, request, response proto.Message, timeout time.Duration) error {
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(request)
	if err != nil {
		return fmt.Errorf("setup_request_invalid")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("setup_request_invalid")
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpClient := &http.Client{Timeout: timeout}
	httpResponse, err := httpClient.Do(httpRequest)
	if err != nil {
		return fmt.Errorf("setup_not_reachable")
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(httpResponse.Body, maxResponseBytes))
		return fmt.Errorf("setup_remote_failed")
	}
	body, err := io.ReadAll(io.LimitReader(httpResponse.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return fmt.Errorf("setup_response_invalid")
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(body, response); err != nil {
		return fmt.Errorf("setup_response_invalid")
	}
	return nil
}

func checkRetInfo(retInfo *pb.RetInfo) error {
	if retInfo == nil {
		return fmt.Errorf("setup_response_invalid")
	}
	if retInfo.GetCode() == pb.ErrorCode_SUCCESS {
		return nil
	}
	switch strings.TrimSpace(retInfo.GetMsg()) {
	case "setup_conflict", "setup_invalid", "setup_storage_failed":
		return fmt.Errorf("%s", retInfo.GetMsg())
	default:
		return fmt.Errorf("setup_remote_failed")
	}
}

func applyRequest(manifest setupconfig.Manifest, spaces []Space) *pb.ApplySetupReq {
	return &pb.ApplySetupReq{
		Admin:        &pb.SetupAdmin{Username: manifest.Admin.Username, Password: manifest.Admin.Password},
		TencentCloud: &pb.SetupTencentCloud{SecretId: manifest.TencentCloud.SecretID, SecretKey: manifest.TencentCloud.SecretKey},
		ControlHost:  hostToPB(manifest.ControlHost),
		OtherHosts:   hostsToPB(manifest.OtherHosts),
		Spaces:       spacesToPB(spaces),
	}
}

func statusRequest(manifest setupconfig.Manifest, spaces []Space) *pb.GetSetupStatusReq {
	return &pb.GetSetupStatusReq{
		Admin:        &pb.SetupAdmin{Username: manifest.Admin.Username, Password: manifest.Admin.Password},
		TencentCloud: &pb.SetupTencentCloud{SecretId: manifest.TencentCloud.SecretID, SecretKey: manifest.TencentCloud.SecretKey},
		ControlHost:  hostToPB(manifest.ControlHost),
		OtherHosts:   hostsToPB(manifest.OtherHosts),
		Spaces:       spacesToPB(spaces),
	}
}

func spacesToPB(spaces []Space) []*pb.SetupSpace {
	result := make([]*pb.SetupSpace, 0, len(spaces))
	for _, space := range spaces {
		result = append(result, &pb.SetupSpace{
			SpaceId: space.SpaceID, Name: space.Name, Description: space.Description,
			Owner: space.Owner, Market: space.Market, Timezone: space.Timezone,
			Status: space.Status, AttributesJson: space.AttributesJSON,
		})
	}
	return result
}

func hostsToPB(hosts []setupconfig.Host) []*pb.SetupHost {
	result := make([]*pb.SetupHost, 0, len(hosts))
	for _, host := range hosts {
		result = append(result, hostToPB(host))
	}
	return result
}

func hostToPB(host setupconfig.Host) *pb.SetupHost {
	return &pb.SetupHost{
		Name: host.Name, Address: host.Address, Port: int32(host.Port),
		Username: host.Username, Password: host.Password,
	}
}
