package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
)

func TestTopologyPersistsFullRegistration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.db")
	manager, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	snapshot := domain.TopologySnapshot{Catalog: catalog, ObservedAt: time.Now().UTC(),
		Hosts:       []domain.TopologyHost{{HostID: "control", Address: "control.test", Status: "enabled"}},
		Deployments: []domain.TopologyDeployment{{HostID: "control", ComponentID: "strategy", Status: "disabled"}},
	}
	err = manager.Repositories().Topology.Reconcile(t.Context(), snapshot)
	require.NoError(t, err)
	require.NoError(t, manager.Close())
	manager, err = Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	repos := manager.Repositories()
	before, err := repos.Topology.Snapshot(t.Context())
	require.NoError(t, err)
	require.Equal(t, snapshot, *before)
	component, _ := before.Catalog.Component("strategy")
	require.Equal(t, 11431, component.Health.Port)
	// 拓扑不合法时不写入，库里仍是上一份完整拓扑。
	invalid := snapshot
	invalid.Deployments = []domain.TopologyDeployment{{HostID: "missing-host", ComponentID: "strategy", Status: "enabled"}}
	require.Error(t, repos.Topology.Reconcile(t.Context(), invalid))
	after, err := repos.Topology.Snapshot(t.Context())
	require.NoError(t, err)
	require.Equal(t, before, after)
	// A complete, successfully discovered empty deployment list removes the
	// registrations; it is distinct from a failed or absent discovery.
	snapshot.Deployments = nil
	err = repos.Topology.Reconcile(t.Context(), snapshot)
	require.NoError(t, err)
	after, err = repos.Topology.Snapshot(t.Context())
	require.NoError(t, err)
	require.NotNil(t, after)
	require.Empty(t, after.Deployments)
}
