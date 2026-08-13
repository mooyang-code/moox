package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLoadServiceDeploymentSeed_Example(t *testing.T) {
	seed, err := loadServiceDeploymentSeed(filepath.Join("..", "..", "..", "..", "examples", "setup", "default", "service-deployments.yaml"))
	require.NoError(t, err)
	require.Equal(t, 1, seed.Version)
	require.Equal(t, "control", seed.Node.ID)
	require.Len(t, seed.Services, 26)
	processes := 0
	for _, service := range seed.Services {
		if service.DeploymentMode == "process" {
			processes++
		}
	}
	require.Equal(t, 14, processes)
}

func TestLoadServiceDeploymentSeed_MatchesDefaultDeploymentContract(t *testing.T) {
	seed, err := loadServiceDeploymentSeed(filepath.Join("..", "..", "..", "..", "examples", "setup", "default", "service-deployments.yaml"))
	require.NoError(t, err)
	configured := make(map[string]serviceDeploymentEntry, len(seed.Services))
	for _, item := range seed.Services {
		configured[item.Name] = item
	}
	defaults := sysdeploy.DefaultDeployments(seed.Node.ID)
	require.Len(t, configured, len(defaults))
	for _, want := range defaults {
		got, ok := configured[want.ServiceName]
		require.Truef(t, ok, "missing default deployment %q", want.ServiceName)
		require.Equal(t, want.ServiceKind, got.Kind, want.ServiceName)
		require.Equal(t, want.Protocol, got.Protocol, want.ServiceName)
		require.Equal(t, want.Port, got.Port, want.ServiceName)
		require.Equal(t, want.GatewayPath, got.GatewayPath, want.ServiceName)
		require.Equal(t, want.GatewayServiceID, got.GatewayService, want.ServiceName)
		require.Equal(t, want.GatewayEnabled, got.GatewayEnabled, want.ServiceName)
		require.Equal(t, want.Scope, got.Scope, want.ServiceName)
		require.Equal(t, want.Status, got.Status, want.ServiceName)
		require.Equal(t, want.Description, got.Description, want.ServiceName)
		var wantExtra map[string]any
		require.NoError(t, json.Unmarshal([]byte(want.ExtraConfig), &wantExtra))
		gotExtraJSON, err := json.Marshal(got.ExtraConfig)
		require.NoError(t, err)
		var normalizedGotExtra map[string]any
		require.NoError(t, json.Unmarshal(gotExtraJSON, &normalizedGotExtra))
		require.Equal(t, wantExtra, normalizedGotExtra, want.ServiceName)
	}
}

func TestRunServiceDeploymentsCommand_IsIdempotent(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "admin.db")
	seedPath := filepath.Join("..", "..", "..", "..", "examples", "setup", "default", "service-deployments.yaml")
	var first, second bytes.Buffer
	require.NoError(t, runServiceDeploymentsCommand([]string{"service-deployments", "import", "--db-path", dbPath, "--file", seedPath, "--public-host", "127.0.0.1", "--eventbus-nats-url", "tls://127.0.0.1:4222"}, &first, &bytes.Buffer{}))
	require.NoError(t, runServiceDeploymentsCommand([]string{"service-deployments", "import", "--db-path", dbPath, "--file", seedPath, "--public-host", "127.0.0.1", "--eventbus-nats-url", "tls://127.0.0.1:4222"}, &second, &bytes.Buffer{}))
	var result struct {
		Created int `json:"created"`
		Updated int `json:"updated"`
	}
	require.NoError(t, json.Unmarshal(second.Bytes(), &result))
	require.Equal(t, 0, result.Created)
	require.Equal(t, 26, result.Updated)

	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	var count int64
	require.NoError(t, db.Table("t_service_deployments").Count(&count).Error)
	require.Equal(t, int64(26), count)
}

func TestRunServiceDeploymentsCommand_AllowsScopedResolverImport(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "admin.db")
	seedPath := filepath.Join("..", "..", "..", "..", "examples", "setup", "default", "service-deployments.yaml")
	var output bytes.Buffer
	require.NoError(t, runServiceDeploymentsCommand([]string{
		"service-deployments", "import", "--db-path", dbPath, "--file", seedPath,
		"--node-id", "compute-1", "--public-host", "43.132.204.177",
		"--eventbus-nats-url", "tls://127.0.0.1:4222", "--only-services", "trade_dns_resolver",
	}, &output, &bytes.Buffer{}))
	var result struct {
		Services int `json:"services"`
	}
	require.NoError(t, json.Unmarshal(output.Bytes(), &result))
	require.Equal(t, 1, result.Services)
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	var rows []sysdeploy.Deployment
	require.NoError(t, db.Where("c_node_id = ?", "compute-1").Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, "trade_dns_resolver", rows[0].ServiceName)
	require.True(t, rows[0].GatewayEnabled)
	var node sysdeploy.GatewayNode
	require.NoError(t, db.Where("c_node_id = ?", "compute-1").First(&node).Error)
	require.Equal(t, "compute-1", node.Name)
	require.Equal(t, "enabled", node.Status)
}

func TestScopedResolverImportPreservesExistingNodeMetadata(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "admin.db")
	require.NoError(t, ensureAdminSchema(dbPath))
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Create(&sysdeploy.GatewayNode{NodeID: "compute-1", Name: "operator-name", PublicAddress: "old.example", Status: "disabled"}).Error)
	var node sysdeploy.GatewayNode
	seedPath := filepath.Join("..", "..", "..", "..", "examples", "setup", "default", "service-deployments.yaml")
	var output bytes.Buffer
	require.NoError(t, runServiceDeploymentsCommand([]string{
		"service-deployments", "import", "--db-path", dbPath, "--file", seedPath,
		"--node-id", "compute-1", "--public-host", "43.132.204.177",
		"--eventbus-nats-url", "tls://127.0.0.1:4222", "--only-services", "trade_dns_resolver",
	}, &output, &bytes.Buffer{}))
	output.Reset()
	require.NoError(t, runServiceDeploymentsCommand([]string{
		"service-deployments", "import", "--db-path", dbPath, "--file", seedPath,
		"--node-id", "compute-1", "--public-host", "43.132.204.177",
		"--eventbus-nats-url", "tls://127.0.0.1:4222", "--only-services", "trade_dns_resolver",
	}, &output, &bytes.Buffer{}))
	require.NoError(t, db.Where("c_node_id = ?", "compute-1").First(&node).Error)
	require.Equal(t, "operator-name", node.Name)
	require.Equal(t, "old.example", node.PublicAddress)
	require.Equal(t, "disabled", node.Status)
}

func TestSetSeedEventBusNATSURLPreservesExtraConfig(t *testing.T) {
	seed := serviceDeploymentSeed{Services: []serviceDeploymentEntry{{
		Name: "eventbus",
		ExtraConfig: map[string]any{
			"health_url":      "http://127.0.0.1:11419/readyz",
			"monitor_enabled": true,
			"nats_url":        "tls://127.0.0.1:4222",
		},
	}}}
	require.NoError(t, setSeedEventBusNATSURL(&seed, "tls://203.0.113.10:4222"))
	require.Equal(t, "tls://203.0.113.10:4222", seed.Services[0].ExtraConfig["nats_url"])
	require.Equal(t, "http://127.0.0.1:11419/readyz", seed.Services[0].ExtraConfig["health_url"])
	require.Equal(t, true, seed.Services[0].ExtraConfig["monitor_enabled"])
}

func TestSetSeedPublicHostUpdatesOnlyPublicEndpoints(t *testing.T) {
	seed := serviceDeploymentSeed{
		Node: serviceDeploymentNode{PublicAddress: "https://127.0.0.1"},
		Services: []serviceDeploymentEntry{
			{Name: "admin_gateway", Scope: "public", Host: "127.0.0.1"},
			{Name: "service_gateway", Scope: "public", Host: "127.0.0.1"},
			{Name: "service_gateway_native", Scope: "public", Host: "127.0.0.1"},
			{Name: "storage-primary", Scope: "public", Host: "127.0.0.1", GatewayEnabled: true},
			{Name: "moox_gateway", Scope: "internal", Host: "127.0.0.1"},
		},
	}
	require.NoError(t, setSeedPublicHost(&seed, "203.0.113.10"))
	require.Equal(t, "https://203.0.113.10", seed.Node.PublicAddress)
	require.Equal(t, "203.0.113.10", seed.Services[0].Host)
	require.Equal(t, "203.0.113.10", seed.Services[1].Host)
	require.Equal(t, "203.0.113.10", seed.Services[2].Host)
	require.Equal(t, "127.0.0.1", seed.Services[3].Host)
	require.Equal(t, "127.0.0.1", seed.Services[4].Host)
	require.Error(t, setSeedPublicHost(&seed, "https://203.0.113.10"))
}

func TestValidateEventBusNATSURLRejectsIncompleteOrNonTLS(t *testing.T) {
	for _, raw := range []string{"", "nats://host:4222", "tls://host", "tls://:4222", "tls://host:4222/path"} {
		_, err := validateEventBusNATSURL(raw)
		require.Error(t, err, raw)
	}
}

func TestValidateServiceDeploymentSeed_RejectsDuplicateAndInvalidGateway(t *testing.T) {
	base := serviceDeploymentSeed{
		Version: 1,
		Node:    serviceDeploymentNode{ID: "control", Name: "control", PublicAddress: "https://127.0.0.1", Status: "enabled"},
		Services: []serviceDeploymentEntry{
			{Name: "same", Kind: "test", Protocol: "http", Host: "127.0.0.1", Port: 1, Scope: "internal", Status: "active", DeploymentMode: "process"},
			{Name: "same", Kind: "test", Protocol: "http", Host: "127.0.0.1", Port: 2, Scope: "internal", Status: "active", DeploymentMode: "endpoint"},
		},
	}
	require.ErrorContains(t, validateServiceDeploymentSeed(base), "duplicate")
	base.Services = base.Services[:1]
	base.Services[0].GatewayEnabled = true
	require.ErrorContains(t, validateServiceDeploymentSeed(base), "gateway_service_id")
	base.Services[0].GatewayService = "same"
	base.Services[0].Protocol = "trpc"
	require.ErrorContains(t, validateServiceDeploymentSeed(base), "gateway-enabled protocol")
	base.Node.ID = "bad/id"
	base.Services[0].Protocol = "http"
	require.ErrorContains(t, validateServiceDeploymentSeed(base), "node id")
}

func TestEnableOptionalStorageShardReplacesEmbeddedRoute(t *testing.T) {
	seed, err := loadServiceDeploymentSeed(filepath.Join("..", "..", "..", "..", "examples", "setup", "default", "service-deployments.yaml"))
	require.NoError(t, err)
	require.NoError(t, enableOptionalStorageShard(&seed))
	require.Len(t, seed.Services, 27)

	var primary, shard serviceDeploymentEntry
	for _, item := range seed.Services {
		switch item.Name {
		case "storage-primary":
			primary = item
		case "storage-shard":
			shard = item
		}
	}
	routes, ok := primary.ExtraConfig["gateway_routes"].([]any)
	require.True(t, ok)
	for _, raw := range routes {
		route, ok := raw.(map[string]any)
		require.True(t, ok)
		require.NotEqual(t, "trpc.moox.storage.DataShard", route["service_path"])
	}
	require.Equal(t, int32(20107), shard.Port)
	require.Equal(t, "storage-shard", shard.GatewayService)
	require.Equal(t, "http", shard.Protocol)
	require.Equal(t, "trpc.moox.storage.DataShard", shard.GatewayPath)
	require.Equal(t, []any{"storage-primary"}, shard.ExtraConfig["gateway_callers"])
}

func TestServiceDeploymentSeedExposesStorageSpaceCleanup(t *testing.T) {
	seed, err := loadServiceDeploymentSeed(filepath.Join("..", "..", "..", "..", "examples", "setup", "default", "service-deployments.yaml"))
	require.NoError(t, err)
	for _, item := range seed.Services {
		if item.Name != "storage-primary" {
			continue
		}
		methods, ok := item.ExtraConfig["gateway_methods"].([]any)
		require.True(t, ok)
		require.NotContains(t, methods, "DeleteSpace")
		routes, ok := item.ExtraConfig["gateway_routes"].([]any)
		require.True(t, ok)
		for _, raw := range routes {
			route, ok := raw.(map[string]any)
			require.True(t, ok)
			if methods, _ := route["gateway_methods"].([]any); len(methods) == 1 && methods[0] == "DeleteSpace" {
				require.Equal(t, []any{"admin-gateway", "moox-cli"}, route["gateway_callers"])
				return
			}
		}
		t.Fatal("storage-primary DeleteSpace route is missing")
	}
	t.Fatal("storage-primary deployment is missing")
}

func TestServiceDeploymentSeedRestrictsFactorGatewayCallers(t *testing.T) {
	seed, err := loadServiceDeploymentSeed(filepath.Join("..", "..", "..", "..", "examples", "setup", "default", "service-deployments.yaml"))
	require.NoError(t, err)
	for _, item := range seed.Services {
		if item.Name != "moox_factor" {
			continue
		}
		require.Equal(t, []any{
			"CreateFactor", "UpdateFactor", "GetFactor", "ListFactors", "SetFactorStatus", "DeleteFactor",
			"UpsertBinding", "ListBindings", "DeleteBinding", "RecalcFactor", "GetEngineStatus",
		}, item.ExtraConfig["gateway_methods"])
		require.Equal(t, []any{"admin-gateway", "moox-cli"}, item.ExtraConfig["gateway_callers"])
		return
	}
	t.Fatal("moox_factor deployment is missing")
}

func TestDisableOptionalStorageShardAddsInactiveOverride(t *testing.T) {
	seed, err := loadServiceDeploymentSeed(filepath.Join("..", "..", "..", "..", "examples", "setup", "default", "service-deployments.yaml"))
	require.NoError(t, err)
	require.NoError(t, disableOptionalStorageShard(&seed))
	require.Len(t, seed.Services, 27)
	shard := seed.Services[len(seed.Services)-1]
	require.Equal(t, "storage-shard", shard.Name)
	require.False(t, shard.GatewayEnabled)
	require.Equal(t, "disabled", shard.Status)
}

func TestDisableSeedServicesUsesDeploymentProfile(t *testing.T) {
	seed, err := loadServiceDeploymentSeed(filepath.Join("..", "..", "..", "..", "examples", "setup", "default", "service-deployments.yaml"))
	require.NoError(t, err)
	require.NoError(t, disableSeedServices(&seed, "moox_archive, moox_factor,moox_strategy"))
	statuses := map[string]string{}
	for _, service := range seed.Services {
		statuses[service.Name] = service.Status
	}
	require.Equal(t, "disabled", statuses["moox_archive"])
	require.Equal(t, "disabled", statuses["moox_factor"])
	require.Equal(t, "disabled", statuses["moox_strategy"])
	require.Equal(t, "active", statuses["moox_monitor"])
	require.ErrorContains(t, disableSeedServices(&seed, "missing"), "unknown services")
}

func TestImportWithOptionalStorageShardCompilesOnlyIndependentDataShardRoute(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "admin.db")
	seedPath := filepath.Join("..", "..", "..", "..", "examples", "setup", "default", "service-deployments.yaml")
	var output bytes.Buffer
	require.NoError(t, runServiceDeploymentsCommand([]string{
		"service-deployments", "import", "--db-path", dbPath, "--file", seedPath, "--node-id", "gateway-node-1", "--public-host", "127.0.0.1", "--eventbus-nats-url", "tls://127.0.0.1:4222", "--with-storage-shard",
	}, &output, &bytes.Buffer{}))
	db, err := openAdminCLIDB(dbPath)
	require.NoError(t, err)
	defer closeAdminCLIDB(db)
	snapshot, err := sysdeploy.NewDAO(db).CompileGatewaySnapshot(context.Background(), "gateway-node-1")
	require.NoError(t, err)
	var found bool
	for _, route := range snapshot.Routes {
		if route.ServicePath != "trpc.moox.storage.DataShard" {
			continue
		}
		require.Equal(t, "storage-shard", route.ServiceID)
		require.Equal(t, "127.0.0.1:20107", route.Address)
		found = true
	}
	require.True(t, found)
}
