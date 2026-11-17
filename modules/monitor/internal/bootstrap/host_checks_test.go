package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/monitor/internal/alerting"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/healthview"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	pb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/notification"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type capturedSender struct {
	mu       sync.Mutex
	messages []notification.Message
}

func (s *capturedSender) Send(_ context.Context, message notification.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, message)
	return nil
}

// forCheck 返回某项检查推送过的消息（推送的 Key 是 规则ID:检查ID）。
func (s *capturedSender) forCheck(checkID string) []notification.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []notification.Message
	for _, message := range s.messages {
		if message.Key == "default:"+checkID+":"+checkID {
			out = append(out, message)
		}
	}
	return out
}

// hostCheckHarness 把主机登记、评估（写检查结果、告警评估、推送）和页面概览接在同一个库上，时间由 clock 控制。
type hostCheckHarness struct {
	t         *testing.T
	repos     *store.Repositories
	hosts     *hostmetrics.Store
	sender    *capturedSender
	clock     time.Time
	suspended bool
	// hostRun 是独立先跑的主机类检查，businessRun 是业务评估；run 按线上的顺序各跑一遍。
	hostRun     func(context.Context) error
	businessRun func(context.Context) error
	run         func(context.Context) error
	page        healthview.Builder
	sequence    int
}

func newHostCheckHarness(t *testing.T) *hostCheckHarness {
	t.Helper()
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	h := &hostCheckHarness{t: t, repos: manager.Repositories(), sender: &capturedSender{}, clock: time.Date(2026, 11, 14, 4, 0, 0, 0, time.UTC)}
	samples, err := store.WithDatabase(manager, hostmetrics.NewSampleStore)
	require.NoError(t, err)
	registry, err := store.WithDatabase(manager, hostmetrics.NewRegistry)
	require.NoError(t, err)
	h.hosts = hostmetrics.NewStore(samples)
	h.hosts.SetRegistry(registry)

	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	err = h.repos.Topology.Reconcile(t.Context(), domain.TopologySnapshot{Catalog: catalog, ObservedAt: h.clock,
		Hosts:       []domain.TopologyHost{{HostID: "storage", Address: "storage.example.test", Status: servicecatalog.Enabled}},
		Deployments: []domain.TopologyDeployment{{HostID: "storage", ComponentID: "host-agent", Status: servicecatalog.Enabled}}})
	require.NoError(t, err)

	now := func() time.Time { return h.clock }
	facts := &observability.Builder{Hosts: h.hosts, Checks: h.repos.Checks, Results: h.repos.Results, Topology: h.repos.Topology, Gateways: h.repos.Gateways, Now: now}
	evaluator := alerting.NewEvaluator(h.repos.Alerts, alerting.Options{Sender: func(context.Context) (notification.Sender, error) { return h.sender, nil }})
	hook := func(ctx context.Context, check domain.Check, result domain.CheckResult) {
		require.NoError(t, evaluator.Evaluate(ctx, check, result))
	}
	observation := func(time.Time) observationState { return observationState{Ready: true, Suspended: h.suspended} }
	h.hostRun = buildHostCheckReporter(facts, h.repos, hook, observation)
	h.businessRun = buildBusinessFreshnessReporterWithInterval(facts, h.repos, hook, observation, 30*time.Second)
	h.run = func(ctx context.Context) error {
		if err := h.hostRun(ctx); err != nil {
			return err
		}
		return h.businessRun(ctx)
	}
	h.page = healthview.Builder{Facts: facts, Checks: h.repos.Checks, Results: h.repos.Results, Alerts: h.repos.Alerts, Now: now}
	return h
}

// report 模拟采集器 agentID 在当前时刻上报一份快照。
func (h *hostCheckHarness) report(agentID string, cpu float64) {
	h.t.Helper()
	h.sequence++
	metric := &hostmetricpb.HostMetric{HostId: "storage", AgentId: agentID, Hostname: "VM-0-9", BootId: "boot", Snapshot: &hostmetricpb.HostSnapshot{
		Cpu:    &hostmetricpb.CpuMetric{LogicalCores: 4, UsageAvailable: true, UsagePercent: cpu},
		Memory: &hostmetricpb.MemoryMetric{TotalBytes: 100, UsedBytes: 50, AvailableBytes: 50, UsagePercent: 50},
		TopProcesses: []*hostmetricpb.ProcessMetric{
			{Pid: 11, Name: "storage-primary", CpuPercent: 340, CpuAvailable: true, RssBytes: 5 << 30},
			{Pid: 12, Name: "duckdb", CpuPercent: 45, CpuAvailable: true, RssBytes: 7 << 30},
		},
	}}
	message := &eventpb.EventMessage{EventId: fmt.Sprintf("event-%d", h.sequence), OccurredAt: timestamppb.New(h.clock)}
	require.NoError(h.t, h.hosts.Persist(h.t.Context(), message, metric))
}

// gateway 写入主机网关的心跳：alive 为 false 时最近一次心跳停在 5 分钟前。
func (h *hostCheckHarness) gateway(alive bool) {
	h.t.Helper()
	seen := h.clock
	if !alive {
		seen = h.clock.Add(-5 * time.Minute)
	}
	raw, err := json.Marshal(&adminpb.HostGatewayRuntimeStatus{InstanceId: "gw", LastSeenAt: seen.Format(time.RFC3339Nano)})
	require.NoError(h.t, err)
	row := domain.GatewayObservation{HostID: "storage", StatusJSON: string(raw), ExpectedHash: "hash", AppliedHash: "hash", HostEnabledAt: h.clock.Add(-time.Hour)}
	require.NoError(h.t, h.repos.Gateways.Reconcile(h.t.Context(), []domain.GatewayObservation{row}, h.clock))
}

func (h *hostCheckHarness) advance(d time.Duration) { h.clock = h.clock.Add(d) }

func (h *hostCheckHarness) evaluate() {
	h.t.Helper()
	require.NoError(h.t, h.run(h.t.Context()))
}

func (h *hostCheckHarness) overview() *healthview.Overview {
	h.t.Helper()
	view, err := h.page.Build(h.t.Context(), "")
	require.NoError(h.t, err)
	return view
}

// pageAlert 返回页面上某项主机类检查的当前告警；没有时返回 nil。
func (h *hostCheckHarness) pageAlert(kind string) *pb.HealthAlert {
	h.t.Helper()
	for _, alert := range h.overview().Alerts {
		if alert.Object.Type == "host" && alert.Object.Kind == kind {
			return alert
		}
	}
	return nil
}

// requireSameAsPush 断言页面上的告警与推送出去的新告警是同一个标题、同一个级别、同一段原因。
func requireSameAsPush(t *testing.T, alert *pb.HealthAlert, pushed notification.Message) {
	t.Helper()
	require.NotNil(t, alert, "推送过的告警在页面上也要有")
	require.Equal(t, pushed.Title, alert.Title)
	require.Equal(t, string(pushed.Severity), alert.Severity)
	require.Contains(t, pushed.Body, "状态：新告警\n问题："+alert.Reason+"\n")
}

// 同一次主机失联（采集器停报、网关心跳也停了）：页面与推送是同一个标题、级别和原因；恢复后页面告警消失并推送恢复。
func TestHostLostShowsTheSameAlertOnPageAndPush(t *testing.T) {
	h := newHostCheckHarness(t)
	presence := hostmetrics.CheckID("AGENT01", hostmetrics.CheckKindPresence)
	h.gateway(true)
	h.report("AGENT01", 20)
	h.evaluate()
	require.Empty(t, h.sender.forCheck(presence))
	require.Nil(t, h.pageAlert(hostmetrics.CheckKindPresence))

	// 停报不满 3 分钟不算失联。
	h.advance(2*time.Minute + 50*time.Second)
	h.gateway(false)
	h.evaluate()
	require.Empty(t, h.sender.forCheck(presence))

	h.advance(20 * time.Second)
	h.gateway(false)
	h.evaluate()
	pushed := h.sender.forCheck(presence)
	require.Len(t, pushed, 1, "停报满 3 分钟后的第一轮评估就告警，不再另等 60 秒")
	require.Equal(t, "主机 storage · 失联", pushed[0].Title)
	require.Equal(t, notification.SeverityCritical, pushed[0].Severity)
	require.Contains(t, pushed[0].Body, "主机网关心跳也已停止")
	alert := h.pageAlert(hostmetrics.CheckKindPresence)
	requireSameAsPush(t, alert, pushed[0])
	require.Equal(t, "storage", alert.Object.HostId)
	require.Nil(t, h.pageAlert(hostmetrics.CheckKindAgent), "主机失联时不再另报 host-agent 停止上报")
	require.Empty(t, h.sender.forCheck(hostmetrics.CheckID("AGENT01", hostmetrics.CheckKindAgent)))
	host := h.overview().Hosts[0]
	require.Equal(t, "down", host.Status)
	require.Equal(t, alert.Reason, host.Reason, "主机卡片与告警是同一段原因")

	h.advance(time.Minute)
	h.gateway(true)
	h.report("AGENT01", 20)
	h.evaluate()
	pushed = h.sender.forCheck(presence)
	require.Len(t, pushed, 2)
	require.Equal(t, notification.SeverityInfo, pushed[1].Severity)
	require.Equal(t, "主机 storage · 失联", pushed[1].Title)
	require.Contains(t, pushed[1].Body, "状态：已恢复")
	require.Nil(t, h.pageAlert(hostmetrics.CheckKindPresence))
	require.Equal(t, "healthy", h.overview().Hosts[0].Status)
}

// 采集器停报但主机网关心跳正常：主机在线，页面与推送都是警告级的“host-agent 停止上报”，不是“失联”。
// 之后网关心跳也停了，改报主机失联。
func TestAgentOnlyStopIsAWarningOnPageAndPush(t *testing.T) {
	h := newHostCheckHarness(t)
	presence := hostmetrics.CheckID("AGENT01", hostmetrics.CheckKindPresence)
	agent := hostmetrics.CheckID("AGENT01", hostmetrics.CheckKindAgent)
	h.gateway(true)
	h.report("AGENT01", 20)
	h.evaluate()

	h.advance(3*time.Minute + 10*time.Second)
	h.gateway(true)
	h.evaluate()
	pushed := h.sender.forCheck(agent)
	require.Len(t, pushed, 1)
	require.Equal(t, "主机 storage · host-agent 停止上报", pushed[0].Title)
	require.Equal(t, notification.SeverityWarning, pushed[0].Severity)
	require.Contains(t, pushed[0].Body, "主机网关心跳正常")
	requireSameAsPush(t, h.pageAlert(hostmetrics.CheckKindAgent), pushed[0])
	require.Empty(t, h.sender.forCheck(presence))
	require.Nil(t, h.pageAlert(hostmetrics.CheckKindPresence))
	host := h.overview().Hosts[0]
	require.Equal(t, "degraded", host.Status, "主机仍在线，主机卡片显示需关注而不是异常")
	require.Equal(t, h.pageAlert(hostmetrics.CheckKindAgent).Reason, host.Reason)

	h.advance(5 * time.Minute)
	h.gateway(false)
	h.evaluate()
	lost := h.sender.forCheck(presence)
	require.Len(t, lost, 1)
	require.Equal(t, notification.SeverityCritical, lost[0].Severity)
	requireSameAsPush(t, h.pageAlert(hostmetrics.CheckKindPresence), lost[0])
	pushed = h.sender.forCheck(agent)
	require.Len(t, pushed, 2)
	require.Equal(t, notification.SeverityInfo, pushed[1].Severity)
	require.Contains(t, pushed[1].Body, "改由“主机 storage · 失联”告警跟进")
	require.Nil(t, h.pageAlert(hostmetrics.CheckKindAgent))
}

// 资源阈值：连续 3 次采样超限才告警（同一次采样不重复计数），告警后回落到恢复线以下才恢复；
// 推送与页面是同一个标题、级别（警告）和原因，并带上进程排行。
func TestHostThresholdAlertCountsSamplesAndMatchesPage(t *testing.T) {
	h := newHostCheckHarness(t)
	cpu := hostmetrics.CheckID("AGENT01", hostmetrics.HostMetricCPU)
	failures := func() int {
		state, err := h.repos.Alerts.GetState(t.Context(), events.SystemSpaceID, "default:"+cpu, cpu)
		require.NoError(t, err)
		return state.FailureCount
	}
	h.gateway(true)
	h.report("AGENT01", 95)
	h.evaluate()
	require.Equal(t, 1, failures())
	// 30 秒后的下一轮没有新采样：不产生结果，不累计。
	h.advance(30 * time.Second)
	h.evaluate()
	require.Equal(t, 1, failures())
	h.advance(30 * time.Second)
	h.report("AGENT01", 95)
	h.evaluate()
	require.Equal(t, 2, failures())
	require.Empty(t, h.sender.forCheck(cpu))
	h.advance(time.Minute)
	h.report("AGENT01", 95)
	h.evaluate()
	pushed := h.sender.forCheck(cpu)
	require.Len(t, pushed, 1)
	require.Equal(t, "主机 storage · CPU 使用率", pushed[0].Title)
	require.Equal(t, notification.SeverityWarning, pushed[0].Severity)
	require.Contains(t, pushed[0].Body, "问题：CPU 使用率 95.0%，超过阈值 80%\n")
	require.Contains(t, pushed[0].Body, "占用最高（进程 CPU 按单核计）：storage-primary CPU 340%、内存 5.0 GB；duckdb CPU 45%、内存 7.0 GB\n")
	alert := h.pageAlert(hostmetrics.HostMetricCPU)
	requireSameAsPush(t, alert, pushed[0])
	require.Len(t, alert.Processes, 2)
	require.Equal(t, "storage-primary", alert.Processes[0].Name)

	// 回落到告警线以下、恢复线以上：仍在告警，不恢复。
	h.advance(time.Minute)
	h.report("AGENT01", 75)
	h.evaluate()
	require.Len(t, h.sender.forCheck(cpu), 1)
	alert = h.pageAlert(hostmetrics.HostMetricCPU)
	require.NotNil(t, alert)
	require.Equal(t, "CPU 使用率 75.0%，还没有回落到恢复线 70%", alert.Reason)

	h.advance(time.Minute)
	h.report("AGENT01", 65)
	h.evaluate()
	pushed = h.sender.forCheck(cpu)
	require.Len(t, pushed, 2)
	require.Equal(t, notification.SeverityInfo, pushed[1].Severity)
	require.Contains(t, pushed[1].Body, "当前：CPU 使用率 65.0%")
	require.NotContains(t, pushed[1].Body, "占用最高")
	require.Nil(t, h.pageAlert(hostmetrics.HostMetricCPU))

	// 没有在告警时，落在两条线之间不算超限。
	h.advance(time.Minute)
	h.report("AGENT01", 75)
	h.evaluate()
	require.Zero(t, failures())
}

// 观测输入中断时主机类检查冻结：不新增失联告警，也不恢复已有告警；恢复后照常判定。
func TestHostChecksFreezeWhileObservationIsSuspended(t *testing.T) {
	h := newHostCheckHarness(t)
	presence := hostmetrics.CheckID("AGENT01", hostmetrics.CheckKindPresence)
	h.gateway(true)
	h.report("AGENT01", 20)
	h.evaluate()

	h.suspended = true
	h.advance(10 * time.Minute)
	h.gateway(false)
	h.evaluate()
	require.Empty(t, h.sender.forCheck(presence), "观测输入中断时所有 last_seen 都是旧的，不能判失联")
	require.Nil(t, h.pageAlert(hostmetrics.CheckKindPresence))

	h.suspended = false
	h.evaluate()
	require.Len(t, h.sender.forCheck(presence), 1)

	h.suspended = true
	h.advance(time.Minute)
	h.gateway(true)
	h.report("AGENT01", 20)
	h.evaluate()
	require.Len(t, h.sender.forCheck(presence), 1, "冻结期间也不恢复")
	require.NotNil(t, h.pageAlert(hostmetrics.CheckKindPresence))
}

// 主机失联后换了新的采集器上报（重装）：旧采集器的失联告警恢复，新采集器接着被检查。停用主机后不再检查。
func TestReplacedAgentResolvesTheOldPresenceAlert(t *testing.T) {
	h := newHostCheckHarness(t)
	old := hostmetrics.CheckID("AGENT01", hostmetrics.CheckKindPresence)
	h.gateway(true)
	h.report("AGENT01", 20)
	h.evaluate()
	h.advance(4 * time.Minute)
	h.gateway(false)
	h.evaluate()
	require.Len(t, h.sender.forCheck(old), 1)

	h.advance(time.Minute)
	h.gateway(true)
	h.report("AGENT02", 20)
	h.evaluate()
	pushed := h.sender.forCheck(old)
	require.Len(t, pushed, 2)
	require.Equal(t, notification.SeverityInfo, pushed[1].Severity)
	require.Nil(t, h.pageAlert(hostmetrics.CheckKindPresence))
	_, err := h.repos.Checks.Get(t.Context(), events.SystemSpaceID, hostmetrics.CheckID("AGENT02", hostmetrics.CheckKindPresence))
	require.NoError(t, err)

	// 主机停用后新采集器停报不告警。
	snapshot, err := h.repos.Topology.Snapshot(t.Context())
	require.NoError(t, err)
	snapshot.Hosts[0].Status = servicecatalog.Disabled
	err = h.repos.Topology.Reconcile(t.Context(), *snapshot)
	require.NoError(t, err)
	h.advance(10 * time.Minute)
	h.gateway(false)
	h.evaluate()
	for _, message := range h.sender.forCheck(hostmetrics.CheckID("AGENT02", hostmetrics.CheckKindPresence)) {
		require.False(t, strings.Contains(message.Body, "新告警"), "停用的主机不产生失联告警")
	}
}

// 业务评估整体失败（例如存储机宕机、读 Storage 超时）时，主机类检查独立先跑、只读本地库：失联照样产出结果并告警。
// 业务评估自己跑的时候也不会把主机类检查标成“不再检查”把告警恢复掉。
func TestHostChecksDoNotDependOnBusinessEvaluation(t *testing.T) {
	h := newHostCheckHarness(t)
	presence := hostmetrics.CheckID("AGENT01", hostmetrics.CheckKindPresence)
	h.gateway(true)
	h.report("AGENT01", 20)
	require.NoError(t, h.hostRun(t.Context()))

	// 业务评估这一轮没有跑成；调用方的上下文也已经超时，主机类检查用自己的时限。
	h.advance(4 * time.Minute)
	h.gateway(false)
	expired, cancel := context.WithCancel(t.Context())
	cancel()
	require.NoError(t, h.hostRun(expired))
	require.Error(t, h.businessRun(expired), "业务评估在已超时的上下文里失败")
	pushed := h.sender.forCheck(presence)
	require.Len(t, pushed, 1)
	require.Equal(t, "主机 storage · 失联", pushed[0].Title)
	requireSameAsPush(t, h.pageAlert(hostmetrics.CheckKindPresence), pushed[0])

	// 只跑业务评估：主机类检查没有新结果，告警保持。
	for range 3 {
		require.NoError(t, h.businessRun(t.Context()))
	}
	require.Len(t, h.sender.forCheck(presence), 1, "业务评估不能把主机失联恢复掉")
	results, err := h.repos.Results.Recent(t.Context(), events.SystemSpaceID, presence, 1)
	require.NoError(t, err)
	require.NotEqual(t, domain.CheckReasonRetired, results[0].ErrorMessage)
	require.NotNil(t, h.pageAlert(hostmetrics.CheckKindPresence))
}

// 资源告警在告警中时主机被停用：主机类检查不再检查这台主机，资源告警随之恢复，不会一直挂着。
func TestDisabledHostResolvesItsResourceAlert(t *testing.T) {
	h := newHostCheckHarness(t)
	cpu := hostmetrics.CheckID("AGENT01", hostmetrics.HostMetricCPU)
	h.gateway(true)
	for range 3 {
		h.report("AGENT01", 95)
		h.evaluate()
		h.advance(time.Minute)
	}
	require.NotNil(t, h.pageAlert(hostmetrics.HostMetricCPU))

	snapshot, err := h.repos.Topology.Snapshot(t.Context())
	require.NoError(t, err)
	snapshot.Hosts[0].Status = servicecatalog.Disabled
	err = h.repos.Topology.Reconcile(t.Context(), *snapshot)
	require.NoError(t, err)
	h.advance(5 * time.Minute)
	h.gateway(false)
	h.evaluate()
	require.Nil(t, h.pageAlert(hostmetrics.HostMetricCPU))
	pushed := h.sender.forCheck(cpu)
	require.Equal(t, notification.SeverityInfo, pushed[len(pushed)-1].Severity)
	require.Len(t, h.sender.forCheck(hostmetrics.CheckID("AGENT01", hostmetrics.CheckKindPresence)), 0, "停用的主机不报失联")
}

// 同一主机新旧两个采集器同时在报（重装重叠）：只有最近上报的那个有主机类检查，不会出两份同名的资源检查。
func TestOnlyTheSelectedAgentHasResourceChecks(t *testing.T) {
	h := newHostCheckHarness(t)
	h.gateway(true)
	h.report("AGENT01", 95)
	h.advance(time.Second)
	h.report("AGENT02", 95)
	h.evaluate()
	_, err := h.repos.Checks.Get(t.Context(), events.SystemSpaceID, hostmetrics.CheckID("AGENT02", hostmetrics.HostMetricCPU))
	require.NoError(t, err)
	_, err = h.repos.Checks.Get(t.Context(), events.SystemSpaceID, hostmetrics.CheckID("AGENT01", hostmetrics.HostMetricCPU))
	require.Error(t, err, "被取代的采集器没有资源检查")
}

// host-agent 停报期间网关状态读取时好时坏：上一次读到的心跳还在 2 分钟内就仍算正常，
// 告警保持“host-agent 停止上报”，不会在停报与失联之间来回切换。
func TestGatewayReadErrorsDoNotFlipAgentStopIntoHostLost(t *testing.T) {
	h := newHostCheckHarness(t)
	presence := hostmetrics.CheckID("AGENT01", hostmetrics.CheckKindPresence)
	agent := hostmetrics.CheckID("AGENT01", hostmetrics.CheckKindAgent)
	h.gateway(true)
	h.report("AGENT01", 20)
	h.evaluate()
	h.advance(3*time.Minute + 10*time.Second)
	h.gateway(true)
	h.evaluate()
	require.Len(t, h.sender.forCheck(agent), 1)

	for range 3 {
		h.advance(30 * time.Second)
		failed := domain.GatewayObservation{HostID: "storage", ReadError: "GetHostRoutes unavailable", HostEnabledAt: h.clock.Add(-time.Hour)}
		require.NoError(t, h.repos.Gateways.Reconcile(t.Context(), []domain.GatewayObservation{failed}, h.clock))
		h.evaluate()
		h.advance(30 * time.Second)
		h.gateway(true)
		h.evaluate()
	}
	require.Empty(t, h.sender.forCheck(presence), "读取出错不能把在线主机报成失联")
	require.Len(t, h.sender.forCheck(agent), 1, "停报告警保持，不反复恢复再告警")
}

// 观测输入冻结期间到的超限采样不会被吃掉：解冻后照常计入。
func TestSampleArrivingWhileSuspendedIsEvaluatedAfterwards(t *testing.T) {
	h := newHostCheckHarness(t)
	cpu := hostmetrics.CheckID("AGENT01", hostmetrics.HostMetricCPU)
	h.gateway(true)
	h.report("AGENT01", 20)
	h.evaluate()
	h.advance(time.Minute)
	h.suspended = true
	h.report("AGENT01", 95)
	h.evaluate()
	h.suspended = false
	h.advance(30 * time.Second)
	h.evaluate()
	state, err := h.repos.Alerts.GetState(t.Context(), events.SystemSpaceID, "default:"+cpu, cpu)
	require.NoError(t, err)
	require.Equal(t, 1, state.FailureCount)
}
