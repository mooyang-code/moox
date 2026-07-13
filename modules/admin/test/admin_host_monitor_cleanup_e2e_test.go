package test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	adminconfig "github.com/mooyang-code/moox/modules/admin/internal/config"
	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	adminschema "github.com/mooyang-code/moox/modules/admin/schema"
)

func TestAdminHasNoLegacyHostMonitorSurface(t *testing.T) {
	root := repoRoot(t)
	assertNotContains(t, filepath.Join(root, "modules/admin/schema/admin.sql"), "t_host_monitor_history")
	assertNotContains(t, filepath.Join(root, "modules/admin/config/trpc_go.yaml"), "trpc.moox.ops.Monitor")
	assertNotContains(t, filepath.Join(root, "modules/admin/proto/ops_service.proto"), "service Monitor")
	assertNotContains(t, filepath.Join(root, "modules/admin/config/app.yaml"), "node_exporter_port")
	assertNotContains(t, filepath.Join(root, "modules/admin/internal/service/sysdeploy/defaults.go"), `deployment("monitor"`)
	assertContains(t, filepath.Join(root, "modules/admin/internal/service/sysdeploy/service.go"), `case "monitor":`)

	if _, err := os.Stat(filepath.Join(root, "modules/admin/internal/service/monitor")); !os.IsNotExist(err) {
		t.Fatalf("legacy monitor package still exists: %v", err)
	}
	if strings.Contains(adminschema.AdminSQL(), "t_host_monitor_history") {
		t.Fatal("fresh Admin schema still contains legacy host history")
	}
	if adminconfig.DefaultConfig().Database.Path == "" {
		t.Fatal("Admin default configuration is incomplete")
	}
	for _, deployment := range sysdeploy.DefaultDeployments() {
		if deployment.ServiceName == "monitor" || deployment.GatewayPath == "trpc.moox.ops.Monitor" {
			t.Fatalf("legacy monitor deployment still exposed: %+v", deployment)
		}
	}
}

func TestAdminUpgradeRetiresLegacyMonitorAndRoutesAlias(t *testing.T) {
	manager := database.NewManager()
	if err := manager.Initialize(&adminconfig.DatabaseConfig{Path: filepath.Join(t.TempDir(), "admin.db")}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	db := manager.GetDB()
	if err := db.Exec(adminschema.AdminSQL()).Error; err != nil {
		t.Fatalf("apply admin schema: %v", err)
	}
	var legacyTableCount int64
	if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='t_host_monitor_history'`).Scan(&legacyTableCount).Error; err != nil {
		t.Fatal(err)
	}
	if legacyTableCount != 0 {
		t.Fatal("fresh Admin database created legacy host history")
	}
	legacy := sysdeploy.Deployment{
		ServiceName: "monitor", ServiceKind: "admin_rpc", Protocol: "http", Host: "127.0.0.1", Port: 11103,
		GatewayPath: "trpc.moox.ops.Monitor", Scope: "internal", Status: "active", ExtraConfig: "{}",
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("seed legacy deployment: %v", err)
	}
	service := sysdeploy.NewService(manager)
	if err := service.SeedDefaults(context.Background()); err != nil {
		t.Fatalf("SeedDefaults: %v", err)
	}
	var activeLegacy int64
	if err := db.Model(&sysdeploy.Deployment{}).Where("c_service_name = ?", "monitor").Count(&activeLegacy).Error; err != nil {
		t.Fatal(err)
	}
	if activeLegacy != 0 {
		t.Fatal("legacy Admin monitor deployment remains active")
	}
	detail, ok := service.ResolveGatewayServiceDetail(context.Background(), "monitor")
	if !ok || detail.Path != "trpc.moox.monitor.MonitorMgr" || detail.Address != "127.0.0.1:11410" {
		t.Fatalf("monitor gateway alias = %+v, ok=%v", detail, ok)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func assertNotContains(t *testing.T, path, needle string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if strings.Contains(string(raw), needle) {
		t.Fatalf("%s still contains %q", path, needle)
	}
}

func assertContains(t *testing.T, path, needle string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(raw), needle) {
		t.Fatalf("%s does not contain %q", path, needle)
	}
}
