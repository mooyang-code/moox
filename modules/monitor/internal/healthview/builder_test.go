package healthview

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestOverviewV2MixedFactsSnapshot(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	for i := range catalog.Components {
		if catalog.Components[i].ID == "console-proxy" {
			catalog.Components[i].Name = "目录中的控制台入口"
		}
	}
	facts := observability.Overview{GeneratedAt: now, TopologyKnown: true, Topology: &domain.TopologySnapshot{Catalog: catalog, Hosts: []domain.TopologyHost{{HostID: "control", Address: "control.test", Status: "enabled"}, {HostID: "offline", Address: "offline.test", Status: "enabled"}}},
		Services: []observability.ServiceStatus{
			{NodeID: "control", ServiceName: "console-proxy", Enabled: true, Status: "healthy", Reason: "health check ok", ProbeStatus: "healthy", ProbeCheckedAt: now, ReporterStatus: "healthy", LastSeenAt: now, Instances: []observability.ReporterInstance{{InstanceID: "proxy-1", BootID: "boot-1", Version: "v2", Status: "healthy", LastSeenAt: now}}},
			{NodeID: "control", ServiceName: "web-host", Enabled: true, Status: "down", Reason: "health check failed", ProbeStatus: "down", ProbeReason: "健康检查失败", ProbeRawError: "dial tcp: connection refused\n原始错误", ProbeCheckedAt: now},
			{NodeID: "offline", ServiceName: "web-host", Enabled: true, Status: "unknown", Reason: "health not checked", ProbeStatus: "unknown"},
			{NodeID: "control", ServiceName: "eventbus", Enabled: true, Status: "unchecked", Reason: "不探测", ProbeStatus: "unchecked", ReporterStatus: "missing"},
			{NodeID: "control", ServiceName: "cloudfunction", Status: "disabled", Reason: "部署已停用", ProbeStatus: "disabled"},
		},
		Unregistered:   []observability.ServiceStatus{{NodeID: "control", ServiceName: "unknown-collector-name", Instances: []observability.ReporterInstance{{InstanceID: "orphan", Version: "dev", Status: "healthy", LastSeenAt: now}}}},
		Hosts:          []observability.HostStatus{{HostID: "offline", AgentID: "PHYSICAL01", Hostname: "offline-host", Status: "down", Reason: "agent unreachable", LastSeenAt: now.Add(-10 * time.Minute)}, {AgentID: "STANDALONE", Hostname: "control", Status: "healthy", Reason: "agent reachable", LastSeenAt: now}},
		BusinessChecks: []observability.BusinessStatus{{CheckID: "console-page:control:console-proxy", Kind: "console-page", Module: "console-proxy", Status: "down", Reason: "页面请求失败", RawError: "HTTP 502\nupstream web-host", LastCheckedAt: now}},
		GatewaySignals: []observability.GatewaySignal{{HostID: "offline", Kind: "heartbeat", Status: "down", Reason: "主机网关心跳已中断", CheckedAt: now}},
		Datasets:       []observability.DatasetFrequencyStatus{{Producer: "storage", SpaceID: "crypto", DatasetID: "bars", Freq: "1m", Status: "degraded", Reason: "输出水位已落后", LastRunAt: now, LastSuccessAt: now.Add(-time.Minute), InputWatermarkAt: now, OutputWatermarkAt: now.Add(-2 * time.Minute), LastReportedAt: now, LatestBarClose: now.Add(-time.Minute), OutputArrivedAt: now.Add(-time.Minute + 3*time.Second), DelaySeconds: 3, DelayKnown: true, ExpectedBarAt: now.Add(-2 * time.Minute), ExpectedBarClose: now.Add(-time.Minute), LatenessLimitSeconds: 600, Timezone: "UTC"}},
	}
	view := projectFacts(facts)
	hostAlert, err := (Builder{}).projectAlert(t.Context(), domain.AlertState{CheckID: "host:PHYSICAL01:cpu", DedupeKey: "host-alert", TriggeredAt: &now, UpdatedAt: now}, facts, newCheckTitles(nil))
	require.NoError(t, err)
	view.Alerts = append(view.Alerts, hostAlert)
	view.Summary.AlertCount = int32(len(view.Alerts))
	require.Len(t, view.Components, 5)
	require.Equal(t, "目录中的控制台入口", view.Components[1].Name)
	require.Equal(t, "healthy", view.Components[1].Status, "page failure must not overwrite proxy readiness")
	require.Equal(t, "control", view.Hosts[1].HostId, "hostname alone must not consume the registered control host")
	require.Equal(t, int32(1), view.Summary.Components.HealthyCount)
	require.Equal(t, int32(1), view.Summary.UnregisteredCount)
	wire, err := protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}.Marshal(view)
	require.NoError(t, err)
	var normalized any
	require.NoError(t, json.Unmarshal(wire, &normalized))
	wire, err = json.MarshalIndent(normalized, "", "  ")
	require.NoError(t, err)
	wire = append(wire, '\n')
	path := filepath.Join("testdata", "overview-v2.json")
	if os.Getenv("UPDATE_HEALTH_GOLDEN") == "1" {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
		require.NoError(t, os.WriteFile(path, wire, 0644))
	}
	expected, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, string(expected), string(wire))
}

func TestActiveHostAlertsAndRecoveryKeepCauseAndLatestCheckSeparate(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	repos := manager.Repositories()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	checkID := "placement:control:web-host"
	require.NoError(t, repos.Checks.Create(t.Context(), &domain.Check{CheckID: checkID, Name: "任意名称", Kind: domain.CheckKindExternal, Enabled: true}))
	require.NoError(t, repos.Results.Insert(t.Context(), &domain.CheckResult{ResultID: "failed", CheckID: checkID, Status: "down", ErrorMessage: "health check failed", RawError: "x509: 原始错误\ncertificate expired", CheckedAt: now.Add(-time.Hour)}))
	for i := 0; i < 20; i++ {
		require.NoError(t, repos.Results.Insert(t.Context(), &domain.CheckResult{ResultID: fmt.Sprintf("success-%d", i), CheckID: checkID, Status: "ok", Success: true, CheckedAt: now.Add(time.Duration(i) * time.Second)}))
	}
	for _, id := range []string{checkID, "host:AGENT01:cpu"} {
		require.NoError(t, repos.Alerts.CreateRule(t.Context(), &domain.AlertRule{RuleID: "default:" + id, CheckID: id, Enabled: true}))
		require.NoError(t, repos.Alerts.UpsertState(t.Context(), &domain.AlertState{RuleID: "default:" + id, CheckID: id, Status: domain.AlertStatusFiring, DedupeKey: id, TriggeredAt: &now, UpdatedAt: now}))
	}
	require.NoError(t, repos.Alerts.CreateEvent(t.Context(), &domain.AlertEvent{EventID: "host-alert", RuleID: "default:host:AGENT01:cpu", CheckID: "host:AGENT01:cpu", EventType: domain.AlertEventTriggered, Message: "状态：新告警\n问题：CPU 使用率 95.0%，超过阈值 80.0%", Payload: `{"agent_id":"AGENT01","metric":"cpu","value":95,"threshold":80}`, CreatedAt: now}))
	view, err := (Builder{Checks: repos.Checks, Results: repos.Results, Alerts: repos.Alerts, Now: func() time.Time { return now }}).Build(t.Context(), "crypto")
	require.NoError(t, err)
	require.Len(t, view.Alerts, 2, "global host/component alerts remain visible when a space is selected")
	var hostFound bool
	for _, alert := range view.Alerts {
		if alert.Object.Type == "host" {
			hostFound = true
			require.Equal(t, "AGENT01", alert.Object.AgentId)
			require.Equal(t, "CPU 使用率 95.0%，超过阈值 80%", alert.Reason, "主机告警原因来自事件的结构化指标值，不是推送正文")
			require.Equal(t, "warning", alert.Severity)
		} else {
			require.Equal(t, "web-host", alert.Object.ComponentId)
			require.Equal(t, "x509: 原始错误\ncertificate expired", alert.RawError)
			require.Equal(t, stamp(now.Add(19*time.Second)), alert.LastCheckedAt)
			require.Equal(t, stamp(now), alert.TriggeredAt)
			require.True(t, alert.Recovering, "latest probe passed while the alert waits for recovery confirmation")
			require.Equal(t, "任意名称", alert.Title, "alert titles follow the check name used by notifications")
			require.Equal(t, "critical", alert.Severity)
		}
	}
	require.True(t, hostFound)
}

func TestUnknownTopologyNeverInventsComponentsAndNamesNeverMatchSubstrings(t *testing.T) {
	view, err := (Builder{}).Build(t.Context(), "")
	require.NoError(t, err)
	require.False(t, view.TopologyKnown)
	require.Empty(t, view.Components)
	object := alertObject("crypto", "business:random-factor-collector", observability.Overview{})
	require.Equal(t, "business", object.Type)
	require.Empty(t, object.ComponentId)
	require.Equal(t, "random-factor-collector", componentName(observability.Overview{}, "random-factor-collector"))
	require.Equal(t, "监控上报正常；健康检查正常", ChineseReason("reporter fresh; health check ok"))
}

func TestDisabledHostAgentDoesNotTurnAnEnabledHostIntoSilenceFailure(t *testing.T) {
	facts := observability.Overview{TopologyKnown: true, Topology: &domain.TopologySnapshot{Hosts: []domain.TopologyHost{{HostID: "control", Status: "enabled"}}, Placements: []domain.TopologyPlacement{{HostID: "control", ComponentID: "host-agent", Status: "disabled"}}}, Hosts: []observability.HostStatus{{HostID: "control", AgentID: "AB12", Status: "down", LastSeenAt: time.Now().Add(-time.Hour)}}, GatewaySignals: []observability.GatewaySignal{{HostID: "control", Kind: "heartbeat", Status: "healthy", Reason: "主机网关心跳正常"}}}
	view := projectFacts(facts)
	require.Equal(t, "healthy", view.Hosts[0].Status)
	require.Empty(t, view.Alerts)
	facts.Topology.Placements[0].Status = "enabled"
	view = projectFacts(facts)
	require.Equal(t, "down", view.Hosts[0].Status)
	require.Len(t, view.Alerts, 1)
	facts.Topology.Hosts[0].Status = "disabled"
	view = projectFacts(facts)
	require.Equal(t, "disabled", view.Hosts[0].Status)
	require.Empty(t, view.Alerts)
}

// 主机从拓扑中删除（改名后移除旧主机）：旧主机上的采集代理不再显示，也不再报失联；拓扑未知时照常显示。
func TestAgentOfRemovedHostIsNeitherListedNorAlerted(t *testing.T) {
	facts := observability.Overview{TopologyKnown: true, Topology: &domain.TopologySnapshot{Hosts: []domain.TopologyHost{{HostID: "overseas", Status: "enabled"}}}, Hosts: []observability.HostStatus{
		{HostID: "compute-1", AgentID: "OLD1", Status: "down", LastSeenAt: time.Now().Add(-time.Hour)},
		{HostID: "overseas", AgentID: "NEW1", Status: "healthy", LastSeenAt: time.Now()},
	}}
	view := projectFacts(facts)
	require.Len(t, view.Hosts, 1)
	require.Equal(t, "overseas", view.Hosts[0].HostId)
	require.Empty(t, view.Alerts)

	facts.Topology = nil
	view = projectFacts(facts)
	require.Len(t, view.Hosts, 2)
	require.Len(t, view.Alerts, 1)
}

func TestGatewayPendingTimeKeepsItsSnapshotIdentity(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	pending := now.Add(-time.Minute)
	facts := observability.Overview{GeneratedAt: now, TopologyKnown: true, Topology: &domain.TopologySnapshot{Hosts: []domain.TopologyHost{{HostID: "control", Status: "enabled"}}}, GatewaySignals: []observability.GatewaySignal{{HostID: "control", Kind: "route_sync", Status: "unknown", PendingSince: pending, ExpectedHash: "new", AppliedHash: "old", CheckedAt: now}}}
	hosts := projectHosts(facts)
	require.Len(t, hosts, 1)
	require.Len(t, hosts[0].GatewaySignals, 1)
	signal := hosts[0].GatewaySignals[0]
	require.Equal(t, pending.Format(time.RFC3339Nano), signal.PendingSince)
	require.Equal(t, "new", signal.ExpectedHash)
	require.Equal(t, "old", signal.AppliedHash)
}

func TestPipelineSkipsSystemMonitoringDatasets(t *testing.T) {
	stages := projectPipeline(observability.Overview{Datasets: []observability.DatasetFrequencyStatus{
		{Producer: "storage", SpaceID: "crypto", DatasetID: "dataset_crypto_kline_1m", Freq: "1m", Status: "healthy"},
		{Producer: "storage", SpaceID: "mooxsys", DatasetID: "dataset_mooxsys_probe", Freq: "30s", Status: "degraded"},
		{Producer: "storage_view", SpaceID: "mooxsys", DatasetID: "view_mooxsys_host_resource", Freq: "1m", Status: "degraded"},
	}})
	var datasets []string
	for _, stage := range stages {
		for _, dataset := range stage.GetDatasets() {
			datasets = append(datasets, dataset.GetSpaceId()+"/"+dataset.GetDatasetId())
		}
	}
	require.Equal(t, []string{"crypto/dataset_crypto_kline_1m"}, datasets, "mooxsys 系统监控数据不进入业务数据链路")
}

func TestSeriesLimitAlertIsAWarning(t *testing.T) {
	object := alertObject("mooxsys", "metrics_ingest:series_limit", observability.Overview{})
	require.Equal(t, "metrics_ingest", object.Kind)
	require.Equal(t, "warning", alertSeverity("metrics_ingest:series_limit", object))
	require.Equal(t, "critical", alertSeverity("dataset:storage:bars:1m", alertObject("crypto", "dataset:storage:bars:1m", observability.Overview{})), "数据集告警仍是严重")
}
