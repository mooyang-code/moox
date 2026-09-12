package command

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/privatenet"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/stretchr/testify/require"
)

func TestSetupPrivateNetworkDryRunJSON(t *testing.T) {
	snapshot := setupSnapshot(t)
	snapshot.Manifest.ControlHost.Provider = "tencent"
	snapshot.Manifest.StorageHost = setupconfig.Host{Name: "storage", Address: "203.0.113.9", Provider: "tencent"}
	called := false
	cmd := newSetupCommand(setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		ensurePrivateNetwork: func(_ context.Context, got *setupconfig.Snapshot, opts privatenet.Options, _ io.Writer) (privatenet.Result, error) {
			called = true
			require.True(t, opts.DryRun)
			require.Equal(t, snapshot, got)
			return privatenet.Result{DryRun: true, Status: "dry_run", RecommendedConfig: privatenet.RecommendedConfig{StoragePrivateIP: "10.1.2.8"}}, nil
		},
	})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"private-network", "--file", "moox.toml", "--dry-run"})
	require.NoError(t, cmd.Execute())
	require.True(t, called)
	var result privatenet.Result
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
	require.Equal(t, "dry_run", result.Status)
	require.Equal(t, "10.1.2.8", result.RecommendedConfig.StoragePrivateIP)
	require.NotContains(t, stdout.String(), "admin-test-password")
	require.NotContains(t, stdout.String(), "AKID-test-secret")
}
