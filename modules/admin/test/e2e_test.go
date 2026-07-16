package test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	adminconfig "github.com/mooyang-code/moox/modules/admin/internal/config"
	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	adminschema "github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/mooyang-code/moox/packages/gatewayproxy"
)

func TestAdminGatewayControlPlaneContract(t *testing.T) {
	ctx := context.Background()
	manager := database.NewManager()
	if err := manager.Initialize(&adminconfig.DatabaseConfig{Path: filepath.Join(t.TempDir(), "admin.db")}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.GetDB().Exec(adminschema.AdminSQL()).Error; err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	const nodeID = "gateway-e2e"
	node := &sysdeploy.GatewayNode{NodeID: nodeID, Name: "E2E node", PublicAddress: "https://127.0.0.1", Status: "enabled"}
	if err := manager.GetDB().Create(node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	deployment := &sysdeploy.Deployment{
		NodeID: nodeID, ServiceName: "monitor", ServiceKind: "trpc", Protocol: "http",
		Host: "127.0.0.1", Port: 11410, GatewayPath: "trpc.moox.monitor.MonitorMgr",
		GatewayServiceID: "monitor", GatewayEnabled: true, Status: "active", ExtraConfig: `{"timeout_ms":7500}`,
	}
	if err := manager.GetDB().Create(deployment).Error; err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	service := sysdeploy.NewService(manager, nodeID)
	snapshot, err := service.CompileGatewaySnapshot(ctx, nodeID)
	if err != nil {
		t.Fatalf("CompileGatewaySnapshot: %v", err)
	}
	if snapshot.NodeID != nodeID || snapshot.Disabled || len(snapshot.Routes) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	route := snapshot.Routes[0]
	if route.ServiceID != "monitor" || route.Address != "127.0.0.1:11410" || route.TimeoutMS != 7500 {
		t.Fatalf("compiled route = %+v", route)
	}

	seenAt := time.Now().UTC().Truncate(time.Microsecond)
	report := gatewayproxy.GatewayStatusReport{
		NodeID: nodeID, AppliedRouteHash: snapshot.RouteHash, RouteCount: int32(len(snapshot.Routes)), LastSeenAt: seenAt,
	}
	if err := service.ReportGatewayStatus(ctx, report); err != nil {
		t.Fatalf("ReportGatewayStatus: %v", err)
	}
	var stored sysdeploy.GatewayNode
	if err := manager.GetDB().Where("c_node_id = ?", nodeID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.RouteHash != snapshot.RouteHash || stored.AppliedRouteHash != snapshot.RouteHash || stored.RouteCount != 1 || stored.LastSeenAt == nil {
		t.Fatalf("stored gateway status = %+v", stored)
	}

	if err := manager.GetDB().Model(&sysdeploy.GatewayNode{}).Where("c_node_id = ?", nodeID).Update("c_status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	disabled, err := service.CompileGatewaySnapshot(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if !disabled.Disabled || disabled.RouteHash == snapshot.RouteHash {
		t.Fatalf("disabled snapshot = %+v, enabled hash = %s", disabled, snapshot.RouteHash)
	}
}
