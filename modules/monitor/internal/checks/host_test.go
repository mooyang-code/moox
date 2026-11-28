package checks

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/healthview"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	pb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/notification"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type capturedSender struct {
	mu       sync.Mutex
	messages []notification.Message
	// err 不为空时每次推送都失败（渠道故障）。
	err error
}

func (s *capturedSender) Send(_ context.Context, message notification.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, message)
	return s.err
}

// forCheck 返回某项系统空间检查推送过的消息（推送的 Key 是 空间/检查ID）。
func (s *capturedSender) forCheck(checkID string) []notification.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []notification.Message
	for _, message := range s.messages {
		if message.Key == events.SystemSpaceID+"/"+checkID {
			out = append(out, message)
		}
	}
	return out
}

// hostCheckHarness 把主机登记、评估（推进检查表、推送）和页面概览接在同一个库上，时间由 clock 控制。
type hostCheckHarness struct {
	t         *testing.T
	repos     *store.Repositories
	hosts     *hostmetrics.Store
	messages  *monmetrics.MetricMessageStore
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
	h.messages, err = store.WithDatabase(manager, monmetrics.NewMetricMessageStore)
	require.NoError(t, err)

	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	err = h.repos.Topology.Reconcile(t.Context(), domain.TopologySnapshot{Catalog: catalog, ObservedAt: h.clock,
		Hosts:       []domain.TopologyHost{{HostID: "storage", Address: "storage.example.test", Status: servicecatalog.Enabled}},
		Deployments: []domain.TopologyDeployment{{HostID: "storage", ComponentID: "host-agent", Status: servicecatalog.Enabled}, {HostID: "storage", ComponentID: "host-gateway", Status: servicecatalog.Enabled}}})
	require.NoError(t, err)

	now := func() time.Time { return h.clock }
	facts := &observability.Builder{Metrics: monmetrics.NewQueryService(h.messages, nil), Hosts: h.hosts, Topology: h.repos.Topology, Now: now}
	applier := &Applier{Checks: h.repos.Checks, Sender: func(context.Context) (notification.Sender, error) { return h.sender, nil }, Now: now}
	observation := func(time.Time) ObservationState { return ObservationState{Ready: true, Suspended: h.suspended} }
	h.hostRun = NewHostReporter(facts, applier, observation)
	h.businessRun = NewBusinessReporter(facts, h.repos, applier, observation, 30*time.Second)
	h.run = func(ctx context.Context) error {
		if err := h.hostRun(ctx); err != nil {
			return err
		}
		return h.businessRun(ctx)
	}
	h.page = healthview.Builder{Facts: facts, Checks: h.repos.Checks, Alerts: h.repos.Alerts, Now: now}
	return h
}

// report 模拟 storage 主机的 host-agent 在当前时刻上报一份快照。
func (h *hostCheckHarness) report(cpu float64) {
	h.t.Helper()
	h.sequence++
	metric := &hostmetricpb.HostMetric{HostId: "storage", Hostname: "VM-0-9", BootId: "boot", Snapshot: &hostmetricpb.HostSnapshot{
		Cpu:    &hostmetricpb.CpuMetric{LogicalCores: 4, UsageAvailable: true, UsagePercent: cpu},
		Memory: &hostmetricpb.MemoryMetric{TotalBytes: 100, UsedBytes: 50, AvailableBytes: 50, UsagePercent: 50},
		TopProcesses: []*hostmetricpb.ProcessMetric{
			{Pid: 11, Name: "storage-primary", CpuPercent: 340, CpuAvailable: true, RssBytes: 5 << 30},
			{Pid: 12, Name: "duckdb", CpuPercent: 45, CpuAvailable: true, RssBytes: 7 << 30},
		},
	}}
	require.NoError(h.t, h.hosts.Persist(h.t.Context(), hostmetrics.Report{ID: fmt.Sprintf("report-%d", h.sequence), CollectedAt: h.clock, Metric: metric}))
}

// gateway 写入主机网关的组件上报：alive 为 false 时最近一次上报停在 5 分钟前。
func (h *hostCheckHarness) gateway(alive bool) {
	h.t.Helper()
	seen := h.clock
	if !alive {
		seen = h.clock.Add(-5 * time.Minute)
	}
	h.sequence++
	_, err := h.messages.CommitIngest(h.t.Context(), &eventpb.EventMessage{EventId: fmt.Sprintf("gateway-%d", h.sequence), OccurredAt: seen.UnixMilli()},
		&metricspb.MetricReport{ServiceName: "host-gateway", InstanceId: "host-gateway@storage", NodeId: "storage", BootId: "boot", Ready: proto.Bool(true)}, nil)
	require.NoError(h.t, err)
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

// 同一次主机失联（采集器停报、网关也停报了）：页面与推送是同一个标题、级别和原因；恢复后页面告警消失并推送恢复。
func TestHostLostShowsTheSameAlertOnPageAndPush(t *testing.T) {
	h := newHostCheckHarness(t)
	presence := hostmetrics.CheckID("storage", hostmetrics.CheckKindPresence)
	h.gateway(true)
	h.report(20)
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
	require.Contains(t, pushed[0].Body, "主机网关也已停止上报")
	alert := h.pageAlert(hostmetrics.CheckKindPresence)
	requireSameAsPush(t, alert, pushed[0])
	require.Equal(t, "storage", alert.Object.HostId)
	require.Nil(t, h.pageAlert(hostmetrics.CheckKindAgent), "主机失联时不再另报 host-agent 停止上报")
	require.Empty(t, h.sender.forCheck(hostmetrics.CheckID("storage", hostmetrics.CheckKindAgent)))
	host := h.overview().Hosts[0]
	require.Equal(t, "down", host.Status)
	require.Equal(t, alert.Reason, host.Reason, "主机卡片与告警是同一段原因")

	h.advance(time.Minute)
	h.gateway(true)
	h.report(20)
	h.evaluate()
	pushed = h.sender.forCheck(presence)
	require.Len(t, pushed, 2)
	require.Equal(t, notification.SeverityInfo, pushed[1].Severity)
	require.Equal(t, "主机 storage · 失联", pushed[1].Title)
	require.Contains(t, pushed[1].Body, "状态：已恢复")
	require.Nil(t, h.pageAlert(hostmetrics.CheckKindPresence))
	require.Equal(t, "healthy", h.overview().Hosts[0].Status)
}

// 采集器停报但主机网关仍在上报：主机在线，页面与推送都是警告级的“host-agent 停止上报”，不是“失联”。
// 之后网关也停报了，改报主机失联。
func TestAgentOnlyStopIsAWarningOnPageAndPush(t *testing.T) {
	h := newHostCheckHarness(t)
	presence := hostmetrics.CheckID("storage", hostmetrics.CheckKindPresence)
	agent := hostmetrics.CheckID("storage", hostmetrics.CheckKindAgent)
	h.gateway(true)
	h.report(20)
	h.evaluate()

	h.advance(3*time.Minute + 10*time.Second)
	h.gateway(true)
	h.evaluate()
	pushed := h.sender.forCheck(agent)
	require.Len(t, pushed, 1)
	require.Equal(t, "主机 storage · host-agent 停止上报", pushed[0].Title)
	require.Equal(t, notification.SeverityWarning, pushed[0].Severity)
	require.Contains(t, pushed[0].Body, "主机网关仍在上报")
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
	cpu := hostmetrics.CheckID("storage", hostmetrics.HostMetricCPU)
	failures := func() int {
		row, err := h.repos.Checks.Get(t.Context(), events.SystemSpaceID, cpu)
		require.NoError(t, err)
		return row.FailureCount
	}
	h.gateway(true)
	h.report(95)
	h.evaluate()
	require.Equal(t, 1, failures())
	// 30 秒后的下一轮没有新采样：不产生结果，不累计。
	h.advance(30 * time.Second)
	h.evaluate()
	require.Equal(t, 1, failures())
	h.advance(30 * time.Second)
	h.report(95)
	h.evaluate()
	require.Equal(t, 2, failures())
	require.Empty(t, h.sender.forCheck(cpu))
	h.advance(time.Minute)
	h.report(95)
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
	h.report(75)
	h.evaluate()
	require.Len(t, h.sender.forCheck(cpu), 1)
	alert = h.pageAlert(hostmetrics.HostMetricCPU)
	require.NotNil(t, alert)
	require.Equal(t, "CPU 使用率 75.0%，还没有回落到恢复线 70%", alert.Reason)

	h.advance(time.Minute)
	h.report(65)
	h.evaluate()
	pushed = h.sender.forCheck(cpu)
	require.Len(t, pushed, 2)
	require.Equal(t, notification.SeverityInfo, pushed[1].Severity)
	require.Contains(t, pushed[1].Body, "当前：CPU 使用率 65.0%")
	require.NotContains(t, pushed[1].Body, "占用最高")
	require.Nil(t, h.pageAlert(hostmetrics.HostMetricCPU))

	// 没有在告警时，落在两条线之间不算超限。
	h.advance(time.Minute)
	h.report(75)
	h.evaluate()
	require.Zero(t, failures())
}

// 观测输入中断时主机类检查冻结：不新增失联告警，也不恢复已有告警；恢复后照常判定。
func TestHostChecksFreezeWhileObservationIsSuspended(t *testing.T) {
	h := newHostCheckHarness(t)
	presence := hostmetrics.CheckID("storage", hostmetrics.CheckKindPresence)
	h.gateway(true)
	h.report(20)
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
	h.report(20)
	h.evaluate()
	require.Len(t, h.sender.forCheck(presence), 1, "冻结期间也不恢复")
	require.NotNil(t, h.pageAlert(hostmetrics.CheckKindPresence))
}

// 主机失联后重装 HostAgent 恢复上报：同一台主机的失联告警恢复（主机 ID 不变，检查也不变）。停用主机后不再告警。
func TestReinstalledAgentResolvesThePresenceAlert(t *testing.T) {
	h := newHostCheckHarness(t)
	presence := hostmetrics.CheckID("storage", hostmetrics.CheckKindPresence)
	h.gateway(true)
	h.report(20)
	h.evaluate()
	h.advance(4 * time.Minute)
	h.gateway(false)
	h.evaluate()
	require.Len(t, h.sender.forCheck(presence), 1)

	h.advance(time.Minute)
	h.gateway(true)
	h.report(20)
	h.evaluate()
	pushed := h.sender.forCheck(presence)
	require.Len(t, pushed, 2)
	require.Equal(t, notification.SeverityInfo, pushed[1].Severity)
	require.Nil(t, h.pageAlert(hostmetrics.CheckKindPresence))

	// 主机停用后 host-agent 停报不告警。
	snapshot, err := h.repos.Topology.Snapshot(t.Context())
	require.NoError(t, err)
	snapshot.Hosts[0].Status = servicecatalog.Disabled
	err = h.repos.Topology.Reconcile(t.Context(), *snapshot)
	require.NoError(t, err)
	h.advance(10 * time.Minute)
	h.gateway(false)
	h.evaluate()
	require.Len(t, h.sender.forCheck(presence), 2, "停用的主机不产生失联告警")
}

// 业务评估整体失败（例如存储机宕机、读 Storage 超时）时，主机类检查独立先跑、只读本地库：失联照样产出结果并告警。
// 业务评估自己跑的时候也不会把主机类检查标成“不再检查”把告警恢复掉。
func TestHostChecksDoNotDependOnBusinessEvaluation(t *testing.T) {
	h := newHostCheckHarness(t)
	presence := hostmetrics.CheckID("storage", hostmetrics.CheckKindPresence)
	h.gateway(true)
	h.report(20)
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
	row, err := h.repos.Checks.Get(t.Context(), events.SystemSpaceID, presence)
	require.NoError(t, err, "业务评估不删主机域的检查")
	require.True(t, row.Firing())
	require.NotNil(t, h.pageAlert(hostmetrics.CheckKindPresence))
}

// 资源告警在告警中时主机被停用：主机类检查不再检查这台主机，资源告警随之恢复，不会一直挂着。
func TestDisabledHostResolvesItsResourceAlert(t *testing.T) {
	h := newHostCheckHarness(t)
	cpu := hostmetrics.CheckID("storage", hostmetrics.HostMetricCPU)
	h.gateway(true)
	for range 3 {
		h.report(95)
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
	require.Len(t, h.sender.forCheck(hostmetrics.CheckID("storage", hostmetrics.CheckKindPresence)), 0, "停用的主机不报失联")
}

// 观测输入冻结期间到的超限采样不会被吃掉：解冻后照常计入。
func TestSampleArrivingWhileSuspendedIsEvaluatedAfterwards(t *testing.T) {
	h := newHostCheckHarness(t)
	cpu := hostmetrics.CheckID("storage", hostmetrics.HostMetricCPU)
	h.gateway(true)
	h.report(20)
	h.evaluate()
	h.advance(time.Minute)
	h.suspended = true
	h.report(95)
	h.evaluate()
	h.suspended = false
	h.advance(30 * time.Second)
	h.evaluate()
	row, err := h.repos.Checks.Get(t.Context(), events.SystemSpaceID, cpu)
	require.NoError(t, err)
	require.Equal(t, 1, row.FailureCount)
}

// 推送渠道一直故障：主机类检查照常成功返回（不让 Monitor 判自己评估停滞），同一次采样只计一次，下一次新采样才累计。
func TestHostChecksKeepRunningWhileNotificationsFail(t *testing.T) {
	h := newHostCheckHarness(t)
	h.sender.err = errors.New("渠道不可用")
	cpu := hostmetrics.CheckID("storage", hostmetrics.HostMetricCPU)
	failures := func() int {
		row, err := h.repos.Checks.Get(t.Context(), events.SystemSpaceID, cpu)
		require.NoError(t, err)
		return row.FailureCount
	}
	h.gateway(true)
	for i := 1; i <= 4; i++ {
		h.report(95)
		require.NoError(t, h.hostRun(t.Context()))
		require.Equal(t, i, failures())
		h.advance(30 * time.Second)
		require.NoError(t, h.hostRun(t.Context()), "没有新采样的一轮也照常返回")
		require.Equal(t, i, failures(), "同一次采样不重复计数")
		h.advance(30 * time.Second)
	}
	row, err := h.repos.Checks.Get(t.Context(), events.SystemSpaceID, cpu)
	require.NoError(t, err)
	require.True(t, row.Firing())
}

// reportDisk 模拟 storage 主机上报带磁盘用量的快照（同时保留正常 CPU/内存）。
func (h *hostCheckHarness) reportDisk(totalGiB, usedGiB uint64) {
	h.t.Helper()
	const gib = uint64(1 << 30)
	h.sequence++
	usage := float64(usedGiB) / float64(totalGiB) * 100
	metric := &hostmetricpb.HostMetric{HostId: "storage", Hostname: "VM-0-9", BootId: "boot", Snapshot: &hostmetricpb.HostSnapshot{
		Cpu:    &hostmetricpb.CpuMetric{LogicalCores: 4, UsageAvailable: true, UsagePercent: 20},
		Memory: &hostmetricpb.MemoryMetric{TotalBytes: 100, UsedBytes: 50, AvailableBytes: 50, UsagePercent: 50},
		Filesystems: []*hostmetricpb.FilesystemMetric{
			{Device: "/dev/vda1", Mountpoint: "/", TotalBytes: totalGiB * gib, UsedBytes: usedGiB * gib, AvailableBytes: (totalGiB - usedGiB) * gib, UsagePercent: usage},
		},
	}}
	require.NoError(h.t, h.hosts.Persist(h.t.Context(), hostmetrics.Report{ID: fmt.Sprintf("disk-report-%d", h.sequence), CollectedAt: h.clock, Metric: metric}))
}

// 磁盘写满预测主机检查：历史不足 3 个自然日间隔时不告警；按近 7 天日增中位数预计 <= 7 天耗尽安全余量（10% 且不少于 5 GiB）时触发警告，
// 告警后需回落到 > 10 天或停止增长才恢复；主机停用后随之恢复。
func TestHostDiskForecastAlertsWithHysteresisAndResolves(t *testing.T) {
	h := newHostCheckHarness(t)
	checkID := hostmetrics.CheckID("storage", hostmetrics.CheckKindDiskForecast)

	// 1. 前 3 天（仅 2 个自然日间隔）：样本不足，按正常记录、不告警。
	// 总容量 100 GiB，安全余量 10 GiB（10%），可用上限 90 GiB；每天增长 2 GiB。
	for day := 0; day < 3; day++ {
		h.gateway(true)
		h.reportDisk(100, 74+uint64(day)*2) // 74, 76, 78 GiB
		h.evaluate()
		require.Empty(t, h.sender.forCheck(checkID))
		require.Nil(t, h.pageAlert(hostmetrics.CheckKindDiskForecast))
		row, err := h.repos.Checks.Get(t.Context(), events.SystemSpaceID, checkID)
		require.NoError(t, err)
		require.Equal(t, domain.CheckStatusOK, row.Status)
		require.Contains(t, row.Reason, "样本不足")
		h.advance(24 * time.Hour)
	}

	// 2. 第 4 天（3 个自然日间隔，日增中位数 2 GiB/天）：已用 80 GiB，距 90 GiB 剩 10 GiB = 5.0 天 <= 7 天，立刻触发警告。
	h.gateway(true)
	h.reportDisk(100, 80)
	h.evaluate()
	pushed := h.sender.forCheck(checkID)
	require.Len(t, pushed, 1)
	require.Equal(t, "主机 storage · 磁盘写满预测", pushed[0].Title)
	require.Equal(t, notification.SeverityWarning, pushed[0].Severity)
	require.Contains(t, pushed[0].Body, "/ 预计 5.0 天后耗尽安全余量")
	require.Contains(t, pushed[0].Body, "阈值 7 天")
	alert := h.pageAlert(hostmetrics.CheckKindDiskForecast)
	requireSameAsPush(t, alert, pushed[0])
	require.Equal(t, "storage", alert.Object.HostId)

	// 3. 同一分钟之后仅推进 30 秒无新采样：冻结，不重复触发也不恢复。
	h.advance(30 * time.Second)
	h.evaluate()
	require.Len(t, h.sender.forCheck(checkID), 1)
	require.NotNil(t, h.pageAlert(hostmetrics.CheckKindDiskForecast))

	// 4. 清理 7 GiB 后当天已用降到 73 GiB：前三天增量仍为 2、2，当天增量变 -5，中位数仍为 2 GiB/天，
	// 剩余 17 GiB / 2 = 8.5 天（在 7 天与 10 天恢复线之间）：仍在告警，不抖动恢复。
	h.advance(time.Minute)
	h.gateway(true)
	h.reportDisk(100, 73)
	h.evaluate()
	require.Len(t, h.sender.forCheck(checkID), 1)
	alert = h.pageAlert(hostmetrics.CheckKindDiskForecast)
	require.NotNil(t, alert)
	require.Contains(t, alert.Reason, "还没有回落到恢复线 10 天")

	// 5. 再清理到 60 GiB：剩余 30 GiB / 2 = 15.0 天 > 10 天，告警恢复并推送恢复通知。
	h.advance(time.Minute)
	h.gateway(true)
	h.reportDisk(100, 60)
	h.evaluate()
	pushed = h.sender.forCheck(checkID)
	require.Len(t, pushed, 2)
	require.Equal(t, notification.SeverityInfo, pushed[1].Severity)
	require.Contains(t, pushed[1].Body, "状态：已恢复")
	require.Nil(t, h.pageAlert(hostmetrics.CheckKindDiskForecast))
}

func TestHostDiskForecastIgnoresSmallPartitionsUnderMinimumReserve(t *testing.T) {
	h := newHostCheckHarness(t)
	checkID := hostmetrics.CheckID("storage", hostmetrics.CheckKindDiskForecast)
	const gib = uint64(1 << 30)
	const mib = uint64(1 << 20)

	// 总量小于 5 GiB 的小分区即使连续 5 天增长，也不应误报“0.0 天后耗尽安全余量”。
	for day := 0; day < 5; day++ {
		h.gateway(true)
		used := 500*mib + uint64(day)*100*mib
		total := 2 * gib
		h.sequence++
		metric := &hostmetricpb.HostMetric{HostId: "storage", Hostname: "VM-0-9", BootId: "boot", Snapshot: &hostmetricpb.HostSnapshot{
			Cpu:    &hostmetricpb.CpuMetric{LogicalCores: 4, UsageAvailable: true, UsagePercent: 20},
			Memory: &hostmetricpb.MemoryMetric{TotalBytes: 100, UsedBytes: 50, AvailableBytes: 50, UsagePercent: 50},
			Filesystems: []*hostmetricpb.FilesystemMetric{
				{Device: "/dev/vda2", Mountpoint: "/boot", TotalBytes: total, UsedBytes: used, AvailableBytes: total - used, UsagePercent: float64(used) / float64(total) * 100},
			},
		}}
		require.NoError(t, h.hosts.Persist(h.t.Context(), hostmetrics.Report{ID: fmt.Sprintf("small-disk-%d", h.sequence), CollectedAt: h.clock, Metric: metric}))
		h.evaluate()
		require.Empty(t, h.sender.forCheck(checkID))
		require.Nil(t, h.pageAlert(hostmetrics.CheckKindDiskForecast))
		h.advance(24 * time.Hour)
	}
	row, err := h.repos.Checks.Get(t.Context(), events.SystemSpaceID, checkID)
	require.NoError(t, err)
	require.Equal(t, domain.CheckStatusOK, row.Status)
	require.Contains(t, row.Reason, "5 GiB")
}
