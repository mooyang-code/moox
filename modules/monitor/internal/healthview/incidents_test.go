package healthview

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	pb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/stretchr/testify/require"
)

func openRepositories(t *testing.T) *store.Repositories {
	t.Helper()
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	return manager.Repositories()
}

func TestIncidentsPairTriggerAndRecoveryWithinWindow(t *testing.T) {
	repos := openRepositories(t)
	ctx := t.Context()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	seq := 0
	event := func(space, check, eventType string, at time.Time, payload string) {
		seq++
		if payload == "" {
			payload = "{}"
		}
		require.NoError(t, repos.Alerts.CreateEvent(ctx, &domain.AlertEvent{EventID: string(rune('a' + seq)), SpaceID: space, RuleID: "default:" + check, CheckID: check, EventType: eventType, Status: "firing", Payload: payload, CreatedAt: at}))
	}
	reporter, strategy, webHost, fetch := "reporter:control:collector", "dataset:strategy:s1:1m", "placement:control:web-host", "market_fetch:scf_timer"
	presence := hostmetrics.PresenceCheckID("AGENT01")
	require.NoError(t, repos.Checks.Create(ctx, &domain.Check{SpaceID: "mooxsys", CheckID: reporter, Name: "行情采集服务（control 节点）· 运行指标上报", Kind: domain.CheckKindExternal, Enabled: true}))
	require.NoError(t, repos.Checks.Create(ctx, &domain.Check{CheckID: webHost, Name: "控制台前端（control）· 健康检查", Kind: domain.CheckKindHTTP, Enabled: true}))

	// 窗口内触发、通知失败、29 秒后恢复
	event("mooxsys", reporter, domain.AlertEventTriggered, now.Add(-10*time.Hour), `{"reason":"运行指标已中断"}`)
	event("mooxsys", reporter, domain.AlertEventSendFailed, now.Add(-10*time.Hour+time.Second), "")
	event("mooxsys", reporter, domain.AlertEventResolved, now.Add(-10*time.Hour+29*time.Second), "")
	// 触发早于窗口：从窗口外最近一次触发补齐时间和原因
	event("crypto", strategy, domain.AlertEventTriggered, now.Add(-50*time.Hour), `{"reason":"一直运行失败"}`)
	event("crypto", strategy, domain.AlertEventReminder, now.Add(-47*time.Hour), "")
	event("crypto", strategy, domain.AlertEventReminder, now.Add(-46*time.Hour), "")
	event("crypto", strategy, domain.AlertEventResolved, now.Add(-45*time.Hour), "")
	// 仍在告警
	event("", webHost, domain.AlertEventTriggered, now.Add(-time.Hour), `{"reason":"健康检查失败"}`)
	require.NoError(t, repos.Alerts.CreateRule(ctx, &domain.AlertRule{RuleID: "default:" + webHost, CheckID: webHost, Enabled: true}))
	require.NoError(t, repos.Alerts.UpsertState(ctx, &domain.AlertState{RuleID: "default:" + webHost, CheckID: webHost, Status: domain.AlertStatusFiring, DedupeKey: webHost, TriggeredAt: ptr(now.Add(-time.Hour)), UpdatedAt: now}))
	// 检查停用后告警被撤销，没有恢复记录
	event("crypto", fetch, domain.AlertEventTriggered, now.Add(-2*time.Hour), "")
	// 主机失联与恢复
	event(hostmetrics.SpaceID, presence, domain.AlertEventTriggered, now.Add(-3*time.Hour), `{"reason":"主机采集代理停止上报"}`)
	event(hostmetrics.SpaceID, presence, domain.AlertEventResolved, now.Add(-3*time.Hour+10*time.Minute), "")
	// 完全在窗口外
	event("crypto", "dataset:factor:f1:1m", domain.AlertEventTriggered, now.Add(-60*time.Hour), "")
	event("crypto", "dataset:factor:f1:1m", domain.AlertEventResolved, now.Add(-59*time.Hour), "")

	view, err := (Builder{Checks: repos.Checks, Results: repos.Results, Alerts: repos.Alerts, Now: func() time.Time { return now }}).Build(ctx, "")
	require.NoError(t, err)
	require.Equal(t, int32(48), view.IncidentWindowHours)
	require.Len(t, view.Incidents, 5)
	byCheck := make(map[string]*pb.AlertIncident)
	var order []string
	for _, item := range view.Incidents {
		byCheck[item.Object.Kind] = item
		order = append(order, item.Object.Kind)
	}
	require.Equal(t, []string{"placement", "market_fetch", "presence", "reporter", "dataset"}, order, "newest trigger first")

	item := byCheck["placement"]
	require.True(t, item.Active)
	require.Empty(t, item.ResolvedAt)
	require.Equal(t, "控制台前端（control）· 健康检查", item.Title)
	require.Equal(t, "critical", item.Severity)

	item = byCheck["market_fetch"]
	require.False(t, item.Active, "revoked alerts are not ongoing")
	require.Empty(t, item.ResolvedAt)

	item = byCheck["presence"]
	require.Equal(t, "主机 AGENT01 · 失联", item.Title)
	require.Equal(t, "主机采集代理停止上报", item.Reason)
	require.Equal(t, stamp(now.Add(-3*time.Hour+10*time.Minute)), item.ResolvedAt)

	item = byCheck["reporter"]
	require.Equal(t, "行情采集服务（control 节点）· 运行指标上报", item.Title)
	require.Equal(t, "运行指标已中断", item.Reason)
	require.Equal(t, "warning", item.Severity)
	require.True(t, item.NotifyFailed)
	require.Equal(t, stamp(now.Add(-10*time.Hour+29*time.Second)), item.ResolvedAt)

	item = byCheck["dataset"]
	require.Equal(t, stamp(now.Add(-50*time.Hour)), item.TriggeredAt, "trigger before the window is restored")
	require.Equal(t, "一直运行失败", item.Reason)
	require.Equal(t, int32(2), item.ReminderCount)
	require.False(t, item.Active)

	scoped, err := (Builder{Checks: repos.Checks, Alerts: repos.Alerts, Now: func() time.Time { return now }}).Build(ctx, "stockcn")
	require.NoError(t, err)
	for _, incident := range scoped.Incidents {
		require.NotEqual(t, "crypto", incident.Object.SpaceId, "other spaces stay out of a selected space")
	}
	require.Len(t, scoped.Incidents, 3)
}

// 恢复通知发送失败时评估器已写入恢复事件、告警保持进行中：之后的发送失败、重试恢复或继续提醒都属于同一条经过。
func TestIncidentsKeepFailedRecoveryNotificationInOneIncident(t *testing.T) {
	repos := openRepositories(t)
	ctx := t.Context()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	seq := 0
	event := func(check, eventType string, at time.Time) {
		seq++
		require.NoError(t, repos.Alerts.CreateEvent(ctx, &domain.AlertEvent{EventID: fmt.Sprintf("e%d", seq), SpaceID: "crypto", RuleID: "default:" + check, CheckID: check, EventType: eventType, Status: "firing", Payload: "{}", CreatedAt: at}))
	}
	retried, refailed := "market_fetch:scf_timer", "market_canary:btc"
	event(retried, domain.AlertEventTriggered, now.Add(-30*time.Minute))
	event(retried, domain.AlertEventResolved, now.Add(-29*time.Minute))
	event(retried, domain.AlertEventSendFailed, now.Add(-29*time.Minute))
	event(retried, domain.AlertEventResolved, now.Add(-28*time.Minute))
	event(refailed, domain.AlertEventTriggered, now.Add(-20*time.Minute))
	event(refailed, domain.AlertEventResolved, now.Add(-19*time.Minute))
	event(refailed, domain.AlertEventSendFailed, now.Add(-19*time.Minute))
	event(refailed, domain.AlertEventReminder, now.Add(-14*time.Minute))
	require.NoError(t, repos.Alerts.CreateRule(ctx, &domain.AlertRule{SpaceID: "crypto", RuleID: "default:" + refailed, CheckID: refailed, Enabled: true}))
	require.NoError(t, repos.Checks.Create(ctx, &domain.Check{SpaceID: "crypto", CheckID: refailed, Name: "行情探针", Kind: domain.CheckKindExternal, Enabled: true}))
	require.NoError(t, repos.Alerts.UpsertState(ctx, &domain.AlertState{SpaceID: "crypto", RuleID: "default:" + refailed, CheckID: refailed, Status: domain.AlertStatusFiring, DedupeKey: refailed, TriggeredAt: ptr(now.Add(-20 * time.Minute)), UpdatedAt: now}))

	view, err := (Builder{Checks: repos.Checks, Alerts: repos.Alerts, Now: func() time.Time { return now }}).Build(ctx, "")
	require.NoError(t, err)
	require.Len(t, view.Incidents, 2)
	refailedIncident, retriedIncident := view.Incidents[0], view.Incidents[1]
	require.True(t, retriedIncident.NotifyFailed)
	require.Equal(t, stamp(now.Add(-28*time.Minute)), retriedIncident.ResolvedAt, "the retried recovery closes the same incident")
	require.Equal(t, stamp(now.Add(-30*time.Minute)), retriedIncident.TriggeredAt)
	require.False(t, retriedIncident.Active)
	require.True(t, refailedIncident.Active, "a reminder after a failed recovery notification reopens the incident")
	require.Empty(t, refailedIncident.ResolvedAt)
	require.Equal(t, int32(1), refailedIncident.ReminderCount)
}

// 事件数超过上限时只保留最新的；被截掉的窗口内触发要从已读到的最早事件往前补查。
func TestIncidentsRecoverTriggerCutByEventLimit(t *testing.T) {
	repos := openRepositories(t)
	ctx := t.Context()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	limit := maxIncidentEvents
	maxIncidentEvents = 3
	t.Cleanup(func() { maxIncidentEvents = limit })
	check := "dataset:strategy:s1:1m"
	require.NoError(t, repos.Alerts.CreateEvent(ctx, &domain.AlertEvent{EventID: "trigger", SpaceID: "crypto", RuleID: "default:" + check, CheckID: check, EventType: domain.AlertEventTriggered, Payload: `{"reason":"一直运行失败"}`, CreatedAt: now.Add(-10 * time.Hour)}))
	for i := range 5 {
		require.NoError(t, repos.Alerts.CreateEvent(ctx, &domain.AlertEvent{EventID: fmt.Sprintf("reminder-%d", i), SpaceID: "crypto", RuleID: "default:" + check, CheckID: check, EventType: domain.AlertEventReminder, Payload: "{}", CreatedAt: now.Add(time.Duration(i-5) * time.Hour)}))
	}
	view, err := (Builder{Alerts: repos.Alerts, Now: func() time.Time { return now }}).Build(ctx, "")
	require.NoError(t, err)
	require.Len(t, view.Incidents, 1)
	require.Equal(t, stamp(now.Add(-10*time.Hour)), view.Incidents[0].TriggeredAt)
	require.Equal(t, "一直运行失败", view.Incidents[0].Reason)
}

func TestHostThresholdsComeFromEnabledDefaultRules(t *testing.T) {
	repos := openRepositories(t)
	ctx := t.Context()
	for metric, definition := range map[string]string{hostmetrics.HostMetricCPU: `{"threshold":80,"recovery_threshold":70}`, hostmetrics.HostMetricMemory: `{"threshold":90}`, hostmetrics.HostMetricFilesystemUsage: `{"threshold":85}`} {
		checkID := hostmetrics.HostRuleKey("AGENT01", metric)
		require.NoError(t, repos.Alerts.CreateRule(ctx, &domain.AlertRule{SpaceID: hostmetrics.SpaceID, RuleID: "default:" + checkID, CheckID: checkID, Description: definition, Enabled: metric != hostmetrics.HostMetricMemory}))
	}
	hosts := []*pb.HealthHost{{HostId: "control", AgentId: "AGENT01"}, {HostId: "storage"}}
	require.NoError(t, (Builder{Alerts: repos.Alerts}).decorateHostThresholds(ctx, hosts))
	require.Equal(t, 80.0, hosts[0].CpuThreshold)
	require.Zero(t, hosts[0].MemoryThreshold, "disabled rules draw no threshold")
	require.Equal(t, 85.0, hosts[0].DiskThreshold)
	require.Zero(t, hosts[1].CpuThreshold)
}

func TestDatasetAlertCarriesDatasetFactsInsteadOfConcatenatedReason(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	facts := observability.Overview{GeneratedAt: now, Datasets: []observability.DatasetFrequencyStatus{{Producer: "factor", SpaceID: "crypto", DatasetID: "dataset_factor_x_1m", Freq: "1m", Status: "stale", Reason: "run stale", LastSuccessAt: now.Add(-time.Hour), OutputWatermarkAt: now.Add(-time.Hour), WaitingBars: 59, WaitingSince: now.Add(-58 * time.Minute), OverdueSeconds: 3480, LatenessLimitSeconds: 600}}}
	checkID := "dataset:factor:dataset_factor_x_1m:1m"
	alert, err := (Builder{}).projectAlert(t.Context(), domain.AlertState{SpaceID: "crypto", CheckID: checkID, DedupeKey: checkID, TriggeredAt: &now, UpdatedAt: now}, facts, newCheckTitles(nil))
	require.NoError(t, err)
	require.Equal(t, "任务运行结果已过期", alert.Reason)
	require.NotNil(t, alert.Dataset)
	require.Equal(t, int64(600), alert.Dataset.LatenessLimitSeconds)
	require.Equal(t, int64(3480), alert.Dataset.OverdueSeconds)
	require.EqualValues(t, 59, alert.Dataset.WaitingBars)
	require.Equal(t, "dataset_factor_x_1m · 1m", alert.Title)
	require.Equal(t, "critical", alert.Severity)
}

func ptr[T any](value T) *T { return &value }
