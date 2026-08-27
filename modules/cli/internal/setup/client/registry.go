package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"google.golang.org/protobuf/proto"
)

// RegisterServiceDeployment records a service after setup deploy-service has
// activated it. Existing rows are updated in place so operator-owned gateway
// and monitoring settings are preserved; a missing row is created from the
// small catalog below. This keeps the Admin deployment store as the source of
// truth consumed by Monitor and the service gateway.
func (c *Client) RegisterServiceDeployment(ctx context.Context, nodeID, serviceName, host string) error {
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
		deployment := get.GetDeployment()
		deployment.Status = "active"
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
		"admin":           {canonical: "admin_gateway", kind: "gateway", protocol: "https", scope: "public", port: 9527, healthPort: 11010, public: true, description: "MooX 管理台入口"},
		"admin_gateway":   {canonical: "admin_gateway", kind: "gateway", protocol: "https", scope: "public", port: 9527, healthPort: 11010, public: true, description: "MooX 管理台入口"},
		"web-host":        {canonical: "web_host", kind: "frontend", protocol: "https", scope: "public", port: 9527, healthPort: 19527, public: true, description: "MooX 管理台 Web 入口"},
		"web_host":        {canonical: "web_host", kind: "frontend", protocol: "https", scope: "public", port: 9527, healthPort: 19527, public: true, description: "MooX 管理台 Web 入口"},
		"eventbus":        {canonical: "eventbus", kind: "eventbus", protocol: "http", scope: "internal", port: 11420, healthPort: 11419, description: "MooX EventBus"},
		"monitor":         {canonical: "moox_monitor", kind: "monitor", protocol: "http", scope: "internal", port: 11410, healthPort: 11409, description: "MooX Monitor"},
		"moox_monitor":    {canonical: "moox_monitor", kind: "monitor", protocol: "http", scope: "internal", port: 11410, healthPort: 11409, description: "MooX Monitor"},
		"gateway":         {canonical: "moox_gateway", kind: "gateway", protocol: "http", scope: "internal", port: 11002, healthPort: 11012, description: "MooX node gateway"},
		"moox_gateway":    {canonical: "moox_gateway", kind: "gateway", protocol: "http", scope: "internal", port: 11002, healthPort: 11012, description: "MooX node gateway"},
		"collector":       {canonical: "moox_collector", kind: "collector", protocol: "http", scope: "internal", port: 11402, healthPort: 11412, description: "MooX Collector"},
		"moox_collector":  {canonical: "moox_collector", kind: "collector", protocol: "http", scope: "internal", port: 11402, healthPort: 11412, description: "MooX Collector"},
		"cloudnode":       {canonical: "moox_cloudnode", kind: "cloudnode", protocol: "http", scope: "internal", port: 11401, healthPort: 11411, description: "MooX CloudNode"},
		"moox_cloudnode":  {canonical: "moox_cloudnode", kind: "cloudnode", protocol: "http", scope: "internal", port: 11401, healthPort: 11411, description: "MooX CloudNode"},
		"factor":          {canonical: "moox_factor", kind: "factor", protocol: "http", scope: "internal", port: 11404, healthPort: 11414, description: "MooX Factor"},
		"moox_factor":     {canonical: "moox_factor", kind: "factor", protocol: "http", scope: "internal", port: 11404, healthPort: 11414, description: "MooX Factor"},
		"strategy":        {canonical: "moox_strategy", kind: "strategy", protocol: "http", scope: "internal", port: 11430, healthPort: 11431, description: "MooX Strategy"},
		"moox_strategy":   {canonical: "moox_strategy", kind: "strategy", protocol: "http", scope: "internal", port: 11430, healthPort: 11431, description: "MooX Strategy"},
		"archive":         {canonical: "moox_archive", kind: "archive", protocol: "http", scope: "internal", port: 11416, healthPort: 11416, description: "MooX Archive"},
		"moox_archive":    {canonical: "moox_archive", kind: "archive", protocol: "http", scope: "internal", port: 11416, healthPort: 11416, description: "MooX Archive"},
		"hostagent":       {canonical: "moox_hostagent", kind: "hostagent", protocol: "http", scope: "internal", port: 11426, healthPort: 11425, description: "MooX HostAgent"},
		"moox_hostagent":  {canonical: "moox_hostagent", kind: "hostagent", protocol: "http", scope: "internal", port: 11426, healthPort: 11425, description: "MooX HostAgent"},
		"trade":           {canonical: "moox_trade", kind: "trade", protocol: "http", scope: "internal", port: 11210, healthPort: 11210, description: "MooX Trade"},
		"moox_trade":      {canonical: "moox_trade", kind: "trade", protocol: "http", scope: "internal", port: 11210, healthPort: 11210, description: "MooX Trade"},
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
