package healthview

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/monitor/internal/alerttext"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/placement"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
)

var updateSnapshot = flag.Bool("update", false, "重写健康概览快照")

var snapshotNow = time.Date(2026, 10, 9, 4, 0, 0, 0, time.UTC)

func ago(d time.Duration) time.Time { return snapshotNow.Add(-d) }

func agoText(d time.Duration) string { return ago(d).Format(time.RFC3339Nano) }

type staticFacts struct{ overview observability.Overview }

func (s staticFacts) Build(context.Context, string) (observability.Overview, error) {
	return s.overview, nil
}

type staticPlacements struct {
	placements []*adminpb.DeployPlacement
	err        error
}

func (s staticPlacements) Placements(context.Context) ([]*adminpb.DeployPlacement, error) {
	return s.placements, s.err
}

type staticAgents []hostmetrics.AgentView

func (s staticAgents) ListAgents(context.Context) ([]hostmetrics.AgentView, error) { return s, nil }

func newRepositories(t *testing.T) *store.Repositories {
	t.Helper()
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	return mgr.Repositories()
}

// snapshotCatalog 是内置组件目录，只把出口代理改成不探测，用来覆盖「不探测」的组件。
func snapshotCatalog(t *testing.T) *servicecatalog.Catalog {
	t.Helper()
	raw := string(servicecatalog.Embedded())
	const egressHealth = "health: {kind: readyz, port: 11441}"
	require.Equal(t, 1, strings.Count(raw, egressHealth))
	catalog, err := servicecatalog.Parse([]byte(strings.Replace(raw, egressHealth, "health: {kind: none}", 1)))
	require.NoError(t, err)
	return catalog
}

func addPlacementCheck(t *testing.T, repositories *store.Repositories, hostID, componentID, url string, enabled bool) {
	t.Helper()
	labels, err := json.Marshal(placement.Labels{HostID: hostID, ComponentID: componentID})
	require.NoError(t, err)
	require.NoError(t, repositories.Checks.Create(t.Context(), &domain.Check{
		CheckID: placement.CheckID(hostID, componentID), Name: alerttext.Service(componentID) + "（" + hostID + "）· 健康检查",
		Kind: domain.CheckKindHTTP, URL: url, Enabled: enabled, Source: domain.CheckSourcePlacement, Labels: string(labels),
	}))
}

func addResult(t *testing.T, repositories *store.Repositories, spaceID, checkID string, at time.Time, rawError string) {
	t.Helper()
	status := domain.CheckStatusOK
	if rawError != "" {
		status = domain.CheckStatusDown
	}
	require.NoError(t, repositories.Results.Insert(t.Context(), &domain.CheckResult{
		ResultID: checkID + "@" + at.Format(time.RFC3339), SpaceID: spaceID, CheckID: checkID, InstanceID: "monitor",
		Success: rawError == "", Status: status, ErrorMessage: rawError, CheckedAt: at,
	}))
}

func addFiringAlert(t *testing.T, repositories *store.Repositories, spaceID, checkID, description string, triggeredAt time.Time) {
	t.Helper()
	ruleID := "default:" + checkID
	require.NoError(t, repositories.Alerts.CreateRule(t.Context(), &domain.AlertRule{
		SpaceID: spaceID, RuleID: ruleID, CheckID: checkID, FailureThreshold: 1, SuccessThreshold: 1,
		Enabled: true, Description: description,
	}))
	require.NoError(t, repositories.Alerts.UpsertState(t.Context(), &domain.AlertState{
		SpaceID: spaceID, RuleID: ruleID, CheckID: checkID, Status: domain.AlertStatusFiring, FailureCount: 3,
		TriggeredAt: &triggeredAt, DedupeKey: ruleID + ":" + checkID,
	}))
}

// TestOverviewSnapshot 覆盖：一个组件异常（storage 上的存储主服务探测失败）；一台主机没有心跳（storage）；一个未登记的
// 进程（compute-1 上的存储数据节点）；一条主机告警（control 的磁盘空间）；一个 https 探测的组件（控制台代理）。另外还有
// 不探测、部署停用、主机停用的组件，以及采集、存储、因子三个阶段的数据集。
func TestOverviewSnapshot(t *testing.T) {
	ctx := t.Context()
	repositories := newRepositories(t)

	hosts := []*adminpb.DeployHost{
		{HostId: "control", Address: "203.0.113.10", Status: "enabled", CreatedAt: agoText(30 * 24 * time.Hour),
			Gateway: &adminpb.HostGatewayStatus{State: "online", InstanceId: "host-gateway@control", ExpectedHash: "abc", AppliedHash: "abc", LastSeenAt: agoText(20 * time.Second)}},
		{HostId: "storage", Address: "203.0.113.20", Status: "enabled", CreatedAt: agoText(30 * 24 * time.Hour),
			Gateway: &adminpb.HostGatewayStatus{State: "offline", InstanceId: "host-gateway@storage", ExpectedHash: "abc", AppliedHash: "abc", LastSeenAt: agoText(10 * time.Minute)}},
		{HostId: "compute-1", Address: "203.0.113.30", Status: "disabled", CreatedAt: agoText(30 * 24 * time.Hour)},
	}
	gatewayHosts := make([]observability.GatewayHostStatus, 0, len(hosts))
	for _, host := range hosts {
		gatewayHosts = append(gatewayHosts, observability.EvaluateGatewayHost(host, snapshotNow))
	}
	placements := []*adminpb.DeployPlacement{
		{HostId: "control", ComponentId: "console-proxy", Status: "enabled"},
		{HostId: "control", ComponentId: "monitor", Status: "enabled"},
		{HostId: "control", ComponentId: "host-agent", Status: "enabled"},
		{HostId: "control", ComponentId: "egress-proxy", Status: "enabled"},
		{HostId: "storage", ComponentId: "storage-primary", Status: "enabled"},
		{HostId: "storage", ComponentId: "access", Status: "disabled", UpdatedAt: agoText(2 * time.Hour)},
		{HostId: "compute-1", ComponentId: "trade", Status: "enabled"},
	}

	addPlacementCheck(t, repositories, "control", "console-proxy", "https://203.0.113.10:9527/", true)
	addPlacementCheck(t, repositories, "control", "monitor", "http://127.0.0.1:11409/readyz", true)
	addPlacementCheck(t, repositories, "control", "host-agent", "http://127.0.0.1:11425/readyz", true)
	addPlacementCheck(t, repositories, "storage", "storage-primary", "http://203.0.113.20:20210/readyz", true)
	addPlacementCheck(t, repositories, "storage", "access", "http://203.0.113.20:11014/readyz", false)
	addPlacementCheck(t, repositories, "compute-1", "trade", "http://203.0.113.30:11210/readyz", false)
	for _, offset := range []time.Duration{90 * time.Second, 60 * time.Second, 30 * time.Second} {
		addResult(t, repositories, "", placement.CheckID("control", "console-proxy"), ago(offset), "")
	}
	addResult(t, repositories, "", placement.CheckID("control", "monitor"), ago(30*time.Second), "")
	addResult(t, repositories, "", placement.CheckID("control", "host-agent"), ago(30*time.Second), "")
	refused := "Get \"http://203.0.113.20:20210/readyz\": dial tcp 203.0.113.20:20210: connect: connection refused"
	addResult(t, repositories, "", placement.CheckID("storage", "storage-primary"), ago(120*time.Second), "")
	for _, offset := range []time.Duration{90 * time.Second, 60 * time.Second, 30 * time.Second} {
		addResult(t, repositories, "", placement.CheckID("storage", "storage-primary"), ago(offset), refused)
	}
	addFiringAlert(t, repositories, "", placement.CheckID("storage", "storage-primary"), "", ago(60*time.Second))

	storageGateway := gatewayHosts[1]
	require.NoError(t, repositories.Checks.Create(ctx, &domain.Check{
		SpaceID: monmetrics.InternalMetricSpaceID, CheckID: "host_gateway:storage", Name: "主机网关（storage）· 心跳与路由同步",
		Kind: domain.CheckKindExternal, Enabled: true, Source: domain.CheckSourceObservability,
	}))
	addResult(t, repositories, monmetrics.InternalMetricSpaceID, "host_gateway:storage", ago(10*time.Second), storageGateway.Reason)
	addFiringAlert(t, repositories, monmetrics.InternalMetricSpaceID, "host_gateway:storage", "", ago(7*time.Minute))
	addFiringAlert(t, repositories, hostmetrics.SpaceID, hostmetrics.HostRuleKey("aB3x", hostmetrics.HostMetricFilesystemUsage),
		`{"threshold":85,"recovery_threshold":80}`, ago(3*time.Minute))

	require.NoError(t, repositories.Notifications.SeedIfAbsent(ctx, domain.NotificationChannel{
		ChannelType: "wecom", WebhookURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=secret-key-0042",
	}))

	agents := staticAgents{
		{AgentID: "aB3x", HostID: "control", Hostname: "VM-0-1-ubuntu", LastSeenAt: agoText(15 * time.Second), Reachable: true,
			Snapshot: &hostmetricpb.HostSnapshot{
				Cpu:    &hostmetricpb.CpuMetric{UsagePercent: 12.5, UsageAvailable: true},
				Memory: &hostmetricpb.MemoryMetric{UsagePercent: 40.2},
				Filesystems: []*hostmetricpb.FilesystemMetric{
					{Mountpoint: "/", UsagePercent: 91}, {Mountpoint: "/data", UsagePercent: 50},
				},
			}},
		{AgentID: "cD5y", HostID: "storage", Hostname: "VM-0-2-ubuntu", LastSeenAt: agoText(10 * time.Minute), Reachable: false,
			Snapshot: &hostmetricpb.HostSnapshot{
				Cpu:         &hostmetricpb.CpuMetric{UsagePercent: 5, UsageAvailable: true},
				Memory:      &hostmetricpb.MemoryMetric{UsagePercent: 30},
				Filesystems: []*hostmetricpb.FilesystemMetric{{Mountpoint: "/", UsagePercent: 60}},
			}},
	}
	facts := observability.Overview{
		GeneratedAt: snapshotNow,
		Services: []observability.ServiceStatus{
			{NodeID: "control", ServiceName: "monitor", InstanceID: "monitor@control", ReporterStatus: "healthy", Version: "v1.2.3", ReportedAt: ago(20 * time.Second)},
			{NodeID: "storage", ServiceName: "storage-primary", InstanceID: "storage-primary@storage-old", ReporterStatus: "stale", Version: "v1.2.2", ReportedAt: ago(3 * time.Hour)},
			{NodeID: "storage", ServiceName: "storage-primary", InstanceID: "storage-primary@storage", ReporterStatus: "stale", Version: "v1.2.3", ReportedAt: ago(6 * time.Minute)},
		},
		Datasets: []observability.DatasetFrequencyStatus{
			// Storage 已有同一数据集频率的事实，Collector 的这一行不再单独列出。
			{Producer: "collector", SpaceID: "crypto", DatasetID: "dataset_crypto_kline", Freq: "1m", Status: "unknown", Reason: "尚未上报"},
			{Producer: "collector", SpaceID: "crypto", DatasetID: "dataset_crypto_funding", Freq: "8h", Status: "unknown", Reason: "尚未上报"},
			{Producer: "storage", SpaceID: "crypto", DatasetID: "dataset_crypto_kline", Freq: "1m", Status: "healthy", Reason: "normal",
				LastRunAt: ago(30 * time.Second), LastSuccessAt: ago(30 * time.Second), OutputWatermarkAt: ago(time.Minute), LagSeconds: 60},
			{Producer: "factor", SpaceID: "crypto", DatasetID: "dataset_crypto_factor_momentum", Freq: "1h", Status: "stale", Reason: "run stale",
				LastRunAt: ago(3 * time.Hour), LastSuccessAt: ago(3 * time.Hour), OutputWatermarkAt: ago(3 * time.Hour), LagSeconds: 10800},
			// 主机指标数据集由主机告警覆盖，不放进数据链路。
			{Producer: "storage", SpaceID: "mooxsys", DatasetID: "dataset_mooxsys_host_resource", Freq: "15s", Status: "healthy"},
		},
		BusinessChecks: []observability.BusinessStatus{
			{SpaceID: "crypto", Kind: "market_fetch", Name: "行情采集 · SCF 定时协调", Module: "scf_timer", Status: "healthy", Reason: "Timer 分配和触发器正常", LastCheckedAt: snapshotNow},
			{SpaceID: "crypto", Kind: "balance", Name: "账户余额同步 · 交易服务", Module: "trade", Status: "down", Reason: "balance sync failed 3 consecutive runs", LastCheckedAt: snapshotNow},
		},
		GatewayHosts: gatewayHosts,
		Unregistered: []monmetrics.UnregisteredProducer{
			{ServiceName: "storage-node", NodeID: "compute-1", InstanceID: "storage-node@compute-1", Version: "v1.2.3", FirstSeenAt: ago(5 * time.Minute), LastSeenAt: ago(25 * time.Second)},
		},
	}

	got, err := (Builder{
		Facts: staticFacts{facts}, Placements: staticPlacements{placements: placements}, Hosts: agents,
		Checks: repositories.Checks, Results: repositories.Results, Alerts: repositories.Alerts,
		Notifications: repositories.Notifications, Catalog: snapshotCatalog(t), Now: func() time.Time { return snapshotNow },
	}).Build(ctx, "")
	require.NoError(t, err)

	encoded, err := json.MarshalIndent(got, "", "  ")
	require.NoError(t, err)
	encoded = append(encoded, '\n')
	path := filepath.Join("testdata", "overview_snapshot.json")
	if *updateSnapshot {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(path, encoded, 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "快照不存在时用 go test -run TestOverviewSnapshot -update 生成")
	require.Equal(t, string(want), string(encoded))
}

func TestOverviewFallsBackToPlacementChecksWhenSysDeployIsUnavailable(t *testing.T) {
	repositories := newRepositories(t)
	addPlacementCheck(t, repositories, "storage", "storage-view", "http://203.0.113.20:20211/readyz", true)
	addResult(t, repositories, "", placement.CheckID("storage", "storage-view"), ago(30*time.Second), "")

	got, err := (Builder{
		Placements: staticPlacements{err: errors.New("dial tcp: i/o timeout")},
		Checks:     repositories.Checks, Results: repositories.Results, Now: func() time.Time { return snapshotNow },
	}).Build(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, got.Components, 1)
	require.Equal(t, "storage-view", got.Components[0].ComponentID)
	require.Equal(t, "存储视图服务", got.Components[0].Name)
	require.Equal(t, StatusDegraded, got.Components[0].Status, "从未上报运行指标")
	require.Equal(t, StatusHealthy, got.Components[0].Probe.Status)
	require.Equal(t, []string{"暂时读不到 SysDeploy 的部署列表（dial tcp: i/o timeout），组件列表取自健康检查，未列出不探测的组件"}, got.Warnings)
}

func TestOverviewWithoutSourcesHasStableSections(t *testing.T) {
	got, err := (Builder{Now: func() time.Time { return snapshotNow }}).Build(t.Context(), "")
	require.NoError(t, err)
	require.True(t, got.GeneratedAt.Equal(snapshotNow))
	require.NotNil(t, got.Alerts)
	require.NotNil(t, got.Components)
	require.Len(t, got.Pipeline, 4)
	for _, stage := range got.Pipeline {
		require.Equal(t, StatusUnchecked, stage.Status)
		require.NotNil(t, stage.Datasets)
	}
	require.Equal(t, Summary{}, got.Summary)
}

func TestMaskURLNeverReturnsFullSecret(t *testing.T) {
	const raw = "https://example.com/hooks/super-secret"
	if got := MaskURL(raw); got == raw || got != "https://...cret" {
		t.Fatalf("masked URL = %q", got)
	}
}

func TestChineseReasonKeepsChineseAndTranslatesKnownCodes(t *testing.T) {
	require.Equal(t, "账户余额已连续三次同步失败", ChineseReason("balance sync failed 3 consecutive runs"))
	require.Equal(t, "无法连接消息总线（认证失败）", ChineseReason("eventbus connection unavailable: authentication failed"))
	require.Equal(t, "数据变更投递正常", ChineseReason("数据变更投递正常"))
	require.Equal(t, "检查失败，详见原始错误", ChineseReason("unexpected EOF"))
}
