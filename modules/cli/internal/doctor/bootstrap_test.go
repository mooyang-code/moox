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

type deploymentClientStub struct{ rows []*adminpb.ServiceDeployment }

func (s deploymentClientStub) ListDeployments(context.Context, string) ([]*adminpb.ServiceDeployment, error) {
	return s.rows, nil
}

func TestRunBootstrapInventory(t *testing.T) {
	manifest, err := core.LoadEmbeddedManifest()
	require.NoError(t, err)
	root := t.TempDir()
	seed := "version: 1\nservices:\n"
	rows := make([]*adminpb.ServiceDeployment, 0, len(manifest.Components))
	for _, component := range manifest.Components {
		seed += "  - name: " + component.ServiceName + "\n    kind: service\n    deployment_mode: process\n    status: active\n"
		rows = append(rows, &adminpb.ServiceDeployment{ServiceName: component.ServiceName, NodeId: "node-a", Status: "active"})
	}
	seedPath := filepath.Join(root, "seed.yaml")
	pipelinePath := filepath.Join(root, "pipelines.yaml")
	manifestDir := filepath.Join(root, "config", "doctor")
	require.NoError(t, os.MkdirAll(manifestDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(manifestDir, "components.yaml"), core.EmbeddedManifest(), 0o600))
	releaseManifest, err := core.LoadManifestFile(filepath.Join(manifestDir, "components.yaml"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(manifestDir, "components.yaml.sha256"), []byte(releaseManifest.Checksum), 0o600))
	require.NoError(t, os.WriteFile(seedPath, []byte(seed), 0o600))
	require.NoError(t, os.WriteFile(pipelinePath, []byte("version: 1\npipelines: []\n"), 0o600))
	report, err := RunBootstrap(context.Background(), BootstrapOptions{NodeID: "node-a", LocalNodeID: "node-a", ReleaseRoot: root, SeedPath: seedPath, PipelinePath: pipelinePath, CheckIDs: []string{"bootstrap.inventory"}, Client: deploymentClientStub{rows: rows}})
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
	runner := &bootstrapRunner{
		manifest:    manifest,
		deployments: map[string]*adminpb.ServiceDeployment{"moox_factor": {ServiceName: "moox_factor", Status: "active"}},
		options: BootstrapOptions{
			NodeID:        "node-a",
			ReleaseRoot:   t.TempDir(),
			ProbeWritable: func(context.Context, string, string) error { pathCalls++; return nil },
			ProcessAlive:  func(string) bool { processCalls++; return true },
		},
	}
	pathResult := runner.run(context.Background(), core.CheckSpec{ID: "bootstrap.path_permissions:moox_factor@node-a"}, nil)
	processResult := runner.run(context.Background(), core.CheckSpec{ID: "bootstrap.service_autostart:moox_factor@node-a"}, nil)
	require.Equal(t, core.StatusPass, pathResult.Status)
	require.Equal(t, core.StatusPass, processResult.Status)
	require.Positive(t, pathCalls)
	require.Equal(t, 1, processCalls)
}

func TestReporterFailureGateUsesRecentErrorTimestamp(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	body := []byte("moox_metrics_report_errors_total{service=\"moox_factor\"} 4\nmoox_metrics_report_last_error_timestamp_seconds{service=\"moox_factor\"} 800\n")
	require.False(t, reporterHasRecentFailure(body, now))
	recent := []byte("moox_metrics_report_errors_total{service=\"moox_factor\"} 5\nmoox_metrics_report_last_error_timestamp_seconds{service=\"moox_factor\"} 999\n")
	require.True(t, reporterHasRecentFailure(recent, now))
}
