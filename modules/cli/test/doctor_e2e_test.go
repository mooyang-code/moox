package test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	doctorcli "github.com/mooyang-code/moox/modules/cli/internal/doctor"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	core "github.com/mooyang-code/moox/packages/doctor"
	"github.com/stretchr/testify/require"
)

type e2eBootstrapDeployments struct {
	rows []doctorcli.Deployment
}

func (d e2eBootstrapDeployments) ListDeployments(context.Context, string) ([]doctorcli.Deployment, error) {
	return d.rows, nil
}

type e2eOverviewClient struct {
	overview *monitorpb.HealthOverview
}

func (c e2eOverviewClient) GetHealthOverview(context.Context, string) (*monitorpb.HealthOverview, error) {
	return c.overview, nil
}

func writeE2EVersion(t *testing.T, root string) {
	t.Helper()
	manifest, err := core.LoadEmbeddedManifest()
	require.NoError(t, err)
	raw := fmt.Sprintf(`{"version":"dev","commit":"dev","catalog_sha256":%q,"deployed_at":"2026-11-28T00:00:00Z","components":{}}`, manifest.Checksum)
	require.NoError(t, os.WriteFile(filepath.Join(root, "version.json"), []byte(raw), 0o600))
}

func TestDoctorBootstrapCombinesLocalFactsAndMonitorOverview(t *testing.T) {
	root := t.TempDir()
	writeE2EVersion(t, root)
	deployments := []doctorcli.Deployment{
		{HostID: "node-a", ComponentID: "host-gateway", Status: "enabled"},
		{HostID: "node-a", ComponentID: "host-agent", Status: "enabled"},
		{HostID: "node-a", ComponentID: "factor-mgr", Status: "enabled"},
	}
	overview := &monitorpb.HealthOverview{
		TopologyKnown: true,
		Components: []*monitorpb.HealthComponent{
			{HostId: "node-a", ComponentId: "host-gateway", Status: "healthy", Readiness: "ready", Reporter: &monitorpb.HealthSignal{Status: "healthy"}},
			{HostId: "node-a", ComponentId: "host-agent", Status: "healthy", Reporter: &monitorpb.HealthSignal{Status: "healthy"}},
			{HostId: "node-a", ComponentId: "factor-mgr", Status: "down", Reason: "监控上报已中断", Reporter: &monitorpb.HealthSignal{Status: "degraded", Reason: "监控上报已中断"}},
		},
	}
	report, err := doctorcli.RunBootstrap(context.Background(), doctorcli.BootstrapOptions{
		NodeID:        "node-a",
		LocalNodeID:   "node-a",
		DeployRoot:    root,
		CheckIDs:      []string{"service.health:factor-mgr@node-a", "monitor.component:factor-mgr@node-a", "monitor.alerts:node-a"},
		Client:        e2eBootstrapDeployments{rows: deployments},
		MonitorClient: e2eOverviewClient{overview: overview},
		Probe: func(context.Context, string, int, string) (doctorcli.ProbeResult, error) {
			return doctorcli.ProbeResult{StatusCode: 200}, nil
		},
	})
	require.NoError(t, err)
	require.Equal(t, core.ConclusionUnhealthy, report.Conclusion)
	require.Equal(t, core.StatusPass, report.CheckByID("service.health:factor-mgr@node-a").Status)
	require.Equal(t, core.StatusFail, report.CheckByID("monitor.component:factor-mgr@node-a").Status)
	require.Equal(t, core.StatusPass, report.CheckByID("monitor.alerts:node-a").Status)
	raw, err := doctorcli.Render(report, "json")
	require.NoError(t, err)
	require.Contains(t, string(raw), `"schema_version": "doctor.moox.dev/v1"`)
	require.Contains(t, string(raw), `"mode": "bootstrap"`)
}

func TestDoctorBootstrapReportsCriticalAlertsFromMonitorOverview(t *testing.T) {
	root := t.TempDir()
	writeE2EVersion(t, root)
	deployments := []doctorcli.Deployment{
		{HostID: "node-a", ComponentID: "host-gateway", Status: "enabled"},
		{HostID: "node-a", ComponentID: "host-agent", Status: "enabled"},
		{HostID: "node-a", ComponentID: "factor-mgr", Status: "enabled"},
	}
	overview := &monitorpb.HealthOverview{
		TopologyKnown: true,
		Components: []*monitorpb.HealthComponent{
			{HostId: "node-a", ComponentId: "factor-mgr", Status: "healthy", Readiness: "ready", Reporter: &monitorpb.HealthSignal{Status: "healthy"}},
		},
		Alerts: []*monitorpb.HealthAlert{
			{Id: "node-a/ready:node-a:factor-mgr", Severity: "critical", Title: "因子管理未就绪", Object: &monitorpb.HealthObject{Type: "component", HostId: "node-a", ComponentId: "factor-mgr"}},
		},
	}
	report, err := doctorcli.RunBootstrap(context.Background(), doctorcli.BootstrapOptions{
		NodeID:        "node-a",
		LocalNodeID:   "node-a",
		DeployRoot:    root,
		CheckIDs:      []string{"monitor.component:factor-mgr@node-a", "monitor.alerts:node-a"},
		Client:        e2eBootstrapDeployments{rows: deployments},
		MonitorClient: e2eOverviewClient{overview: overview},
	})
	require.NoError(t, err)
	require.Equal(t, core.ConclusionUnhealthy, report.Conclusion)
	require.Equal(t, core.StatusPass, report.CheckByID("monitor.component:factor-mgr@node-a").Status)
	require.Equal(t, core.StatusFail, report.CheckByID("monitor.alerts:node-a").Status)
}
