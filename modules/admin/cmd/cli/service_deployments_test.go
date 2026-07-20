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
	seed, err := loadServiceDeploymentSeed(filepath.Join("..", "..", "..", "..", "examples", "service-deployments.seed.yaml"))
	require.NoError(t, err)
	require.Equal(t, 1, seed.Version)
	require.Equal(t, "control", seed.Node.ID)
	require.Len(t, seed.Services, 31)
	processes := 0
	for _, service := range seed.Services {
		if service.DeploymentMode == "process" {
			processes++
		}
	}
	require.Equal(t, 14, processes)
}

func TestLoadServiceDeploymentSeed_MatchesDefaultDeploymentContract(t *testing.T) {
	seed, err := loadServiceDeploymentSeed(filepath.Join("..", "..", "..", "..", "examples", "service-deployments.seed.yaml"))
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
	seedPath := filepath.Join("..", "..", "..", "..", "examples", "service-deployments.seed.yaml")
	var first, second bytes.Buffer
	require.NoError(t, runServiceDeploymentsCommand([]string{"service-deployments", "import", "--db-path", dbPath, "--file", seedPath}, &first, &bytes.Buffer{}))
	require.NoError(t, runServiceDeploymentsCommand([]string{"service-deployments", "import", "--db-path", dbPath, "--file", seedPath}, &second, &bytes.Buffer{}))
	var result struct {
		Created int `json:"created"`
		Updated int `json:"updated"`
	}
	require.NoError(t, json.Unmarshal(second.Bytes(), &result))
	require.Equal(t, 0, result.Created)
	require.Equal(t, 31, result.Updated)

	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	var count int64
	require.NoError(t, db.Table("t_service_deployments").Count(&count).Error)
	require.Equal(t, int64(31), count)
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
	seed, err := loadServiceDeploymentSeed(filepath.Join("..", "..", "..", "..", "examples", "service-deployments.seed.yaml"))
	require.NoError(t, err)
	require.NoError(t, enableOptionalStorageShard(&seed))
	require.Len(t, seed.Services, 32)

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

func TestDisableOptionalStorageShardAddsInactiveOverride(t *testing.T) {
	seed, err := loadServiceDeploymentSeed(filepath.Join("..", "..", "..", "..", "examples", "service-deployments.seed.yaml"))
	require.NoError(t, err)
	require.NoError(t, disableOptionalStorageShard(&seed))
	require.Len(t, seed.Services, 32)
	shard := seed.Services[len(seed.Services)-1]
	require.Equal(t, "storage-shard", shard.Name)
	require.False(t, shard.GatewayEnabled)
	require.Equal(t, "disabled", shard.Status)
}

func TestImportWithOptionalStorageShardCompilesOnlyIndependentDataShardRoute(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "admin.db")
	seedPath := filepath.Join("..", "..", "..", "..", "examples", "service-deployments.seed.yaml")
	var output bytes.Buffer
	require.NoError(t, runServiceDeploymentsCommand([]string{
		"service-deployments", "import", "--db-path", dbPath, "--file", seedPath, "--node-id", "gateway-node-1", "--with-storage-shard",
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
