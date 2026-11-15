package bootstrap

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// componentHarness 是组件三类检查（停止上报、未就绪、反复重启）的测试台：真实的库、真实的业务评估，观测输入状态可调。
type componentHarness struct {
	t     *testing.T
	db    *gorm.DB
	repos *store.Repositories
	run   func() error
	state observationState
}

func newComponentHarness(t *testing.T, componentID string, rows ...monmetrics.MetricService) *componentHarness {
	t.Helper()
	t.Setenv("MOOX_NOTIFICATION_WEBHOOK_URL", "")
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close() })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	var handle *gorm.DB
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		handle = db
		for i := range rows {
			require.NoError(t, db.Create(&rows[i]).Error)
		}
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	require.NoError(t, err)
	h := &componentHarness{t: t, db: handle, repos: manager.Repositories(), state: observationState{Ready: true}}
	seedBusinessTopology(t, h.repos, "node-a", componentID, true)
	run := buildBusinessFreshnessReporterWithInterval(&monitorobservability.Builder{
		Metrics: query, Checks: h.repos.Checks, Topology: h.repos.Topology, Results: h.repos.Results,
	}, h.repos, nil, func(time.Time) observationState { return h.state }, 30*time.Second)
	h.run = func() error { return run(t.Context()) }
	return h
}

// latest 返回检查最近一次结果；ok 为 false 表示这项检查还没有被判定过。
func (h *componentHarness) latest(checkID string) (domain.CheckResult, bool) {
	results, err := h.repos.Results.Recent(h.t.Context(), events.SystemSpaceID, checkID, 1)
	require.NoError(h.t, err)
	if len(results) == 0 {
		return domain.CheckResult{}, false
	}
	return results[0], true
}

func serviceRow(service string, lastSeen time.Time, readiness, reason string) monmetrics.MetricService {
	return monmetrics.MetricService{ServiceName: service, InstanceID: service + "@node-a", BootID: "boot-a", NodeID: "node-a", LastSeenAt: lastSeen,
		Readiness: readiness, NotReadyReason: reason, CreatedAt: lastSeen.Add(-time.Hour)}
}

// 停止上报：超过无数据时限没有上报的组件判失败；登记了部署却从没上报过的组件同样判失败，不论目录里的功能观测标记。
func TestComponentReporterCheckFailsWhenReportsStop(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	h := newComponentHarness(t, "collector", serviceRow("collector", now.Add(-10*time.Minute), monmetrics.ReadinessReady, ""))
	require.NoError(t, h.run())
	result, ok := h.latest("reporter:node-a:collector")
	require.True(t, ok)
	require.False(t, result.Success)
	require.Equal(t, reporterReasonText("stale"), result.ErrorMessage)
	_, judged := h.latest("ready:node-a:collector")
	require.False(t, judged, "停止上报时未就绪检查保持原状态，由停止上报那一条说明")

	// egress-proxy 在目录里是 not_applicable，以前从没上报过就不告警；现在一视同仁。
	never := newComponentHarness(t, "egress-proxy")
	require.NoError(t, never.run())
	result, ok = never.latest("reporter:node-a:egress-proxy")
	require.True(t, ok)
	require.False(t, result.Success)
	require.Equal(t, reporterReasonText("missing"), result.ErrorMessage)
}

// 未就绪：上报新鲜但报了未就绪，未就绪检查失败并带上组件给出的原因；恢复就绪后通过。停止上报那一条照常通过。
func TestComponentReadyCheckFollowsReportedReadiness(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	h := newComponentHarness(t, "strategy", serviceRow("strategy", now, monmetrics.ReadinessNotReady, "出站 EventBus 未连接"))
	require.NoError(t, h.run())
	result, ok := h.latest("ready:node-a:strategy")
	require.True(t, ok)
	require.False(t, result.Success)
	require.Equal(t, "组件未就绪：出站 EventBus 未连接", result.ErrorMessage)
	reporter, _ := h.latest("reporter:node-a:strategy")
	require.True(t, reporter.Success, "进程仍在上报")
	check, err := h.repos.Checks.Get(t.Context(), events.SystemSpaceID, "ready:node-a:strategy")
	require.NoError(t, err)
	require.Equal(t, "策略服务（node-a 节点）· 就绪", check.Name)

	require.NoError(t, h.db.Exec(`UPDATE t_monitor_metric_services SET c_readiness = 'ready', c_not_ready_reason = ''`).Error)
	require.NoError(t, h.run())
	result, _ = h.latest("ready:node-a:strategy")
	require.True(t, result.Success)
}

// bootRows 造 boots 个启动标识，第一次上报时间都在最近 10 分钟里；withOlder 为真时前面另有一次更早的启动。
func bootRows(service string, now time.Time, boots int, withOlder bool) []monmetrics.MetricService {
	var rows []monmetrics.MetricService
	for i := 0; i < boots; i++ {
		row := serviceRow(service, now.Add(-time.Duration(i)*time.Minute), monmetrics.ReadinessReady, "")
		row.BootID, row.CreatedAt = fmt.Sprintf("boot-%d", i), now.Add(-time.Duration(2*i+1)*time.Minute)
		rows = append(rows, row)
	}
	if withOlder {
		old := serviceRow(service, now.Add(-time.Hour), monmetrics.ReadinessReady, "")
		old.BootID = "boot-old"
		rows = append(rows, old)
	}
	return rows
}

// 反复重启：10 分钟内启动标识变了 3 次判失败。全新部署的第一次启动不算变化：窗口里 3 个启动标识（2 次变化）不报，4 个才报；
// 前面已有更早的启动时，窗口里的 3 个新启动标识就是 3 次变化。
func TestComponentRestartCheckCountsBootChangesInTenMinutes(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		name      string
		boots     int
		withOlder bool
		fail      bool
	}{
		{"已有启动后又重启 3 次", 3, true, true},
		{"已有启动后重启 2 次", 2, true, false},
		{"全新部署启动 3 次", 3, false, false},
		{"全新部署启动 4 次", 4, false, true},
		{"只启动过一次", 1, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newComponentHarness(t, "trade", bootRows("trade", now, tc.boots, tc.withOlder)...)
			require.NoError(t, h.run())
			result, ok := h.latest("restart:node-a:trade")
			require.True(t, ok)
			require.Equal(t, !tc.fail, result.Success, result.ErrorMessage)
			if tc.fail {
				require.Contains(t, result.ErrorMessage, "最近 10 分钟重启了 3 次")
			}
		})
	}
}

// 观测输入中断或刚恢复时，停止上报、未就绪、反复重启三类都冻结：不新增告警，也不恢复已有告警；只有“观测输入”一项说明原因。
func TestComponentChecksFreezeWhileObservationIsSuspended(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	rows := bootRows("collector", now, monmetrics.RestartChanges, true)
	rows[0].LastSeenAt, rows[0].Readiness, rows[0].NotReadyReason = now, monmetrics.ReadinessNotReady, "数据库不可用"
	h := newComponentHarness(t, "collector", rows...)
	h.state = observationState{Suspended: true, Reason: "eventbus unreachable"}
	require.NoError(t, h.run())
	for _, checkID := range []string{"reporter:node-a:collector", "ready:node-a:collector", "restart:node-a:collector"} {
		_, judged := h.latest(checkID)
		require.False(t, judged, "%s 在观测输入中断时冻结", checkID)
	}
	ingest, ok := h.latest(observationIngestCheckID)
	require.True(t, ok)
	require.False(t, ingest.Success)

	h.state = observationState{Suspended: true, Ready: true}
	require.NoError(t, h.run())
	for _, checkID := range []string{"reporter:node-a:collector", "ready:node-a:collector", "restart:node-a:collector"} {
		_, judged := h.latest(checkID)
		require.False(t, judged, "%s 在恢复宽限期内仍冻结", checkID)
	}

	h.state = observationState{Ready: true}
	require.NoError(t, h.run())
	ready, _ := h.latest("ready:node-a:collector")
	require.False(t, ready.Success)
	restart, _ := h.latest("restart:node-a:collector")
	require.False(t, restart.Success)
	reporter, _ := h.latest("reporter:node-a:collector")
	require.True(t, reporter.Success)
}
