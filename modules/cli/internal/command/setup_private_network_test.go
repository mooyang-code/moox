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
			require.True(t, opts.SkipSCF)
			require.True(t, opts.SkipHosts)
			require.False(t, opts.RestoreSCFPublic)
			require.Equal(t, snapshot, got)
			return privatenet.Result{DryRun: true, Status: "dry_run", RecommendedConfig: privatenet.RecommendedConfig{StoragePublicIP: "203.0.113.9"}}, nil
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
	require.Equal(t, "203.0.113.9", result.RecommendedConfig.StoragePublicIP)
	require.NotContains(t, stdout.String(), "admin-test-password")
	require.NotContains(t, stdout.String(), "AKID-test-secret")
}

func TestSetupPrivateNetworkRestorePublicFlag(t *testing.T) {
	snapshot := setupSnapshot(t)
	snapshot.Manifest.ControlHost.Provider = "tencent"
	snapshot.Manifest.StorageHost = setupconfig.Host{Name: "storage", Address: "203.0.113.9", Provider: "tencent"}
	cmd := newSetupCommand(setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		ensurePrivateNetwork: func(_ context.Context, _ *setupconfig.Snapshot, opts privatenet.Options, _ io.Writer) (privatenet.Result, error) {
			require.True(t, opts.RestoreSCFPublic)
			require.True(t, opts.UnbindSCFVPC)
			require.False(t, opts.SkipSCF)
			require.True(t, opts.SkipHosts)
			require.True(t, opts.SkipProbe)
			require.True(t, opts.DryRun)
			return privatenet.Result{DryRun: true, Status: "dry_run"}, nil
		},
	})
	cmd.SetOut(io.Discard)
	cmd.SetArgs([]string{"private-network", "--file", "moox.toml", "--restore-scf-public", "--dry-run"})
	require.NoError(t, cmd.Execute())
}

func TestSetupPrivateNetworkDeprecatedUpdateGatewayRestoresPublic(t *testing.T) {
	snapshot := setupSnapshot(t)
	snapshot.Manifest.ControlHost.Provider = "tencent"
	snapshot.Manifest.StorageHost = setupconfig.Host{Name: "storage", Address: "203.0.113.9", Provider: "tencent"}
	cmd := newSetupCommand(setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		ensurePrivateNetwork: func(_ context.Context, _ *setupconfig.Snapshot, opts privatenet.Options, _ io.Writer) (privatenet.Result, error) {
			require.True(t, opts.RestoreSCFPublic)
			require.True(t, opts.UnbindSCFVPC)
			require.False(t, opts.SkipSCF)
			return privatenet.Result{DryRun: true, Status: "dry_run"}, nil
		},
	})
	cmd.SetOut(io.Discard)
	cmd.SetArgs([]string{"private-network", "--file", "moox.toml", "--update-scf-gateway", "--dry-run"})
	require.NoError(t, cmd.Execute())
}

func TestPrivateNetworkRewriteRunsWhenProbesSkipped(t *testing.T) {
	opts := privatenet.Options{SkipProbe: true, RewriteRuntime: true}
	require.False(t, shouldRunPrivateNetworkProbes(opts))
	require.True(t, shouldRewritePublicStorageRPC(opts))
	require.False(t, shouldRewritePublicStorageRPC(privatenet.Options{DryRun: true, RewriteRuntime: true}))
}

func TestAppendMissingFactorHostsFromSnapshot(t *testing.T) {
	snapshot := setupSnapshot(t)
	snapshot.Manifest.OtherHosts = append(snapshot.Manifest.OtherHosts, setupconfig.Host{
		Name: "factor-1", Address: "192.168.0.102", Provider: "lan",
	})
	hosts := appendMissingFactorHosts(nil, snapshot)
	require.Len(t, hosts, 1)
	require.Equal(t, "factor-1", hosts[0].Name)
	require.Equal(t, "192.168.0.102", hosts[0].Address)
}

func TestRewriteStorageRPCUsesPublicGateway(t *testing.T) {
	result := privatenet.Result{
		RecommendedConfig: privatenet.RecommendedConfig{
			StoragePublicIP:  "146.56.196.204",
			StoragePrivateIP: "10.206.0.5",
		},
		Plan: privatenet.Plan{Hosts: []privatenet.ResolvedHost{
			{HostTarget: privatenet.HostTarget{Name: "control", Address: "106.53.107.122", Roles: []string{"control"}}},
			{HostTarget: privatenet.HostTarget{Name: "factor-1", Address: "192.168.0.102", Roles: []string{"factor-1"}}},
		}},
	}
	seen := map[string]string{}
	exec := func(_ context.Context, publicIP, script string) (string, error) {
		seen[publicIP] = script
		require.Contains(t, script, "10.206.0.5 146.56.196.204")
		require.NotContains(t, script, "146.56.196.204 10.206.0.5")
		if publicIP == "106.53.107.122" {
			require.Contains(t, script, "/data/moox/prod/config/runtime.env")
			require.Contains(t, script, "./start.sh collector")
			return "rewrote ip://10.206.0.5:11003 -> ip://146.56.196.204:11003\nMOOX_COLLECTOR_STORAGE_RPC_GATEWAY_TARGET=ip://146.56.196.204:11003\n", nil
		}
		require.Equal(t, "192.168.0.102", publicIP)
		require.Contains(t, script, ".config/moox/factor-engine/runtime.env")
		require.Contains(t, script, "factor-engine")
		return "rewrote ip://10.206.0.5:11003 -> ip://146.56.196.204:11003\nMOOX_FACTOR_STORAGE_RPC_GATEWAY_TARGET=ip://146.56.196.204:11003\n", nil
	}
	require.NoError(t, rewriteMainlandStorageRPC(t.Context(), exec, result, io.Discard))
	require.Contains(t, seen, "106.53.107.122")
	require.Contains(t, seen, "192.168.0.102")
}
