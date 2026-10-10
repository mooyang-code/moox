package bootstrap

import (
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/placement"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/stretchr/testify/require"
)

func TestReporterDeploymentExpected(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close() })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	checks := manager.Repositories().Checks
	create := func(host, component string, enabled bool) {
		require.NoError(t, checks.Create(t.Context(), &domain.Check{
			CheckID: placement.CheckID(host, component), Name: component, Source: domain.CheckSourcePlacement, Enabled: enabled,
			IntervalSeconds: 30, TimeoutMS: 3000, Labels: `{"host_id":"` + host + `","component_id":"` + component + `"}`,
		}))
	}
	create("storage", "storage-view", true)
	create("storage", "access", false)

	cases := []struct {
		name    string
		service monitorobservability.ServiceStatus
		want    bool
	}{
		{"启用的部署：上报中断要告警", monitorobservability.ServiceStatus{NodeID: "storage", ServiceName: "storage-view"}, true},
		{"停用的部署：不告警", monitorobservability.ServiceStatus{NodeID: "storage", ServiceName: "access"}, false},
		{"部署已删除，目录里残留旧上报行：不告警", monitorobservability.ServiceStatus{NodeID: "storage", ServiceName: "storage-node"}, false},
		{"外部上报方（SCF 采集函数）没有部署检查：照常告警", monitorobservability.ServiceStatus{NodeID: "scf", ServiceName: "scf-collector"}, true},
	}
	for _, c := range cases {
		got, err := reporterDeploymentExpected(t.Context(), checks, c.service)
		require.NoError(t, err, c.name)
		require.Equal(t, c.want, got, c.name)
	}
}
