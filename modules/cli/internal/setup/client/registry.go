package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"google.golang.org/protobuf/proto"
)

// tradeConsoleGatewayMethods is the authenticated browser/operator surface.
// Ownership fencing methods intentionally remain on the strategy-only
// trade_owner route and are not reachable from the Admin BFF.
var tradeConsoleGatewayMethods = []string{
	"CreateTradingAccount", "UpdateTradingAccount", "GetTradingAccount", "ListTradingAccounts",
	"SetLeverage", "SyncTradingAccount", "CreateLogicalAccount", "GetLogicalAccount",
	"ListLogicalAccounts", "UpdateLogicalAccount", "AddLogicalAccountMember", "RemoveLogicalAccountMember",
	"PauseLogicalAccount", "ResumeLogicalAccount", "FlattenLogicalAccount", "PlaceManualOrder",
	"SubmitOrder", "CancelOrder", "GetOperatorAction", "GetLogicalAccountTarget", "GetOrder",
	"ListOrders", "ListFills", "ListPositions", "CreatePaperSimulation", "ClosePaperSimulation",
	"GetExecutionCapabilities", "QueryEquityCurve", "ListHoldings",
}

// RegisterServiceDeployment records a service after setup deploy-service has
// activated it. Existing rows are updated in place so operator-owned gateway
// and monitoring settings are preserved; a missing row is created from the
// small catalog below. This keeps the Admin deployment store as the source of
// truth consumed by Monitor and the service gateway.
func (c *Client) RegisterServiceDeployment(ctx context.Context, nodeID, serviceName, host string) error {
	canonical, _ := lookupServiceDeployment(serviceName)
	if canonical == "moox_trade" && strings.TrimSpace(nodeID) != "control" {
		// Ensure the destination Gateway exists before creating the service and
		// owner route. Admin rejects deployments for unknown gateway nodes.
		if err := c.ensureGatewayNodeAt(ctx, nodeID, host, TradeGatewayHTTPSPort); err != nil {
			return fmt.Errorf("trade_gateway_node_failed: %w", err)
		}
	}
	if err := c.registerServiceDeployment(ctx, nodeID, serviceName, host); err != nil {
		return err
	}
	if canonical != "moox_trade" {
		return nil
	}
	// The ownership endpoint is a separate machine-owned route on the Trade
	// node. Never reuse the control node's browser trade_console placement.
	owner := &pb.ServiceDeployment{
		NodeId: strings.TrimSpace(nodeID), ServiceName: "trade_owner", ServiceKind: "trade",
		Protocol: "http", Host: "127.0.0.1", Port: 11200, Scope: "internal", Status: "active",
		GatewayPath: "trpc.moox.trade.TradeConsoleService", GatewayServiceId: "trade_owner", GatewayEnabled: true,
		Description: "Strategy-only logical account ownership route",
		ExtraConfig: `{"gateway_methods":["GetLogicalAccount","ClaimLogicalAccountOwner","ReleaseLogicalAccountOwner","RebindLogicalAccountOwner"],"gateway_callers":["strategy"],"monitor_enabled":false,"managed_by":"moox-cli"}`,
	}
	if err := c.upsertDeployment(ctx, owner); err != nil {
		return fmt.Errorf("trade_owner_registry_failed: %w", err)
	}
	console := &pb.ServiceDeployment{
		NodeId: strings.TrimSpace(nodeID), ServiceName: "trade_console", ServiceKind: "trade",
		Protocol: "http", Host: "127.0.0.1", Port: 11200, Scope: "internal", Status: "active",
		// Dedicated Trade nodes expose the browser/operator methods through a
		// distinct native service path. This keeps the strategy ownership route
		// on the canonical path and makes route snapshots unambiguous even when
		// an older Gateway is rolled back.
		GatewayPath: "trpc.moox.trade.TradeConsoleAdminService", GatewayServiceId: "trade_console", GatewayEnabled: true,
		Description: "Authenticated Admin TradeConsole route; ownership fencing remains strategy-only",
		ExtraConfig: tradeConsoleGatewayExtraConfig(),
	}
	if err := c.upsertDeployment(ctx, console); err != nil {
		return fmt.Errorf("trade_console_registry_failed: %w", err)
	}
	return nil
}

func (c *Client) registerServiceDeployment(ctx context.Context, nodeID, serviceName, host string) error {
	nodeID = strings.TrimSpace(nodeID)
	serviceName = strings.TrimSpace(serviceName)
	host = strings.TrimSpace(host)
	if c == nil || c.forwarder == nil || nodeID == "" || serviceName == "" {
		return fmt.Errorf("service_registry_invalid")
	}
	canonical, spec := lookupServiceDeployment(serviceName)
	get := &pb.GetServiceDeploymentRsp{}
	err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "GetServiceDeployment",
		&pb.GetServiceDeploymentReq{NodeId: nodeID, ServiceName: canonical}, get)
	if err != nil {
		return fmt.Errorf("service_registry_lookup_failed: %w", err)
	}
	if checkRetInfo(get.GetRetInfo()) == nil && get.GetDeployment() != nil {
		deployment := proto.Clone(get.GetDeployment()).(*pb.ServiceDeployment)
		deployment.Status = "active"
		// Strategy exposes separate native tRPC and browser HTTP listeners. A
		// pre-split deployment may still point at the native 11430 port; migrate
		// the managed endpoint on the next component upgrade so Admin's HTTP BFF
		// cannot keep forwarding JSON to the native listener.
		if canonical == "moox_strategy" {
			deployment.Protocol = spec.protocol
			deployment.Port = spec.port
		}
		if canonical == "moox_trade" && deployment.GetNodeId() != "control" {
			deployment.ExtraConfig = disableRemoteTradeMonitoring(deployment.GetExtraConfig())
		}
		return c.upsertDeployment(ctx, deployment)
	}
	if get.GetRetInfo().GetCode() != pb.ErrorCode_NOT_FOUND {
		return fmt.Errorf("service_registry_lookup_failed: %s", get.GetRetInfo().GetMsg())
	}

	deployment := &pb.ServiceDeployment{
		NodeId: nodeID, ServiceName: canonical, ServiceKind: spec.kind,
		Protocol: spec.protocol, Host: registrationHost(nodeID, host, spec.public),
		Port: spec.port, Scope: spec.scope, Status: "active",
		Description: spec.description,
		ExtraConfig: registrationExtra(nodeID, host, spec),
	}
	if spec.gatewayPath != "" {
		deployment.GatewayPath = spec.gatewayPath
	}
	return c.upsertDeployment(ctx, deployment)
}

// DisableServiceDeployment marks a managed service unavailable after a
// post-activation control-plane check fails. This prevents Monitor from
// reporting a stopped process as healthy while the operator fixes the route.
func (c *Client) DisableServiceDeployment(ctx context.Context, nodeID, serviceName string) error {
	err := c.disableServiceDeployment(ctx, nodeID, serviceName)
	canonical, _ := lookupServiceDeployment(serviceName)
	if canonical == "moox_trade" {
		err = errors.Join(err, c.disableServiceDeployment(ctx, nodeID, "trade_owner"))
		err = errors.Join(err, c.disableServiceDeployment(ctx, nodeID, "trade_console"))
	}
	return err
}

func tradeConsoleGatewayExtraConfig() string {
	callers, _ := json.Marshal([]string{"admin-gateway"})
	methods, _ := json.Marshal(tradeConsoleGatewayMethods)
	return fmt.Sprintf(`{"gateway_methods":%s,"gateway_callers":%s,"monitor_enabled":false,"managed_by":"moox-cli"}`, methods, callers)
}

func (c *Client) disableServiceDeployment(ctx context.Context, nodeID, serviceName string) error {
	nodeID = strings.TrimSpace(nodeID)
	serviceName = strings.TrimSpace(serviceName)
	if c == nil || c.forwarder == nil || nodeID == "" || serviceName == "" {
		return fmt.Errorf("service_registry_invalid")
	}
	canonical, _ := lookupServiceDeployment(serviceName)
	get := &pb.GetServiceDeploymentRsp{}
	if err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "GetServiceDeployment",
		&pb.GetServiceDeploymentReq{NodeId: nodeID, ServiceName: canonical}, get); err != nil {
		return fmt.Errorf("service_registry_lookup_failed: %w", err)
	}
	if get.GetRetInfo().GetCode() == pb.ErrorCode_NOT_FOUND {
		return nil
	}
	if err := checkRetInfo(get.GetRetInfo()); err != nil || get.GetDeployment() == nil {
		return fmt.Errorf("service_registry_lookup_failed")
	}
	deployment := proto.Clone(get.GetDeployment()).(*pb.ServiceDeployment)
	deployment.Status = "disabled"
	if err := c.upsertDeployment(ctx, deployment); err != nil {
		return fmt.Errorf("service_registry_update_failed: %w", err)
	}
	return nil
}

type serviceDeploymentCatalogEntry struct {
	canonical, kind, protocol, scope, description, gatewayPath string
	port, healthPort                                           int32
	public                                                     bool
}

func lookupServiceDeployment(name string) (string, serviceDeploymentCatalogEntry) {
	name = strings.ToLower(strings.TrimSpace(name))
	known := map[string]serviceDeploymentCatalogEntry{
		"admin":          {canonical: "admin_gateway", kind: "gateway", protocol: "https", scope: "public", port: 9527, healthPort: 11010, public: true, description: "MooX 管理台入口"},
		"admin_gateway":  {canonical: "admin_gateway", kind: "gateway", protocol: "https", scope: "public", port: 9527, healthPort: 11010, public: true, description: "MooX 管理台入口"},
		"web-host":       {canonical: "web_host", kind: "frontend", protocol: "https", scope: "public", port: 9527, healthPort: 19527, public: true, description: "MooX 管理台 Web 入口"},
		"web_host":       {canonical: "web_host", kind: "frontend", protocol: "https", scope: "public", port: 9527, healthPort: 19527, public: true, description: "MooX 管理台 Web 入口"},
		"eventbus":       {canonical: "eventbus", kind: "eventbus", protocol: "http", scope: "internal", port: 11420, healthPort: 11419, description: "MooX EventBus"},
		"monitor":        {canonical: "moox_monitor", kind: "monitor", protocol: "http", scope: "internal", port: 11410, healthPort: 11409, description: "MooX Monitor"},
		"moox_monitor":   {canonical: "moox_monitor", kind: "monitor", protocol: "http", scope: "internal", port: 11410, healthPort: 11409, description: "MooX Monitor"},
		"gateway":        {canonical: "moox_gateway", kind: "gateway", protocol: "http", scope: "internal", port: 11002, healthPort: 11012, description: "MooX node gateway"},
		"moox_gateway":   {canonical: "moox_gateway", kind: "gateway", protocol: "http", scope: "internal", port: 11002, healthPort: 11012, description: "MooX node gateway"},
		"collector":      {canonical: "moox_collector", kind: "collector", protocol: "http", scope: "internal", port: 11402, healthPort: 11412, description: "MooX Collector"},
		"moox_collector": {canonical: "moox_collector", kind: "collector", protocol: "http", scope: "internal", port: 11402, healthPort: 11412, description: "MooX Collector"},
		"cloudnode":      {canonical: "moox_cloudnode", kind: "cloudnode", protocol: "http", scope: "internal", port: 11401, healthPort: 11411, description: "MooX CloudNode"},
		"moox_cloudnode": {canonical: "moox_cloudnode", kind: "cloudnode", protocol: "http", scope: "internal", port: 11401, healthPort: 11411, description: "MooX CloudNode"},
		"factor":         {canonical: "moox_factor", kind: "factor", protocol: "http", scope: "internal", port: 11404, healthPort: 11414, description: "MooX Factor"},
		"moox_factor":    {canonical: "moox_factor", kind: "factor", protocol: "http", scope: "internal", port: 11404, healthPort: 11414, description: "MooX Factor"},
		"strategy":       {canonical: "moox_strategy", kind: "strategy", protocol: "http", scope: "internal", port: 11433, healthPort: 11431, description: "MooX Strategy"},
		"moox_strategy":  {canonical: "moox_strategy", kind: "strategy", protocol: "http", scope: "internal", port: 11433, healthPort: 11431, description: "MooX Strategy"},
		"archive":        {canonical: "moox_archive", kind: "archive", protocol: "http", scope: "internal", port: 11416, healthPort: 11416, description: "MooX Archive"},
		"moox_archive":   {canonical: "moox_archive", kind: "archive", protocol: "http", scope: "internal", port: 11416, healthPort: 11416, description: "MooX Archive"},
		"hostagent":      {canonical: "moox_hostagent", kind: "hostagent", protocol: "http", scope: "internal", port: 11426, healthPort: 11425, description: "MooX HostAgent"},
		"moox_hostagent": {canonical: "moox_hostagent", kind: "hostagent", protocol: "http", scope: "internal", port: 11426, healthPort: 11425, description: "MooX HostAgent"},
		// Trade health is loopback-only on dedicated execution nodes. Remote
		// registrations are monitored through the deployment/SSH acceptance
		// path rather than advertising an unreachable public /readyz URL.
		"trade":           {canonical: "moox_trade", kind: "trade", protocol: "http", scope: "internal", port: 11210, healthPort: 0, description: "MooX Trade"},
		"moox_trade":      {canonical: "moox_trade", kind: "trade", protocol: "http", scope: "internal", port: 11210, healthPort: 0, description: "MooX Trade"},
		"storage-primary": {canonical: "storage-primary", kind: "storage", protocol: "http", scope: "public", port: 20200, healthPort: 20210, description: "MooX Storage Primary"},
		"storage_primary": {canonical: "storage-primary", kind: "storage", protocol: "http", scope: "public", port: 20200, healthPort: 20210, description: "MooX Storage Primary"},
		"storage-view":    {canonical: "storage-view", kind: "storage", protocol: "http", scope: "public", port: 20202, healthPort: 20211, description: "MooX Storage View"},
		"storage_view":    {canonical: "storage-view", kind: "storage", protocol: "http", scope: "public", port: 20202, healthPort: 20211, description: "MooX Storage View"},
	}
	if spec, ok := known[name]; ok {
		return spec.canonical, spec
	}
	return strings.TrimSpace(name), serviceDeploymentCatalogEntry{canonical: strings.TrimSpace(name), kind: "service", protocol: "http", scope: "internal", description: "由 moox-cli 部署的服务"}
}

func registrationHost(nodeID, host string, public bool) string {
	if public && host != "" {
		return host
	}
	if nodeID == "control" || host == "" {
		return "127.0.0.1"
	}
	return host
}

func registrationExtra(nodeID, host string, spec serviceDeploymentCatalogEntry) string {
	if spec.healthPort <= 0 {
		if spec.kind == "trade" && nodeID == "control" {
			return `{"health_url":"http://127.0.0.1:11210/readyz","health_kind":"readiness","monitor_enabled":true,"managed_by":"moox-cli"}`
		}
		if spec.kind == "trade" && nodeID != "control" {
			return `{"monitor_enabled":false,"managed_by":"moox-cli"}`
		}
		return `{"managed_by":"moox-cli"}`
	}
	healthHost := registrationHost(nodeID, host, spec.public)
	extra := map[string]any{
		"health_url":      "http://" + net.JoinHostPort(healthHost, fmt.Sprintf("%d", spec.healthPort)) + "/readyz",
		"health_kind":     "readiness",
		"monitor_enabled": true,
		"managed_by":      "moox-cli",
	}
	raw, _ := json.Marshal(extra)
	return string(raw)
}

func disableRemoteTradeMonitoring(raw string) string {
	var extra map[string]any
	if json.Unmarshal([]byte(raw), &extra) != nil || extra == nil {
		extra = make(map[string]any)
	}
	extra["monitor_enabled"] = false
	extra["managed_by"] = "moox-cli"
	encoded, err := json.Marshal(extra)
	if err != nil {
		return `{"monitor_enabled":false,"managed_by":"moox-cli"}`
	}
	return string(encoded)
}
