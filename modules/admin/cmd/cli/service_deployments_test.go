package main

import (
	"bytes"
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
	require.Len(t, seed.Services, 30)
	processes := 0
	for _, service := range seed.Services {
		if service.DeploymentMode == "process" {
			processes++
		}
	}
	require.Equal(t, 13, processes)
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
	require.Equal(t, 30, result.Updated)

	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	var count int64
	require.NoError(t, db.Table("t_service_deployments").Count(&count).Error)
	require.Equal(t, int64(30), count)
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
}
