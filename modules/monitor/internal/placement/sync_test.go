package placement

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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
			{HostId: "control", Address: "control.example.test", Status: "enabled"},
			{HostId: "storage", Address: "storage.example.test", PrivateAddress: "2001:db8::2", Status: "enabled"},
		},
		Placements: []*adminpb.ComponentPlacement{
			{HostId: "control", ComponentId: "console-proxy", Status: "enabled"},
			{HostId: "storage", ComponentId: "storage-primary", Status: "enabled"},
		},
	}
}

func TestChecksUseCatalogHealthAndPreserveProxyLoopback(t *testing.T) {
	snapshot := testSnapshot(t)
	checks, err := Checks(snapshot, map[string]domain.HTTPSConfig{"console-proxy": {
		URL: "https://console.example.test:9527/", ConnectAddress: "127.0.0.1:9527", ServerName: "console.example.test", TrustMode: "public",
	}})
	require.NoError(t, err)
	require.Len(t, checks, 3)
	require.Equal(t, "placement:control:console-proxy", checks[0].CheckID)
	require.Equal(t, "http://127.0.0.1:19528/readyz", checks[0].URL)
	require.Equal(t, `"ready":true`, checks[0].BodyContains)
	require.Equal(t, domain.CheckSourcePlacement, checks[0].Source)
	require.Equal(t, "console-page:control:console-proxy", checks[1].CheckID)
	require.Equal(t, "127.0.0.1:9527", checks[1].ConnectAddress)
	require.Equal(t, "200-399", checks[1].ExpectedStatus)
	require.Empty(t, checks[1].BodyContains)
	require.Equal(t, "http://[2001:db8::2]:20210/readyz", checks[2].URL)

	for index := range snapshot.Catalog.Components {
		if snapshot.Catalog.Components[index].ID == "storage-primary" {
			snapshot.Catalog.Components[index].Health = servicecatalog.Health{Kind: "none"}
		}
	}
	checks, err = Checks(snapshot, testHTTPS())
	require.NoError(t, err)
	require.Len(t, checks, 2, "health:none does not create an unknown probe")
}

func TestSyncPlacementLifecyclePreservesHistoryAndRetiresAlerts(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	repos := manager.Repositories()
	source := &memorySource{snapshot: testSnapshot(t)}
	syncer := NewSyncer(repos.Checks, source, testHTTPS(), nil)
	_, err = syncer.Sync(t.Context())
	require.NoError(t, err)
	id := CheckID("control", "console-proxy")
	checkedAt := time.Now().UTC().Add(-time.Minute)
	nextAt := checkedAt.Add(time.Hour)
	require.NoError(t, repos.Checks.MarkChecked(t.Context(), "", id, checkedAt, nextAt))
	require.NoError(t, repos.Results.Insert(t.Context(), &domain.CheckResult{ResultID: "probe", CheckID: id, CheckedAt: checkedAt, Status: "ok", Success: true}))
	_, err = syncer.Sync(t.Context())
	require.NoError(t, err)
	check, err := repos.Checks.Get(t.Context(), "", id)
	require.NoError(t, err)
	require.WithinDuration(t, nextAt, *check.NextCheckAt, time.Millisecond)
	require.NoError(t, repos.Alerts.CreateRule(t.Context(), &domain.AlertRule{RuleID: "default:" + id, CheckID: id, Enabled: true}))
	require.NoError(t, repos.Alerts.UpsertState(t.Context(), &domain.AlertState{RuleID: "default:" + id, CheckID: id, Status: domain.AlertStatusFiring}))

	for _, disableHost := range []bool{false, true} {
		if disableHost {
			source.snapshot.Hosts[0].Status = "disabled"
		} else {
			source.snapshot.Placements[0].Status = "disabled"
		}
		_, err = syncer.Sync(t.Context())
		require.NoError(t, err)
		check, err = repos.Checks.Get(t.Context(), "", id)
		require.NoError(t, err)
		require.False(t, check.Enabled)
		_, err = repos.Alerts.GetRule(t.Context(), "", "default:"+id)
		require.ErrorIs(t, err, gorm.ErrRecordNotFound)
		source.snapshot.Hosts[0].Status, source.snapshot.Placements[0].Status = "enabled", "enabled"
		_, err = syncer.Sync(t.Context())
		require.NoError(t, err)
		check, err = repos.Checks.Get(t.Context(), "", id)
		require.NoError(t, err)
		require.True(t, check.Enabled)
		require.Nil(t, check.NextCheckAt, "enabled deployment must be checked immediately")
	}

	// An incomplete API response must not remove the valid checks.
	source.err = errors.New("directory temporarily unavailable")
	_, err = syncer.Sync(t.Context())
	require.Error(t, err)
	source.err = nil
	source.snapshot.Placements[0].ComponentId = "unknown-component"
	_, err = syncer.Sync(t.Context())
	require.Error(t, err)
	_, err = repos.Checks.Get(t.Context(), "", id)
	require.NoError(t, err)
	source.snapshot.Placements = source.snapshot.Placements[1:]
	_, err = syncer.Sync(t.Context())
	require.NoError(t, err)
	_, err = repos.Checks.Get(t.Context(), "", id)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	history, err := repos.Results.Recent(t.Context(), "", id, 10)
	require.NoError(t, err)
	require.Empty(t, history)
}

func testHTTPS() map[string]domain.HTTPSConfig {
	return map[string]domain.HTTPSConfig{"console-proxy": {URL: "https://console.example.test:9527/", ConnectAddress: "127.0.0.1:9527", ServerName: "console.example.test", TrustMode: "public"}}
}

func TestSyncRollsBackAllDefinitionsOnOwnershipCollision(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	repos := manager.Repositories()
	require.NoError(t, repos.Checks.Create(t.Context(), &domain.Check{CheckID: CheckID("storage", "storage-primary"), Source: domain.CheckSourceObservability}))
	syncer := NewSyncer(repos.Checks, &memorySource{snapshot: testSnapshot(t)}, testHTTPS(), nil)
	count, err := syncer.Sync(t.Context())
	require.ErrorContains(t, err, "collides")
	require.Zero(t, count)
	_, err = repos.Checks.Get(t.Context(), "", CheckID("control", "console-proxy"))
	require.ErrorIs(t, err, gorm.ErrRecordNotFound, "earlier upserts must roll back")
}
