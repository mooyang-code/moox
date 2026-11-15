package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
	core "github.com/mooyang-code/moox/packages/doctor"
	"github.com/stretchr/testify/require"
)

type deploymentClientStub struct{ rows []Deployment }

func (s deploymentClientStub) ListDeployments(context.Context, string) ([]Deployment, error) {
	return s.rows, nil
}

func TestRunBootstrapInventory(t *testing.T) {
	manifest, err := core.LoadEmbeddedManifest()
	require.NoError(t, err)
	root := t.TempDir()
	rows := make([]Deployment, 0, len(manifest.Components))
	for _, component := range manifest.Components {
		rows = append(rows, Deployment{ComponentID: component.ComponentID, HostID: "node-a", Status: "enabled"})
	}
	datasetHealthPolicyPath := filepath.Join(root, "dataset-health-policy.yaml")
	manifestDir := filepath.Join(root, "config", "servicecatalog")
	require.NoError(t, os.MkdirAll(manifestDir, 0o700))
	manifestRaw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "packages", "servicecatalog", "catalog.yaml"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(manifestDir, "catalog.yaml"), manifestRaw, 0o600))
	releaseManifest, err := core.LoadManifestFile(filepath.Join(manifestDir, "catalog.yaml"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(manifestDir, "catalog.yaml.sha256"), []byte(releaseManifest.Checksum), 0o600))
	require.NoError(t, os.WriteFile(datasetHealthPolicyPath, []byte("version: 2\nrealtime_timeseries:\n  defaults:\n    run_missed_intervals: 2\n    success_missed_intervals: 3\n    minute_lateness: 30s\n    hour_lateness: 1m\n  overrides: []\n"), 0o600))
	report, err := RunBootstrap(context.Background(), BootstrapOptions{NodeID: "node-a", LocalNodeID: "node-a", ReleaseRoot: root, DatasetHealthPolicyPath: datasetHealthPolicyPath, CheckIDs: []string{"bootstrap.inventory"}, Client: deploymentClientStub{rows: rows}})
	require.NoError(t, err)
	require.Equal(t, core.ConclusionHealthy, report.Conclusion)
	require.Len(t, report.Checks, 2)
}

func TestRunBootstrapRejectsRemoteNode(t *testing.T) {
	_, err := RunBootstrap(context.Background(), BootstrapOptions{NodeID: "remote", LocalNodeID: "local"})
	require.ErrorContains(t, err, "only accepts the local node")
}

func TestBootstrapRunnerUsesInjectedHostCapabilities(t *testing.T) {
	manifest, err := core.LoadEmbeddedManifest()
	require.NoError(t, err)
	pathCalls, processCalls := 0, 0
	releaseRoot, deployRoot := t.TempDir(), t.TempDir()
	var pidPath string
	runner := &bootstrapRunner{
		manifest:    manifest,
		deployments: map[string]Deployment{"factor-mgr": {ComponentID: "factor-mgr", Status: "enabled"}},
		options: BootstrapOptions{
			NodeID:        "node-a",
			ReleaseRoot:   releaseRoot,
			DeployRoot:    deployRoot,
			ProbeWritable: func(context.Context, string, string) error { pathCalls++; return nil },
			ProcessAlive:  func(path string) bool { processCalls++; pidPath = path; return true },
		},
	}
	pathResult := runner.run(context.Background(), core.CheckSpec{ID: "bootstrap.path_permissions:factor-mgr@node-a"}, nil)
	processResult := runner.run(context.Background(), core.CheckSpec{ID: "bootstrap.service_autostart:factor-mgr@node-a"}, nil)
	require.Equal(t, core.StatusPass, pathResult.Status)
	require.Equal(t, core.StatusPass, processResult.Status)
	require.Positive(t, pathCalls)
	require.Equal(t, 1, processCalls)
	// 进程记录在 <部署根>/run/pids/<组件>.json，不在发布目录里。
	require.Equal(t, filepath.Join(deployRoot, "run", "pids", "factor-mgr.json"), pidPath)
}

func TestBootstrapServiceProcessReadsRuntimeRecord(t *testing.T) {
	manifest, err := core.LoadEmbeddedManifest()
	require.NoError(t, err)
	deployRoot := t.TempDir()
	runner := &bootstrapRunner{
		manifest:    manifest,
		deployments: map[string]Deployment{"factor-mgr": {ComponentID: "factor-mgr", Status: "enabled"}},
		options:     BootstrapOptions{NodeID: "node-a", ReleaseRoot: t.TempDir(), DeployRoot: deployRoot},
	}
	spec := core.CheckSpec{ID: "bootstrap.service_autostart:factor-mgr@node-a"}
	// 没有进程记录：提示进程不在。
	require.Equal(t, core.StatusWarn, runner.run(context.Background(), spec, nil).Status)

	record := unitruntime.PIDRecordPath(deployRoot, "factor-mgr")
	require.NoError(t, os.MkdirAll(filepath.Dir(record), 0o700))
	require.NoError(t, os.WriteFile(record, []byte(fmt.Sprintf(`{"pid":%d,"start_ticks":"1"}`, os.Getpid())), 0o600))
	require.Equal(t, core.StatusPass, runner.run(context.Background(), spec, nil).Status)

	// 没有部署根也读不到 runtime.json 时不假装检查过。
	runner.options.DeployRoot = ""
	result := runner.run(context.Background(), spec, nil)
	require.Equal(t, core.StatusWarn, result.Status)
	require.Contains(t, result.Summary, "部署根")
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
