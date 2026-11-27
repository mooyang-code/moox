package healthview

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	pb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// moduleReport 是一个模块的一份上报：silent 表示部署了但这段时间没有上报；notReady 非空表示上报了未就绪。
type moduleReport struct {
	id        string
	silent    bool
	notReady  string
	lifecycle *metricspb.ModuleLifecycle
}

func stage(value string) *metricspb.ModuleLifecycle { return &metricspb.ModuleLifecycle{Stage: value} }

// reportedSystem 按真实的数据来源走一遍：各模块的上报入库 → 概览的组件状态 → 首页系统状态（设计 3.8：组件健康与生命周期只看上报）。
// 因子引擎不经 packages/report，由 factor-mgr 在自己的上报里代报。
func reportedSystem(t *testing.T, reports ...moduleReport) *pb.HealthSystem {
	t.Helper()
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mgr.Close()) })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	messages, err := store.WithDatabase(mgr, monmetrics.NewMetricMessageStore)
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Second)
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	topology := domain.TopologySnapshot{Catalog: catalog, ObservedAt: now, Hosts: []domain.TopologyHost{{HostID: "control", Address: "control.test", Status: "enabled"}}}
	var delegated []*metricspb.DelegatedReport
	for i, r := range reports {
		topology.Deployments = append(topology.Deployments, domain.TopologyDeployment{HostID: "control", ComponentID: r.id, Status: "enabled"})
		if r.silent {
			continue
		}
		if r.id == "factor-engine" {
			delegated = append(delegated, &metricspb.DelegatedReport{ServiceName: r.id, InstanceId: "engine@mac", BootId: "engine-boot", LastHeartbeatAt: now.UnixMilli(),
				Ready: proto.Bool(r.notReady == ""), NotReadyReason: r.notReady, Lifecycle: r.lifecycle})
			continue
		}
		_, err := messages.CommitIngest(t.Context(), &eventpb.EventMessage{EventId: fmt.Sprint("report-", i), OccurredAt: now.UnixMilli()},
			&metricspb.MetricReport{ServiceName: r.id, InstanceId: r.id + "@control", NodeId: "control", BootId: "boot", Ready: proto.Bool(r.notReady == ""), NotReadyReason: r.notReady, Lifecycle: r.lifecycle}, nil)
		require.NoError(t, err)
	}
	if delegated != nil {
		topology.Deployments = append(topology.Deployments, domain.TopologyDeployment{HostID: "control", ComponentID: "factor-mgr", Status: "enabled"})
		_, err := messages.CommitIngest(t.Context(), &eventpb.EventMessage{EventId: "factor-mgr", OccurredAt: now.UnixMilli()},
			&metricspb.MetricReport{ServiceName: "factor-mgr", InstanceId: "factor-mgr@control", NodeId: "control", BootId: "boot", Ready: proto.Bool(true), Delegated: delegated}, nil)
		require.NoError(t, err)
	}
	repos := mgr.Repositories()
	require.NoError(t, repos.Topology.Reconcile(t.Context(), topology))
	facts, err := (&observability.Builder{Metrics: monmetrics.NewQueryService(messages, nil), Topology: repos.Topology, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	return projectSystem(projectFacts(facts).Components)
}

func TestProjectSystemRunning(t *testing.T) {
	require.Nil(t, reportedSystem(t, moduleReport{id: "storage-primary"}), "没有模块上报生命周期时不输出系统状态")

	running := reportedSystem(t, moduleReport{id: "factor-engine", lifecycle: stage("running")}, moduleReport{id: "strategy", lifecycle: stage("running")}, moduleReport{id: "collector", lifecycle: stage("running")})
	require.Equal(t, "running", running.Phase)
	require.Equal(t, "green", running.Color)
	require.Equal(t, "healthy", running.Health)
	require.Empty(t, running.Issues)
	require.Nil(t, running.PauseKinds)
	require.Equal(t, int32(3), running.ModuleCount)
}

// 有生命周期接口的模块停止上报：不能从系统汇总里消失，健康面标红并列出；它最后一次上报的生命周期不再参与汇总。
func TestProjectSystemReportsSilentLifecycleModule(t *testing.T) {
	system := reportedSystem(t, moduleReport{id: "factor-engine", lifecycle: stage("running")}, moduleReport{id: "strategy", silent: true}, moduleReport{id: "storage-primary", silent: true})
	require.Equal(t, "attention", system.Health)
	require.Equal(t, "red", system.Color)
	require.Equal(t, "running", system.Phase)
	require.Equal(t, []string{"策略服务：停止上报"}, system.Issues, "没有生命周期接口的组件不在此列")
	require.Equal(t, int32(1), system.SilentModules)
	require.Equal(t, int32(2), system.ModuleCount)

	only := reportedSystem(t, moduleReport{id: "strategy", silent: true})
	require.Equal(t, "attention", only.Health)
	require.Equal(t, "unknown", only.Phase, "一个模块都读不到时运行阶段未知")
}

// 有模块未就绪：Strategy 自检不过但进程仍在上报，首页标红并写明原因；它的运行阶段照常参与汇总。
func TestProjectSystemReportsNotReadyModule(t *testing.T) {
	system := reportedSystem(t,
		moduleReport{id: "strategy", notReady: "出站 EventBus 未连接", lifecycle: stage("running")},
		moduleReport{id: "collector", lifecycle: stage("running")},
		moduleReport{id: "factor-engine", notReady: "因子目录未载入", lifecycle: stage("running")},
	)
	require.Equal(t, "attention", system.Health)
	require.Equal(t, "red", system.Color)
	require.Equal(t, "running", system.Phase)
	require.Equal(t, int32(2), system.NotReadyModules)
	require.Zero(t, system.SilentModules)
	require.Equal(t, []string{"因子引擎：未就绪（因子目录未载入）", "策略服务：未就绪（出站 EventBus 未连接）"}, system.Issues, "管理端代报的因子引擎同样计入")
}

// 系统暂停覆盖的模块全部暂停是 paused，部分暂停是 partially_paused；不参与系统暂停的模块不影响“全部”的判断。
func TestProjectSystemPausePhases(t *testing.T) {
	paused := func(kind string) *metricspb.ModuleLifecycle {
		reason := map[string]string{"system": "系统暂停：切换行情源", "manual": "手动暂停：排查"}[kind]
		return &metricspb.ModuleLifecycle{Stage: "paused", Reason: reason, PauseKind: kind, PausedSince: time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC).UnixMilli()}
	}
	all := reportedSystem(t, moduleReport{id: "factor-engine", lifecycle: paused("system")}, moduleReport{id: "strategy", lifecycle: paused("system")},
		moduleReport{id: "collector", lifecycle: paused("system")}, moduleReport{id: "trade", lifecycle: paused("system")}, moduleReport{id: "archive-mgr", lifecycle: stage("running")})
	require.Equal(t, "paused", all.Phase)
	require.Equal(t, "orange", all.Color)
	require.Equal(t, "healthy", all.Health)
	require.Equal(t, int32(4), all.PauseKinds["system"])
	require.Equal(t, time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC).UnixMilli(), all.PausedSince)
	require.Contains(t, all.Issues, "策略服务：系统暂停：切换行情源")

	partial := reportedSystem(t, moduleReport{id: "factor-engine", lifecycle: stage("running")}, moduleReport{id: "strategy", lifecycle: paused("manual")})
	require.Equal(t, "partially_paused", partial.Phase)
	require.Equal(t, "orange", partial.Color)
	require.Equal(t, int32(1), partial.PauseKinds["manual"])
	require.Equal(t, []string{"策略服务：手动暂停：排查"}, partial.Issues)

	// 只有不参与系统暂停的模块（例如 Monitor 的告警屏蔽）暂停：不算系统部分暂停，但列出来。
	muted := reportedSystem(t, moduleReport{id: "factor-engine", lifecycle: stage("running")}, moduleReport{id: "monitor", lifecycle: &metricspb.ModuleLifecycle{Stage: "paused", Reason: "手动暂停：告警屏蔽", PauseKind: "manual"}})
	require.Equal(t, "running", muted.Phase)
	require.Equal(t, []string{"监控服务：手动暂停：告警屏蔽"}, muted.Issues)

	// 部分系统可暂停模块崩溃停报、其余在报模块已暂停：不能误判为全系统 "paused"，应为 "partially_paused" 且健康标红。
	oneSilent := reportedSystem(t,
		moduleReport{id: "collector", lifecycle: paused("system")},
		moduleReport{id: "factor-engine", lifecycle: paused("system")},
		moduleReport{id: "strategy", lifecycle: paused("system")},
		moduleReport{id: "trade", silent: true},
	)
	require.Equal(t, "partially_paused", oneSilent.Phase)
	require.Equal(t, "attention", oneSilent.Health)
	require.Equal(t, "red", oneSilent.Color)
	require.Equal(t, int32(1), oneSilent.SilentModules)
	require.Contains(t, oneSilent.Issues, "交易服务：停止上报")

	mostSilent := reportedSystem(t,
		moduleReport{id: "collector", silent: true},
		moduleReport{id: "factor-engine", silent: true},
		moduleReport{id: "strategy", lifecycle: paused("manual")},
		moduleReport{id: "trade", silent: true},
	)
	require.Equal(t, "partially_paused", mostSilent.Phase)
	require.Equal(t, "attention", mostSilent.Health)
	require.Equal(t, "red", mostSilent.Color)
	require.Equal(t, int32(3), mostSilent.SilentModules)

	// 全部系统可暂停模块都已确认暂停，仅非可暂停模块（Monitor）停报：阶段仍为 "paused"，健康标红。
	nonPausableSilent := reportedSystem(t,
		moduleReport{id: "collector", lifecycle: paused("system")},
		moduleReport{id: "factor-engine", lifecycle: paused("system")},
		moduleReport{id: "strategy", lifecycle: paused("system")},
		moduleReport{id: "trade", lifecycle: paused("system")},
		moduleReport{id: "monitor", silent: true},
	)
	require.Equal(t, "paused", nonPausableSilent.Phase)
	require.Equal(t, "attention", nonPausableSilent.Health)
	require.Equal(t, "red", nonPausableSilent.Color)
	require.Equal(t, int32(1), nonPausableSilent.SilentModules)

	// 已停用（disabled）的可暂停部署不计入分母：其余已启用可暂停模块全部暂停时仍为 "paused"。
	disabledExcluded := projectSystem([]*pb.HealthComponent{
		{ComponentId: "collector", Name: "行情采集服务", Status: "healthy", Reporter: &pb.HealthSignal{Status: "healthy"}, Lifecycle: &commonpb.LifecycleSnapshot{Stage: "paused", Pause: &commonpb.LifecyclePause{Kind: "system"}}},
		{ComponentId: "strategy", Name: "策略服务", Status: "healthy", Reporter: &pb.HealthSignal{Status: "healthy"}, Lifecycle: &commonpb.LifecycleSnapshot{Stage: "paused", Pause: &commonpb.LifecyclePause{Kind: "system"}}},
		{ComponentId: "trade", Name: "交易服务", Status: "disabled", Reporter: &pb.HealthSignal{Status: "unknown"}},
	})
	require.Equal(t, "paused", disabledExcluded.Phase)
	require.Equal(t, "healthy", disabledExcluded.Health)
	require.Equal(t, "orange", disabledExcluded.Color)
}

// 恢复追赶：显示追赶中的模块数与已追平（待放行）的模块数，以及各模块的进度文字。
func TestProjectSystemResumingAndCaughtUp(t *testing.T) {
	resuming := func(catchup *commonpb.CatchupProgress) *metricspb.ModuleLifecycle {
		return &metricspb.ModuleLifecycle{Stage: "resuming", PauseKind: "system", Catchup: catchup}
	}
	system := reportedSystem(t,
		moduleReport{id: "collector", lifecycle: resuming(&commonpb.CatchupProgress{Done: 6, Total: 10})},
		moduleReport{id: "strategy", lifecycle: resuming(&commonpb.CatchupProgress{BlockedBy: "实例 i1"})},
		moduleReport{id: "trade", lifecycle: resuming(&commonpb.CatchupProgress{CaughtUp: true})},
	)
	require.Equal(t, "resuming", system.Phase)
	require.Equal(t, "cyan", system.Color)
	require.Equal(t, int32(3), system.ResumingModules)
	require.Equal(t, int32(1), system.CaughtUpModules)
	require.ElementsMatch(t, []string{"行情采集服务：补采 6/10", "策略服务：被 实例 i1 阻塞", "交易服务：已追平，待放行"}, system.CatchupNotes)

	// 全部追平、等待放行。
	caught := reportedSystem(t, moduleReport{id: "factor-engine", lifecycle: resuming(&commonpb.CatchupProgress{CaughtUp: true})}, moduleReport{id: "collector", lifecycle: stage("running")})
	require.Equal(t, "resuming", caught.Phase)
	require.Equal(t, caught.ResumingModules, caught.CaughtUpModules)
	require.Equal(t, []string{"因子引擎：已追平，待放行"}, caught.CatchupNotes)
}

// 待生效（上游历史补齐中）的数据集：状态 disabled、原因翻成中文，不计入需关注。
func TestProjectDatasetHistorySeedingIsPendingNotAttention(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	facts := observability.Overview{GeneratedAt: now, Datasets: []observability.DatasetFrequencyStatus{{
		Producer: "strategy", SpaceID: "stockus", DatasetID: "inst_a", Freq: "5m", DisplayName: "均线（a）",
		Status: "disabled", Reason: observability.DatasetReasonHistorySeeding, LastReportedAt: now,
	}}}
	dataset := projectDataset(facts.Datasets[0])
	require.Equal(t, "待生效（上游历史补齐中）", dataset.Reason)
	require.Equal(t, "disabled", string(dataset.Status))
	require.Empty(t, dataset.RawError)
	summary := &pb.HealthSummary{}
	countSummary(summary, string(dataset.Status))
	require.Zero(t, summary.AttentionCount)
}
