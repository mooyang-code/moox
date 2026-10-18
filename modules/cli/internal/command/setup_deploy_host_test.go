package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitdeploy"
	"github.com/stretchr/testify/require"
)

func TestSetupDeployHostUsesOneSnapshotAndPreservesPausedResult(t *testing.T) {
	snapshot := setupSnapshot(t)
	loaded, called := 0, 0
	var watchdogHosts []string
	command := newSetupCommand(setupDeps{load: func(string) (*setupconfig.Snapshot, error) { loaded++; return snapshot, nil }, installWatchdog: func(_ context.Context, _ *setupconfig.Snapshot, host setupconfig.Host, remove bool) (json.RawMessage, error) {
		require.False(t, remove)
		watchdogHosts = append(watchdogHosts, host.Name)
		return json.RawMessage(`{"seed":"written"}`), nil
	}, deployHost: func(_ context.Context, got *setupconfig.Snapshot, options unitdeploy.HostOptions) (unitdeploy.HostResult, error) {
		called++
		require.Same(t, snapshot, got)
		require.Equal(t, "compute", options.HostID)
		require.NotEmpty(t, options.StateDirectory)
		require.NotEmpty(t, options.OperatorDirectory)
		return unitdeploy.HostResult{Stage: "host-paused"}, nil
	}})
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{"deploy-host"})
	require.ErrorContains(t, command.Execute(), "--host")
	require.Zero(t, loaded)
	command.SetArgs([]string{"deploy-host", "--host", "compute", "--state-dir", t.TempDir(), "--operator-dir", t.TempDir()})
	require.NoError(t, command.Execute())
	require.Equal(t, 1, loaded)
	require.Equal(t, 1, called)
	require.Equal(t, []string{"compute"}, watchdogHosts, "主机服务就绪后安装守护")
	require.Contains(t, output.String(), `"stage":"host-paused"`)
	require.NotContains(t, output.String(), "admin-test-password")
}

// 只能出站的主机：部署前先确认 control 的防火墙端口，确认失败不部署；直达主机不调用。
func TestSetupDeployHostOpensControlFirewallForOutboundHost(t *testing.T) {
	snapshot := setupSnapshot(t)
	var steps []string
	firewallErr := errors.New("tencent unavailable")
	deps := setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		ensureRelayFirewall: func(_ context.Context, got *setupconfig.Snapshot) (setupFirewallSummary, error) {
			require.Same(t, snapshot, got)
			steps = append(steps, "firewall")
			return setupFirewallSummary{Status: "ready", Created: 1}, firewallErr
		},
		deployHost: func(context.Context, *setupconfig.Snapshot, unitdeploy.HostOptions) (unitdeploy.HostResult, error) {
			steps = append(steps, "deploy")
			return unitdeploy.HostResult{Stage: "host-ready"}, nil
		},
		installWatchdog: func(context.Context, *setupconfig.Snapshot, setupconfig.Host, bool) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		},
	}
	run := func() error {
		command := newSetupCommand(deps)
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		command.SetArgs([]string{"deploy-host", "--host", "compute", "--state-dir", t.TempDir(), "--operator-dir", t.TempDir()})
		return command.Execute()
	}
	require.NoError(t, run())
	require.Equal(t, []string{"deploy"}, steps, "直达主机不改 control 防火墙")

	definition := snapshot.Manifest.HostCatalog["compute"]
	definition.Reachability = "outbound"
	snapshot.Manifest.HostCatalog["compute"] = definition
	steps = nil
	require.ErrorContains(t, run(), "tencent unavailable")
	require.Equal(t, []string{"firewall"}, steps, "防火墙确认失败时不部署")
	firewallErr, steps = nil, nil
	require.NoError(t, run())
	require.Equal(t, []string{"firewall", "deploy"}, steps)
}
