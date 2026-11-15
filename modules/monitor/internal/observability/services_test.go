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

func TestReverseReconciliationUsesAllDeploymentsAndExactIdentity(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	query, repos := openOverviewState(t, func(db *gorm.DB) {
		for _, row := range []monmetrics.MetricService{
			{ServiceName: "collector", NodeID: "control", InstanceID: "current", BootID: "new", LastSeenAt: now},
			{ServiceName: "collector", NodeID: "control", InstanceID: "current", BootID: "old", LastSeenAt: now.Add(-time.Hour)},
			{ServiceName: "collector", NodeID: "disabled-host", InstanceID: "disabled-host", BootID: "1", LastSeenAt: now},
			{ServiceName: "egress-proxy", NodeID: "control", InstanceID: "disabled-deployment", BootID: "1", LastSeenAt: now},
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
	snapshot := domain.TopologySnapshot{Catalog: catalog, ObservedAt: now,
		Hosts: []domain.TopologyHost{
			{HostID: "control", Address: "control.test", Status: "enabled"},
			{HostID: "disabled-host", Address: "disabled.test", Status: "disabled"},
			{HostID: "storage", Address: "storage.test", Status: "enabled"},
		},
		Deployments: []domain.TopologyDeployment{
			{HostID: "control", ComponentID: "collector", Status: "enabled"},
			{HostID: "control", ComponentID: "factor-mgr", Status: "enabled"},
			{HostID: "control", ComponentID: "web-host", Status: "enabled"},
			{HostID: "control", ComponentID: "egress-proxy", Status: "disabled"},
			{HostID: "disabled-host", ComponentID: "collector", Status: "enabled"},
		},
	}
	err = repos.Topology.Reconcile(t.Context(), snapshot)
	require.NoError(t, err)
	for _, space := range []string{"", "crypto"} {
		got, err := (Builder{Metrics: query, Topology: repos.Topology, Now: func() time.Time { return now }}).Build(t.Context(), space)
		require.NoError(t, err)
		require.True(t, got.TopologyKnown)
		require.Len(t, got.Services, 5)
		byKey := make(map[string]ServiceStatus)
		for _, service := range got.Services {
			byKey[deploymentKey(service.NodeID, service.ServiceName)] = service
		}
		collector := byKey[deploymentKey("control", "collector")]
		require.Equal(t, "healthy", collector.ReporterStatus)
		require.Equal(t, "healthy", collector.Status)
		require.Len(t, collector.Instances, 1, "boot history is not another process")
		require.Equal(t, "new", collector.Instances[0].BootID)
		missing := byKey[deploymentKey("control", "factor-mgr")]
		require.Equal(t, "missing", missing.ReporterStatus, "登记了部署却没有上报")
		require.Equal(t, "down", missing.Status, "已启用的部署从没上报，进程没起来，与未就绪一样严重")
		require.True(t, missing.LastSeenAt.IsZero())
		require.Equal(t, "missing", byKey[deploymentKey("control", "web-host")].ReporterStatus, "不再有不要求上报的组件")
		for _, key := range []string{deploymentKey("control", "egress-proxy"), deploymentKey("disabled-host", "collector")} {
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
	err = repos.Topology.Reconcile(t.Context(), domain.TopologySnapshot{Catalog: catalog, ObservedAt: now})
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
			err = repos.Topology.Reconcile(t.Context(), domain.TopologySnapshot{Catalog: catalog, ObservedAt: now})
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

// 生命周期只在上报新鲜时挂上：停止上报的模块不能显示成它最后一次上报的暂停状态。
func TestServiceLifecycleOnlyFromFreshReport(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	paused := `{"stage":"paused","pauseKind":"system","pausedSince":"2026-11-15T01:00:00Z"}`
	query, repos := openOverviewState(t, func(db *gorm.DB) {
		require.NoError(t, db.Create(&monmetrics.MetricService{ServiceName: "strategy", NodeID: "control", InstanceID: "strategy@control", BootID: "1", LastSeenAt: now, Readiness: monmetrics.ReadinessReady, LifecycleJSON: paused}).Error)
		require.NoError(t, db.Create(&monmetrics.MetricService{ServiceName: "trade", NodeID: "control", InstanceID: "trade@control", BootID: "1", LastSeenAt: now.Add(-10 * time.Minute), Readiness: monmetrics.ReadinessReady, LifecycleJSON: paused}).Error)
	})
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	require.NoError(t, repos.Topology.Reconcile(t.Context(), domain.TopologySnapshot{Catalog: catalog, ObservedAt: now,
		Hosts:       []domain.TopologyHost{{HostID: "control", Address: "control.test", Status: "enabled"}},
		Deployments: []domain.TopologyDeployment{{HostID: "control", ComponentID: "strategy", Status: "enabled"}, {HostID: "control", ComponentID: "trade", Status: "enabled"}},
	}))
	got, err := (Builder{Metrics: query, Topology: repos.Topology, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	byID := map[string]ServiceStatus{}
	for _, service := range got.Services {
		byID[service.ServiceName] = service
	}
	require.NotNil(t, byID["strategy"].Lifecycle)
	require.Equal(t, "paused", byID["strategy"].Lifecycle.Stage)
	require.Equal(t, "system", byID["strategy"].Lifecycle.Pause.Kind)
	require.Equal(t, "stale", byID["trade"].ReporterStatus)
	require.Equal(t, "down", byID["trade"].Status, "已启用的部署停止上报记为 down")
	require.Nil(t, byID["trade"].Lifecycle, "停止上报时不挂旧的生命周期")
}

// 管理端代报的外部程序（因子引擎）不带主机，按组件名对到它唯一的部署上；host-agent 按这台主机最近一次主机快照判断。
func TestDelegatedAndHostSnapshotSources(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, repos := openOverviewState(t, func(db *gorm.DB) {
		require.NoError(t, db.Create(&monmetrics.MetricService{ServiceName: "factor-engine", InstanceID: "engine@mac", BootID: "e1", LastSeenAt: now, Readiness: monmetrics.ReadinessNotReady, NotReadyReason: "因子目录未载入", CreatedAt: now}).Error)
	})
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	require.NoError(t, repos.Topology.Reconcile(t.Context(), domain.TopologySnapshot{Catalog: catalog, ObservedAt: now,
		Hosts:       []domain.TopologyHost{{HostID: "factor", Address: "192.168.0.103", Status: "enabled"}},
		Deployments: []domain.TopologyDeployment{{HostID: "factor", ComponentID: "factor-engine", Status: "enabled"}, {HostID: "factor", ComponentID: "host-agent", Status: "enabled"}},
	}))
	services, unregistered, _, err := (Builder{Metrics: query, Topology: repos.Topology}).buildServices(t.Context(), now,
		[]HostStatus{{HostID: "factor", AgentID: "agent-1", Status: "healthy", LastSeenAt: now}})
	require.NoError(t, err)
	require.Empty(t, unregistered, "代报行不是未登记的进程")
	byID := map[string]ServiceStatus{}
	for _, service := range services {
		byID[service.ServiceName] = service
	}
	engine := byID["factor-engine"]
	require.Equal(t, SourceDelegated, engine.Source)
	require.Equal(t, "down", engine.Status)
	require.Contains(t, engine.Reason, "因子目录未载入")
	require.Zero(t, engine.BootChanges, "只启动过一次")
	agent := byID["host-agent"]
	require.Equal(t, SourceHostSnapshot, agent.Source)
	require.Equal(t, "healthy", agent.Status)
	require.Equal(t, "agent-1", agent.InstanceID)

	services, _, _, err = (Builder{Metrics: query, Topology: repos.Topology}).buildServices(t.Context(), now,
		[]HostStatus{{HostID: "factor", AgentID: "agent-1", Status: "down", LastSeenAt: now.Add(-10 * time.Minute)}})
	require.NoError(t, err)
	for _, service := range services {
		if service.ServiceName == "host-agent" {
			require.Equal(t, "stale", service.ReporterStatus, "主机快照不新鲜即停止上报")
			require.Equal(t, "down", service.Status)
		}
	}
}

// 已启用的部署从没上报或停止上报记为 down（上报新鲜度仍在 ReporterStatus 里）；未启用的部署不算，记为 disabled。
func TestSilentEnabledDeploymentsAreDown(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, repos := openOverviewState(t, func(db *gorm.DB) {
		require.NoError(t, db.Create(&monmetrics.MetricService{ServiceName: "trade", NodeID: "control", InstanceID: "trade@control", BootID: "1", LastSeenAt: now.Add(-10 * time.Minute), Readiness: monmetrics.ReadinessReady}).Error)
		require.NoError(t, db.Create(&monmetrics.MetricService{ServiceName: "monitor", NodeID: "control", InstanceID: "monitor@control", BootID: "1", LastSeenAt: now, Readiness: monmetrics.ReadinessReady}).Error)
	})
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	require.NoError(t, repos.Topology.Reconcile(t.Context(), domain.TopologySnapshot{Catalog: catalog, ObservedAt: now,
		Hosts: []domain.TopologyHost{{HostID: "control", Address: "control.test", Status: "enabled"}},
		Deployments: []domain.TopologyDeployment{
			{HostID: "control", ComponentID: "strategy", Status: "enabled"},
			{HostID: "control", ComponentID: "trade", Status: "enabled"},
			{HostID: "control", ComponentID: "monitor", Status: "enabled"},
			{HostID: "control", ComponentID: "callback-dispatcher", Status: "disabled"},
		},
	}))
	got, err := (Builder{Metrics: query, Topology: repos.Topology, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	byID := map[string]ServiceStatus{}
	for _, service := range got.Services {
		byID[service.ServiceName] = service
	}
	require.Equal(t, [2]string{"missing", "down"}, [2]string{byID["strategy"].ReporterStatus, byID["strategy"].Status}, "从没上报")
	require.Equal(t, [2]string{"stale", "down"}, [2]string{byID["trade"].ReporterStatus, byID["trade"].Status}, "停止上报")
	require.Equal(t, "healthy", byID["monitor"].Status)
	require.Equal(t, [2]string{"missing", "disabled"}, [2]string{byID["callback-dispatcher"].ReporterStatus, byID["callback-dispatcher"].Status}, "未启用的部署不算")
}
