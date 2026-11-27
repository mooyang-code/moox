package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/mooyang-code/moox/packages/doctor"
	"github.com/stretchr/testify/require"
)

type deploymentClientStub struct{ rows []Deployment }

func (s deploymentClientStub) ListDeployments(context.Context, string) ([]Deployment, error) {
	return s.rows, nil
}

func writeVersion(t *testing.T, root, checksum string) {
	t.Helper()
	raw := fmt.Sprintf(`{"version":"dev","commit":"dev","catalog_sha256":%q,"deployed_at":"2026-11-15T00:00:00Z","components":{}}`, checksum)
	require.NoError(t, os.WriteFile(filepath.Join(root, "version.json"), []byte(raw), 0o600))
}

func writePolicy(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "dataset-health-policy.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: 2\nrealtime_timeseries:\n  defaults:\n    run_missed_intervals: 2\n    success_missed_intervals: 3\n    minute_lateness: 30s\n    hour_lateness: 1m\n  overrides: []\n"), 0o600))
	return path
}

func TestRunBootstrapInventory(t *testing.T) {
	manifest, err := core.LoadEmbeddedManifest()
	require.NoError(t, err)
	root := t.TempDir()
	rows := make([]Deployment, 0, len(manifest.Components))
	for _, component := range manifest.Components {
		rows = append(rows, Deployment{ComponentID: component.ComponentID, HostID: "node-a", Status: "enabled"})
	}
	writeVersion(t, root, manifest.Checksum)
	report, err := RunBootstrap(context.Background(), BootstrapOptions{NodeID: "node-a", LocalNodeID: "node-a", DeployRoot: root, DatasetHealthPolicyPath: writePolicy(t, root), CheckIDs: []string{"bootstrap.inventory"}, Client: deploymentClientStub{rows: rows}})
	require.NoError(t, err)
	require.Equal(t, core.ConclusionHealthy, report.Conclusion)
	require.Len(t, report.Checks, 2)
}

// 部署记录的服务目录摘要与 CLI 内嵌的不一致、没有部署记录时，发布契约检查不通过。
func TestRunBootstrapChecksDeployedCatalog(t *testing.T) {
	manifest, err := core.LoadEmbeddedManifest()
	require.NoError(t, err)
	root := t.TempDir()
	policy := writePolicy(t, root)
	run := func(options BootstrapOptions) core.CheckStatus {
		options.NodeID, options.LocalNodeID, options.DatasetHealthPolicyPath = "node-a", "node-a", policy
		options.CheckIDs = []string{"bootstrap.release_contract"}
		report, err := RunBootstrap(context.Background(), options)
		require.NoError(t, err)
		return report.CheckByID("bootstrap.release_contract").Status
	}
	require.Equal(t, core.StatusFail, run(BootstrapOptions{DeployRoot: root}), "没有 version.json")
	writeVersion(t, root, "sha256:"+strings.Repeat("0", 64))
	require.Equal(t, core.StatusFail, run(BootstrapOptions{DeployRoot: root}), "摘要不一致")
	writeVersion(t, root, manifest.Checksum)
	require.Equal(t, core.StatusPass, run(BootstrapOptions{DeployRoot: root}))
	require.Equal(t, core.StatusFail, run(BootstrapOptions{}), "没有部署根")
	// 远端节点经注入的读取函数取 version.json。
	var asked string
	read := func(_ context.Context, node, path string) ([]byte, error) {
		asked = node + ":" + path
		return os.ReadFile(path)
	}
	require.Equal(t, core.StatusPass, run(BootstrapOptions{DeployRoot: root, ReadFile: read}))
	require.Equal(t, "node-a:"+filepath.Join(root, "version.json"), asked)
}

func TestRunBootstrapRemoteNodeRequiresRemoteCapabilities(t *testing.T) {
	_, err := RunBootstrap(context.Background(), BootstrapOptions{NodeID: "remote", LocalNodeID: "local"})
	require.ErrorContains(t, err, "requires remote SSH capabilities")

	manifest, err := core.LoadEmbeddedManifest()
	require.NoError(t, err)
	root := t.TempDir()
	writeVersion(t, root, manifest.Checksum)
	policy := writePolicy(t, root)
	var readNode, readPath, probedNode, probedPath string
	versionRaw, err := os.ReadFile(filepath.Join(root, "version.json"))
	require.NoError(t, err)
	report, err := RunBootstrap(context.Background(), BootstrapOptions{
		NodeID:                  "remote",
		LocalNodeID:             "local",
		DeployRoot:              "/data/moox",
		DatasetHealthPolicyPath: policy,
		CheckIDs:                []string{"bootstrap.path_permissions:host-gateway@remote", "service.health:host-gateway@remote"},
		Client: deploymentClientStub{rows: []Deployment{
			{ComponentID: "host-gateway", HostID: "remote", Status: "enabled"},
			{ComponentID: "host-agent", HostID: "remote", Status: "enabled"},
		}},
		ReadFile: func(_ context.Context, node, filePath string) ([]byte, error) {
			readNode, readPath = node, filePath
			return versionRaw, nil
		},
		Probe: func(_ context.Context, node string, _ int, _ string) (ProbeResult, error) {
			probedNode = node
			return ProbeResult{StatusCode: 200}, nil
		},
		ProbeWritable: func(_ context.Context, _, rel string) error {
			probedPath = rel
			return nil
		},
	})
	require.NoError(t, err)
	require.Equal(t, core.ConclusionHealthy, report.Conclusion)
	require.Equal(t, "remote", readNode)
	require.Equal(t, "/data/moox/version.json", readPath)
	require.Equal(t, "remote", probedNode)
	require.Equal(t, "data/host-gateway", probedPath)
	require.Equal(t, "/data/moox/version.json", joinNodePath(true, "/data/moox/", "version.json"))
	require.Equal(t, "data/archive-worker", joinNodePath(true, "data", "archive-worker"))
}

// 自启动检查对每个组件都读 systemd 用户服务 moox-<组件>.service；数据目录检查写的是 <部署根>/data/<组件>。
func TestBootstrapRunnerUsesInjectedHostCapabilities(t *testing.T) {
	manifest, err := core.LoadEmbeddedManifest()
	require.NoError(t, err)
	deployRoot := t.TempDir()
	var probed []string
	var units []string
	runner := &bootstrapRunner{
		manifest:    manifest,
		deployments: map[string]Deployment{"factor-mgr": {ComponentID: "factor-mgr", Status: "enabled"}, "factor-engine": {ComponentID: "factor-engine", Status: "enabled"}},
		options: BootstrapOptions{
			NodeID:        "node-a",
			DeployRoot:    deployRoot,
			ProbeWritable: func(_ context.Context, root, path string) error { probed = append(probed, root+"|"+path); return nil },
			Systemd: func(_ context.Context, node, unit string) (SystemdState, error) {
				units = append(units, node+"/"+unit)
				return SystemdState{Enabled: true, Active: true, Linger: true}, nil
			},
		},
	}
	for _, id := range []string{"factor-mgr", "factor-engine"} {
		require.Equal(t, core.StatusPass, runner.run(context.Background(), core.CheckSpec{ID: "bootstrap.path_permissions:" + id + "@node-a"}, nil).Status)
		require.Equal(t, core.StatusPass, runner.run(context.Background(), core.CheckSpec{ID: "bootstrap.service_autostart:" + id + "@node-a"}, nil).Status)
	}
	require.Equal(t, []string{deployRoot + "|data/factor-mgr", deployRoot + "|data/factor-engine"}, probed)
	require.Equal(t, []string{"node-a/moox-factor-mgr.service", "node-a/moox-factor-engine.service"}, units)

	// systemd 状态不全（active 但没开机自启）不通过；读不到时记为无法判断。
	runner.options.Systemd = func(context.Context, string, string) (SystemdState, error) {
		return SystemdState{Enabled: false, Active: true, Linger: true}, nil
	}
	require.Equal(t, core.StatusFail, runner.run(context.Background(), core.CheckSpec{ID: "bootstrap.service_autostart:factor-mgr@node-a"}, nil).Status)
	runner.options.Systemd = nil
	require.Equal(t, core.StatusUnknown, runner.run(context.Background(), core.CheckSpec{ID: "bootstrap.service_autostart:factor-mgr@node-a"}, nil).Status)
	runner.options.DeployRoot = ""
	require.Equal(t, core.StatusWarn, runner.run(context.Background(), core.CheckSpec{ID: "bootstrap.path_permissions:factor-mgr@node-a"}, nil).Status)
}

func TestReporterFailureGateUsesRecentErrorTimestamp(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	body := []byte("moox_factor_report_errors_total 4\nmoox_factor_report_last_error_timestamp_seconds 800\n")
	require.False(t, reporterHasRecentFailure(body, now))
	recent := []byte("moox_factor_report_errors_total 5\nmoox_factor_report_last_error_timestamp_seconds 999\n")
	require.True(t, reporterHasRecentFailure(recent, now))
	moduleRecent := []byte("moox_factor_metrics_errors_total{operation=\"run\"} 1\nmoox_factor_metrics_last_error_timestamp_seconds 999\n")
	require.True(t, reporterHasRecentFailure(moduleRecent, now))
}

// bootstrap 只查本节点：健康与身份检查请求本节点上组件只听本机的健康端口。
func TestBootstrapProbesTheLocalHealthPort(t *testing.T) {
	manifest, err := core.LoadEmbeddedManifest()
	require.NoError(t, err)
	var asked []string
	runner := &bootstrapRunner{
		manifest:    manifest,
		deployments: map[string]Deployment{"factor-mgr": {ComponentID: "factor-mgr", Status: "enabled"}},
		options: BootstrapOptions{NodeID: "node-a", Probe: func(_ context.Context, node string, port int, path string) (ProbeResult, error) {
			asked = append(asked, fmt.Sprintf("%s:%d%s", node, port, path))
			return ProbeResult{StatusCode: 200}, nil
		}},
	}
	require.Equal(t, core.StatusPass, runner.run(context.Background(), core.CheckSpec{ID: "service.health:factor-mgr@node-a"}, nil).Status)
	require.Equal(t, []string{"node-a:11414/readyz"}, asked)
	runner.options.Probe = nil
	require.Equal(t, core.StatusFail, runner.run(context.Background(), core.CheckSpec{ID: "service.health:factor-mgr@node-a"}, nil).Status)
}
