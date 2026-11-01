package command

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/stretchr/testify/require"
)

func restartManifest() setupconfig.Manifest {
	return setupconfig.Manifest{
		Paths:       setupconfig.Paths{DeployRoot: "/data/moox", ControlRoot: "/data/moox/prod", StorageRoot: "/data/moox/storage"},
		HostCatalog: map[string]setupconfig.HostDefinition{"control": {Address: "192.0.2.1"}, "storage": {Address: "192.0.2.2"}, "factor": {Address: "192.0.2.3"}},
		Deployments: map[string][]string{"control": {"admin", "monitor"}, "storage": {"storage-node", "collector-stream"}, "factor": {"factor-engine"}},
	}
}

func TestSetupRestartLocatesUnitDirectory(t *testing.T) {
	manifest := restartManifest()
	for _, item := range []struct {
		host, component, directory string
		external                   bool
	}{
		{"control", "host-agent", "/data/moox/host", false},
		{"storage", "host-gateway", "/data/moox/host", false},
		{"control", "monitor", "/data/moox/prod", false},
		{"storage", "storage-node", "/data/moox/storage", false},
		{"storage", "collector-stream", "/data/moox/collector-stream", false},
		{"factor", "factor-engine", "/data/moox", true},
	} {
		target, err := setupRestartLocate(manifest, item.host, item.component)
		require.NoError(t, err, item.component)
		require.Equal(t, item.directory, target.Directory, item.component)
		require.Equal(t, item.external, target.External, item.component)
	}
	_, err := setupRestartLocate(manifest, "nobody", "monitor")
	require.ErrorContains(t, err, "没有主机")
	_, err = setupRestartLocate(manifest, "storage", "monitor")
	require.ErrorContains(t, err, "没有部署在主机")
	_, err = setupRestartLocate(manifest, "control", "nothing")
	require.ErrorContains(t, err, "服务目录里没有组件")
}

func TestSetupRestartPausedDetected(t *testing.T) {
	require.True(t, setupRestartPaused(`{"components":[{"id":"monitor","state":"paused"}]}`))
	require.False(t, setupRestartPaused(`{"components":[{"id":"monitor","state":"running"}]}`))
}

func TestSetupRestartPreviewsUntilConfirmed(t *testing.T) {
	snapshot := &setupconfig.Snapshot{Manifest: restartManifest()}
	called := 0
	deps := setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		restartComponent: func(_ context.Context, target setupRestartTarget) (json.RawMessage, error) {
			called++
			require.Equal(t, "monitor", target.Component)
			return json.RawMessage(`{"state":"running"}`), nil
		},
	}
	run := func(args ...string) string {
		var output bytes.Buffer
		cmd := newSetupRestartCommand(deps)
		cmd.SetOut(&output)
		cmd.SetArgs(append([]string{"--host", "control", "--component", "monitor"}, args...))
		require.NoError(t, cmd.Execute())
		return output.String()
	}
	require.Contains(t, run(), "restart_component")
	require.Zero(t, called, "默认只预览")
	require.Contains(t, run("--yes"), `"state":"running"`)
	require.Equal(t, 1, called)
}
