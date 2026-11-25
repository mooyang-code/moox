package deployment

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
)

type memorySource struct {
	snapshot Snapshot
	err      error
}

func (s *memorySource) Snapshot(context.Context) (Snapshot, error) { return s.snapshot, s.err }

func testSnapshot(t *testing.T) Snapshot {
	t.Helper()
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	return Snapshot{Catalog: catalog,
		Hosts: []*adminpb.DeploymentHost{
			{HostId: "control", Address: "control.example.test", Region: "ap-guangzhou", Status: "enabled"},
			{HostId: "storage", Address: "storage.example.test", PrivateAddress: "2001:db8::2", Region: "ap-guangzhou", Status: "enabled"},
		},
		Deployments: []*adminpb.ComponentDeployment{
			{HostId: "control", ComponentId: "admin", Status: "enabled"},
			{HostId: "storage", ComponentId: "storage-primary", Status: "enabled"},
		},
	}
}

// 同步只写部署拓扑，不生成任何探测检查；发现结果不完整或不合法时保留上一份拓扑。
func TestSyncWritesTopologyWithoutProbeChecks(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	repos := manager.Repositories()
	source := &memorySource{snapshot: testSnapshot(t)}
	syncer := NewSyncer(repos.Topology, source)
	require.NoError(t, syncer.Sync(t.Context()))
	topology, err := repos.Topology.Snapshot(t.Context())
	require.NoError(t, err)
	require.Len(t, topology.Deployments, 2)
	count, err := repos.Checks.Count(t.Context())
	require.NoError(t, err)
	require.Zero(t, count, "部署同步不再生成探测检查")

	source.snapshot.Deployments[0].Status = "disabled"
	require.NoError(t, syncer.Sync(t.Context()))
	topology, err = repos.Topology.Snapshot(t.Context())
	require.NoError(t, err)
	require.Equal(t, "disabled", topology.Deployments[0].Status)

	source.err = errors.New("directory temporarily unavailable")
	require.Error(t, syncer.Sync(t.Context()))
	source.err = nil
	source.snapshot.Deployments[0].ComponentId = "unknown-component"
	require.Error(t, syncer.Sync(t.Context()))
	preserved, err := repos.Topology.Snapshot(t.Context())
	require.NoError(t, err)
	require.Equal(t, topology, preserved)
}
