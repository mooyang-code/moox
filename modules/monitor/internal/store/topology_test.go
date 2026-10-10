package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestTopologyPersistsFullRegistrationAndRollsBackWithChecks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.db")
	manager, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	for i := range catalog.Components {
		if catalog.Components[i].ID == "web-host" {
			catalog.Components[i].Health = servicecatalog.Health{Kind: "none"}
		}
	}
	snapshot := domain.TopologySnapshot{Catalog: catalog, ObservedAt: time.Now().UTC(),
		Hosts:      []domain.TopologyHost{{HostID: "control", Address: "control.test", Status: "enabled"}},
		Placements: []domain.TopologyPlacement{{HostID: "control", ComponentID: "web-host", Status: "disabled"}},
	}
	_, err = manager.Repositories().Topology.Reconcile(t.Context(), snapshot, nil)
	require.NoError(t, err)
	require.NoError(t, manager.Close())
	manager, err = Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	repos := manager.Repositories()
	before, err := repos.Topology.Snapshot(t.Context())
	require.NoError(t, err)
	require.Equal(t, snapshot, *before)
	component, _ := before.Catalog.Component("web-host")
	require.Equal(t, "none", component.Health.Kind)
	// The first probe upsert would succeed, but a later ownership collision
	// must roll back both it and the changed authoritative registration.
	require.NoError(t, repos.Checks.Create(t.Context(), &domain.Check{CheckID: "placement:control:monitor", Source: domain.CheckSourceObservability}))
	snapshot.Placements[0].Status = "enabled"
	_, err = repos.Topology.Reconcile(t.Context(), snapshot, []domain.Check{
		{CheckID: "placement:control:collector", Name: "collector", Kind: domain.CheckKindHTTP, Source: domain.CheckSourcePlacement},
		{CheckID: "placement:control:monitor", Name: "monitor", Kind: domain.CheckKindHTTP, Source: domain.CheckSourcePlacement},
	})
	require.ErrorContains(t, err, "collides")
	after, err := repos.Topology.Snapshot(t.Context())
	require.NoError(t, err)
	require.Equal(t, before, after)
	_, err = repos.Checks.Get(t.Context(), "", "placement:control:collector")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	// A complete, successfully discovered empty placement list removes the
	// registrations; it is distinct from a failed or absent discovery.
	snapshot.Placements = nil
	_, err = repos.Topology.Reconcile(t.Context(), snapshot, nil)
	require.NoError(t, err)
	after, err = repos.Topology.Snapshot(t.Context())
	require.NoError(t, err)
	require.NotNil(t, after)
	require.Empty(t, after.Placements)
}
