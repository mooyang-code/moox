package observability

import (
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestReverseReconciliationUsesAllPlacementsAndExactIdentity(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	query, repos := openOverviewState(t, func(db *gorm.DB) {
		for _, row := range []monmetrics.MetricService{
			{ServiceName: "collector", NodeID: "control", InstanceID: "current", BootID: "new", LastSeenAt: now},
			{ServiceName: "collector", NodeID: "control", InstanceID: "current", BootID: "old", LastSeenAt: now.Add(-time.Hour)},
			{ServiceName: "collector", NodeID: "disabled-host", InstanceID: "disabled-host", BootID: "1", LastSeenAt: now},
			{ServiceName: "archive", NodeID: "control", InstanceID: "disabled-placement", BootID: "1", LastSeenAt: now},
			{ServiceName: "storage-primary", NodeID: "storage", InstanceID: "orphan", BootID: "1", LastSeenAt: now},
			{ServiceName: "collector-extra", NodeID: "control", InstanceID: "unknown-component", BootID: "1", LastSeenAt: now},
			{ServiceName: "trade", NodeID: "control", InstanceID: "old-orphan", BootID: "1", LastSeenAt: now.Add(-3 * time.Minute)},
			{ServiceName: "scf-collector", NodeID: "function-1", InstanceID: "invocation", BootID: "1", LastSeenAt: now},
		} {
			require.NoError(t, db.Create(&row).Error)
		}
	})
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	for i := range catalog.Components {
		if catalog.Components[i].ID == "collector" || catalog.Components[i].ID == "factor-mgr" || catalog.Components[i].ID == "web-host" {
			catalog.Components[i].Health = servicecatalog.Health{Kind: "none"}
		}
	}
	snapshot := domain.TopologySnapshot{Catalog: catalog, ObservedAt: now,
		Hosts: []domain.TopologyHost{
			{HostID: "control", Address: "control.test", Status: "enabled"},
			{HostID: "disabled-host", Address: "disabled.test", Status: "disabled"},
			{HostID: "storage", Address: "storage.test", Status: "enabled"},
		},
		Placements: []domain.TopologyPlacement{
			{HostID: "control", ComponentID: "collector", Status: "enabled"},
			{HostID: "control", ComponentID: "factor-mgr", Status: "enabled"},
			{HostID: "control", ComponentID: "web-host", Status: "enabled"},
			{HostID: "control", ComponentID: "archive", Status: "disabled"},
			{HostID: "disabled-host", ComponentID: "collector", Status: "enabled"},
		},
	}
	_, err = repos.Topology.Reconcile(t.Context(), snapshot, nil)
	require.NoError(t, err)
	for _, space := range []string{"", "crypto"} {
		got, err := (Builder{Metrics: query, Topology: repos.Topology, Now: func() time.Time { return now }}).Build(t.Context(), space)
		require.NoError(t, err)
		require.True(t, got.TopologyKnown)
		require.Len(t, got.Services, 5)
		byKey := make(map[string]ServiceStatus)
		for _, service := range got.Services {
			byKey[placementKey(service.NodeID, service.ServiceName)] = service
		}
		collector := byKey[placementKey("control", "collector")]
		require.Equal(t, "healthy", collector.ReporterStatus)
		require.Equal(t, "unchecked", collector.ProbeStatus)
		require.Len(t, collector.Instances, 1, "boot history is not another process")
		require.Equal(t, "new", collector.Instances[0].BootID)
		missing := byKey[placementKey("control", "factor-mgr")]
		require.Equal(t, "missing", missing.ReporterStatus, "health:none does not erase a registered reporter expectation")
		require.Equal(t, "unchecked", missing.ProbeStatus)
		require.Equal(t, "unchecked", missing.Status, "none never becomes an unknown probe")
		require.True(t, missing.LastSeenAt.IsZero())
		require.Equal(t, "unchecked", byKey[placementKey("control", "web-host")].Status)
		for _, key := range []string{placementKey("control", "archive"), placementKey("disabled-host", "collector")} {
			require.False(t, byKey[key].Enabled)
			require.Equal(t, "disabled", byKey[key].Status)
			require.Equal(t, "healthy", byKey[key].ReporterStatus, "reporter fact survives disabling the deployment")
		}
		require.Len(t, got.Unregistered, 2, "disabled, stale and catalog principal reporters are not unregistered running components")
		require.Equal(t, "collector-extra", got.Unregistered[0].ServiceName, "similar names must not match")
		require.Equal(t, "storage-primary", got.Unregistered[1].ServiceName, "registration on another host must not match")
	}
}

func TestReverseReconciliationWaitsForFirstCompleteTopology(t *testing.T) {
	now := time.Now().UTC()
	query, repos := openOverviewState(t, func(db *gorm.DB) {
		require.NoError(t, db.Create(&monmetrics.MetricService{ServiceName: "collector", NodeID: "control", InstanceID: "current", BootID: "1", LastSeenAt: now}).Error)
	})
	builder := Builder{Metrics: query, Topology: repos.Topology, Now: func() time.Time { return now }}
	got, err := builder.Build(t.Context(), "")
	require.NoError(t, err)
	require.False(t, got.TopologyKnown)
	require.Empty(t, got.Unregistered)
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	_, err = repos.Topology.Reconcile(t.Context(), domain.TopologySnapshot{Catalog: catalog, ObservedAt: now}, nil)
	require.NoError(t, err)
	got, err = builder.Build(t.Context(), "")
	require.NoError(t, err)
	require.True(t, got.TopologyKnown)
	require.Len(t, got.Unregistered, 1, "a successful empty topology is authoritative")
}

func TestReverseReconciliationReadsSecondReporterPageAndRejectsOverflow(t *testing.T) {
	for _, count := range []int{501, 1001} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			now := time.Now().UTC()
			query, repos := openOverviewState(t, func(db *gorm.DB) {
				rows := make([]monmetrics.MetricService, count)
				for i := range rows {
					rows[i] = monmetrics.MetricService{ServiceName: "unregistered", NodeID: "control", InstanceID: fmt.Sprint(i), BootID: "1", LastSeenAt: now}
				}
				require.NoError(t, db.CreateInBatches(rows, 100).Error)
			})
			catalog, err := servicecatalog.LoadEmbedded()
			require.NoError(t, err)
			_, err = repos.Topology.Reconcile(t.Context(), domain.TopologySnapshot{Catalog: catalog, ObservedAt: now}, nil)
			require.NoError(t, err)
			got, err := (Builder{Metrics: query, Topology: repos.Topology, Now: func() time.Time { return now }}).Build(t.Context(), "")
			if count > maxOverviewServices {
				require.ErrorContains(t, err, "exceed limit")
				return
			}
			require.NoError(t, err)
			require.Len(t, got.Unregistered, count)
		})
	}
}
