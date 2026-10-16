package bootstrap

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGatewayAlertsFreezeDuringUncertaintyAndRetireWhenHostDisabled(t *testing.T) {
	t.Setenv("MOOX_NOTIFICATION_WEBHOOK_URL", "")
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	repos := manager.Repositories()
	now := time.Now().UTC()
	raw, err := json.Marshal(&adminpb.HostGatewayRuntimeStatus{InstanceId: "new", ConflictInstanceId: "old", ConflictSeenAt: now.Format(time.RFC3339Nano), LastSeenAt: now.Format(time.RFC3339Nano)})
	require.NoError(t, err)
	row := domain.GatewayObservation{HostID: "control", StatusJSON: string(raw), ExpectedHash: "expected", AppliedHash: "expected", HostEnabledAt: now.Add(-time.Hour)}
	require.NoError(t, repos.Gateways.Reconcile(t.Context(), []domain.GatewayObservation{row}, now))
	builder := &observability.Builder{Checks: repos.Checks, Results: repos.Results, Gateways: repos.Gateways, Now: func() time.Time { return now }}
	runtime := &Runtime{Repositories: repos}
	run := buildBusinessFreshnessReporter(builder, repos, monitorResultHook(runtime))
	// 外部检查持续失败满宽限时间才告警
	samples := externalFailureThreshold(30)
	for range samples {
		require.NoError(t, run(t.Context()))
	}
	id := "gateway:control:status" // 三项网关信号合并成一条检查
	state, err := repos.Alerts.GetState(t.Context(), monmetrics.InternalMetricSpaceID, "default:"+id, id)
	require.NoError(t, err)
	require.Equal(t, domain.AlertStatusFiring, state.Status)
	results, err := repos.Results.Recent(t.Context(), monmetrics.InternalMetricSpaceID, id, 10)
	require.NoError(t, err)
	require.Len(t, results, samples)
	require.Contains(t, results[0].RawError, "conflict_instance_id=old")

	failed := domain.GatewayObservation{HostID: "control", ReadError: "GetHostRoutes unavailable", HostEnabledAt: row.HostEnabledAt}
	require.NoError(t, repos.Gateways.Reconcile(t.Context(), []domain.GatewayObservation{failed}, now.Add(time.Second)))
	require.NoError(t, run(t.Context()))
	state, err = repos.Alerts.GetState(t.Context(), monmetrics.InternalMetricSpaceID, "default:"+id, id)
	require.NoError(t, err)
	require.Equal(t, domain.AlertStatusFiring, state.Status, "unknown gateway status cannot resolve an existing conflict")
	results, err = repos.Results.Recent(t.Context(), monmetrics.InternalMetricSpaceID, id, 10)
	require.NoError(t, err)
	require.Len(t, results, samples, "unknown status cannot synthesize a healthy probe")

	require.NoError(t, repos.Gateways.Reconcile(t.Context(), nil, now.Add(2*time.Second)))
	require.NoError(t, run(t.Context()))
	check, err := repos.Checks.Get(t.Context(), monmetrics.InternalMetricSpaceID, id)
	require.NoError(t, err)
	require.False(t, check.Enabled)
	_, err = repos.Alerts.GetRule(t.Context(), monmetrics.InternalMetricSpaceID, "default:"+id)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	row.StatusJSON = `{"instance_id":"replacement","previous_instance_id":"old","last_seen_at":"` + now.Format(time.RFC3339Nano) + `"}`
	require.NoError(t, repos.Gateways.Reconcile(t.Context(), []domain.GatewayObservation{row}, now))
	require.NoError(t, run(t.Context()))
	check, err = repos.Checks.Get(t.Context(), monmetrics.InternalMetricSpaceID, id)
	require.NoError(t, err)
	require.True(t, check.Enabled, "re-enabled hosts must restore gateway checks")
	state, err = repos.Alerts.GetState(t.Context(), monmetrics.InternalMetricSpaceID, "default:"+id, id)
	require.NoError(t, err)
	require.NotEqual(t, domain.AlertStatusFiring, state.Status, "normal replacement is not a conflict")
}

// 旧版本每台主机三条网关检查（心跳、实例冲突、路由同步）合并成一条后，旧检查及其规则必须被停用并删除。
func TestGatewayAlertsMigrateLegacyPerSignalChecks(t *testing.T) {
	t.Setenv("MOOX_NOTIFICATION_WEBHOOK_URL", "")
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	repos := manager.Repositories()
	now := time.Now().UTC()
	for _, kind := range []string{"heartbeat", "instance_conflict", "route_sync"} {
		id := "gateway:control:" + kind
		require.NoError(t, repos.Checks.Create(t.Context(), &domain.Check{SpaceID: monmetrics.InternalMetricSpaceID, CheckID: id, Name: id, GroupName: "business", Kind: domain.CheckKindExternal, Source: domain.CheckSourceObservability, Enabled: true, IntervalSeconds: 30, TimeoutMS: 20000}))
		require.NoError(t, repos.Alerts.CreateRule(t.Context(), &domain.AlertRule{SpaceID: monmetrics.InternalMetricSpaceID, RuleID: "default:" + id, CheckID: id, FailureThreshold: 1, SuccessThreshold: 1, Enabled: true}))
	}
	// 心跳从未上报且路由已同步：合并后的检查按"有异常"处理，原因里带出异常的那一项。
	raw, err := json.Marshal(&adminpb.HostGatewayRuntimeStatus{InstanceId: "new"})
	require.NoError(t, err)
	row := domain.GatewayObservation{HostID: "control", StatusJSON: string(raw), ExpectedHash: "expected", AppliedHash: "expected", HostEnabledAt: now.Add(-time.Hour)}
	require.NoError(t, repos.Gateways.Reconcile(t.Context(), []domain.GatewayObservation{row}, now))
	builder := &observability.Builder{Checks: repos.Checks, Results: repos.Results, Gateways: repos.Gateways, Now: func() time.Time { return now }}
	run := buildBusinessFreshnessReporter(builder, repos, monitorResultHook(&Runtime{Repositories: repos}))
	for range externalFailureThreshold(30) {
		require.NoError(t, run(t.Context()))
	}

	for _, kind := range []string{"heartbeat", "instance_conflict", "route_sync"} {
		id := "gateway:control:" + kind
		check, err := repos.Checks.Get(t.Context(), monmetrics.InternalMetricSpaceID, id)
		require.NoError(t, err)
		require.False(t, check.Enabled, id)
		_, err = repos.Alerts.GetRule(t.Context(), monmetrics.InternalMetricSpaceID, "default:"+id)
		require.ErrorIs(t, err, gorm.ErrRecordNotFound, id)
	}
	merged := "gateway:control:status"
	state, err := repos.Alerts.GetState(t.Context(), monmetrics.InternalMetricSpaceID, "default:"+merged, merged)
	require.NoError(t, err)
	require.Equal(t, domain.AlertStatusFiring, state.Status)
	results, err := repos.Results.Recent(t.Context(), monmetrics.InternalMetricSpaceID, merged, 1)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.False(t, results[0].Success)
	require.Contains(t, results[0].ErrorMessage, "心跳")
}
