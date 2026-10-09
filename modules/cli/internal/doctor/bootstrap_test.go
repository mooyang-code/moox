package doctor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	core "github.com/mooyang-code/moox/packages/doctor"
	"github.com/stretchr/testify/require"
)

type placementClientStub struct{ rows []*adminpb.DeployPlacement }

func (s placementClientStub) ListPlacements(context.Context, string) ([]*adminpb.DeployPlacement, error) {
	return s.rows, nil
}

func writeDatasetHealthPolicy(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "dataset-health-policy.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: 2\nrealtime_timeseries:\n  defaults:\n    run_missed_intervals: 2\n    success_missed_intervals: 3\n    watermark_periods: 3\n    minimum_watermark_lag: 10m\n  overrides: []\n"), 0o600))
	return path
}

func TestRunBootstrapInventory(t *testing.T) {
	manifest, err := core.LoadEmbeddedManifest()
	require.NoError(t, err)
	root := t.TempDir()
	rows := make([]*adminpb.DeployPlacement, 0, len(manifest.Components))
	for _, component := range manifest.Components {
		rows = append(rows, &adminpb.DeployPlacement{HostId: "node-a", ComponentId: component.ComponentID, Status: "enabled"})
	}
	report, err := RunBootstrap(context.Background(), BootstrapOptions{
		NodeID: "node-a", LocalNodeID: "node-a", ReleaseRoot: root, DatasetHealthPolicyPath: writeDatasetHealthPolicy(t, root),
		CheckIDs: []string{"bootstrap.inventory"}, Client: placementClientStub{rows: rows},
	})
	require.NoError(t, err)
	require.Equal(t, core.ConclusionHealthy, report.Conclusion)
	require.Len(t, report.Checks, 2)
	require.Equal(t, manifest.Checksum, report.ManifestChecksum, "诊断报告标明依据的组件目录版本")
}

func TestRunBootstrapInventoryFailsWithoutPlacements(t *testing.T) {
	root := t.TempDir()
	report, err := RunBootstrap(context.Background(), BootstrapOptions{
		NodeID: "node-a", LocalNodeID: "node-a", ReleaseRoot: root, DatasetHealthPolicyPath: writeDatasetHealthPolicy(t, root),
		CheckIDs: []string{"bootstrap.inventory"}, Client: placementClientStub{},
	})
	require.NoError(t, err)
	require.NotEqual(t, core.ConclusionHealthy, report.Conclusion)
}

func TestRunBootstrapRejectsRemoteNode(t *testing.T) {
	_, err := RunBootstrap(context.Background(), BootstrapOptions{NodeID: "remote", LocalNodeID: "local"})
	require.ErrorContains(t, err, "only accepts the local node")
}

func TestBootstrapRunnerUsesInjectedHostCapabilities(t *testing.T) {
	manifest, err := core.LoadEmbeddedManifest()
	require.NoError(t, err)
	root := t.TempDir()
	var pidPaths []string
	runner := &bootstrapRunner{
		manifest:   manifest,
		placements: map[string]*adminpb.DeployPlacement{"factor-mgr": {HostId: "node-a", ComponentId: "factor-mgr", Status: "enabled"}},
		options: BootstrapOptions{
			NodeID:       "node-a",
			ReleaseRoot:  root,
			ProcessAlive: func(path string) bool { pidPaths = append(pidPaths, path); return true },
		},
	}
	processResult := runner.run(context.Background(), core.CheckSpec{ID: "bootstrap.service_autostart:factor-mgr@node-a"}, nil)
	require.Equal(t, core.StatusPass, processResult.Status)
	require.Equal(t, []string{filepath.Join(root, "run", "factor-mgr.pid")}, pidPaths, "PID 文件按组件 ID 命名")

	skipped := runner.run(context.Background(), core.CheckSpec{ID: "bootstrap.service_autostart:collector@node-a"}, nil)
	require.Equal(t, core.StatusSkipped, skipped.Status, "没有部署在这台主机上的组件跳过")
}

func TestLocalHealthURLUsesCatalogHealthPort(t *testing.T) {
	manifest, err := core.LoadEmbeddedManifest()
	require.NoError(t, err)
	collector, ok := manifest.Component("collector")
	require.True(t, ok)
	require.Equal(t, "http://127.0.0.1:11412/readyz", localHealthURL(collector, collector.HealthPath))
	require.Equal(t, "http://127.0.0.1:11412/metrics", localHealthURL(collector, "/metrics"))
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
