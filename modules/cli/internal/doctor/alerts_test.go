package doctor

import (
	"encoding/json"
	"testing"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	core "github.com/mooyang-code/moox/packages/doctor"
	"github.com/stretchr/testify/require"
)

func healthyOverview() *monitorpb.HealthOverview {
	return &monitorpb.HealthOverview{
		GeneratedAt: "2026-10-24T03:00:00Z",
		System:      &monitorpb.HealthSystem{Health: "healthy"},
		Pipeline: []*monitorpb.HealthPipelineStage{{Id: "view", Name: "视图", Datasets: []*monitorpb.HealthDataset{
			{Producer: "storage_view", DatasetId: "view_a", Freq: "1h", Status: "healthy"},
		}}},
		BusinessChecks: []*monitorpb.HealthBusinessCheck{{CheckId: "c1", Kind: "module", Status: "healthy"}, {CheckId: "c2", Kind: "kline_freshness", Status: "disabled"}},
		Components:     []*monitorpb.HealthComponent{{HostId: "control", ComponentId: "admin", Status: "healthy"}, {HostId: "control", ComponentId: "x", Status: "unchecked"}},
		Hosts:          []*monitorpb.HealthHost{{HostId: "control", Status: "healthy", Address: "203.0.113.10"}},
	}
}

// 全部健康（disabled、unchecked 不算问题）时没有条目，结论正常。
func TestBuildAlertsReportHealthy(t *testing.T) {
	report := BuildAlertsReport(healthyOverview())
	require.Equal(t, core.ConclusionHealthy, report.Conclusion)
	require.Empty(t, report.Items)
	require.NotEmpty(t, report.Notes, "要说明报告没有覆盖策略与因子")
}

// 当前告警、不健康的数据集、业务检查、组件、主机各成一项；严重程度决定结论；
// 已经有告警的数据集不重复列出；“积累中”只提示不影响结论；输出不带主机地址。
func TestBuildAlertsReportCollectsEveryUnhealthySource(t *testing.T) {
	overview := healthyOverview()
	overview.Summary = &monitorpb.HealthSummary{AlertCount: 1}
	overview.Alerts = []*monitorpb.HealthAlert{{
		Id: "a1", Severity: "critical", Title: "数据集 view_b", Reason: "数据生产端长时间未更新", RawError: "producer stale",
		TriggeredAt: "2026-10-24T02:00:00Z", Object: &monitorpb.HealthObject{Type: "dataset", DatasetId: "view_b", Freq: "1h"},
	}}
	overview.Pipeline[0].Datasets = append(overview.Pipeline[0].Datasets,
		&monitorpb.HealthDataset{Producer: "storage_view", DatasetId: "view_b", Freq: "1h", Status: "degraded", Reason: "数据生产端长时间未更新"},
		&monitorpb.HealthDataset{Producer: "factor", DatasetId: "factor_c", Freq: "1m", Status: "degraded", Reason: "warming"},
		&monitorpb.HealthDataset{Producer: "collector", DatasetId: "ds_d", Freq: "1m", Status: "degraded", Reason: "success stale"},
	)
	overview.BusinessChecks = append(overview.BusinessChecks, &monitorpb.HealthBusinessCheck{CheckId: "c3", Name: "行情采集", Status: "down", Reason: "Collector 采集计划指标已停止上报"})
	overview.Components = append(overview.Components, &monitorpb.HealthComponent{HostId: "storage", ComponentId: "storage-node", Status: "unknown", Reason: "从未收到监控上报"})
	overview.Hosts = append(overview.Hosts, &monitorpb.HealthHost{HostId: "overseas", Status: "down", Reason: "主机采集代理停止上报"})

	report := BuildAlertsReport(overview)
	require.Equal(t, core.ConclusionUnhealthy, report.Conclusion)
	require.Equal(t, int32(1), report.AlertCount)

	bySource := map[string][]AlertItem{}
	for _, item := range report.Items {
		bySource[item.Source] = append(bySource[item.Source], item)
	}
	require.Len(t, bySource[AlertSourceAlert], 1)
	require.Equal(t, AlertSeverityCritical, bySource[AlertSourceAlert][0].Severity)
	require.Contains(t, bySource[AlertSourceAlert][0].Hint, "生产端停止上报")

	datasets := bySource[AlertSourceDataset]
	require.Len(t, datasets, 2, "view_b 已有告警不重复列出")
	severities := map[string]string{}
	for _, item := range datasets {
		severities[item.Object] = item.Severity
	}
	require.Equal(t, AlertSeverityInfo, severities["/factor_c/1m"], "积累中只提示")
	require.Equal(t, AlertSeverityWarning, severities["/ds_d/1m"])

	require.Len(t, bySource[AlertSourceBusinessCheck], 1)
	require.Equal(t, AlertSeverityCritical, bySource[AlertSourceBusinessCheck][0].Severity)
	require.Equal(t, "行情采集", bySource[AlertSourceBusinessCheck][0].Title)
	require.Len(t, bySource[AlertSourceComponent], 1)
	require.Len(t, bySource[AlertSourceHost], 1)

	raw, err := RenderAlerts(report, "json")
	require.NoError(t, err)
	require.NotContains(t, string(raw), "203.0.113.10", "不输出主机地址")
	var decoded AlertsReport
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Len(t, decoded.Items, len(report.Items))
}

// 只有警告时结论是降级；只有“积累中”提示时仍是正常。
func TestBuildAlertsReportConclusionByWorstSeverity(t *testing.T) {
	overview := healthyOverview()
	overview.Pipeline[0].Datasets = append(overview.Pipeline[0].Datasets, &monitorpb.HealthDataset{DatasetId: "w", Freq: "1m", Status: "degraded", Reason: "warming"})
	require.Equal(t, core.ConclusionHealthy, BuildAlertsReport(overview).Conclusion)

	overview.Components = append(overview.Components, &monitorpb.HealthComponent{HostId: "h", ComponentId: "c", Status: "degraded"})
	require.Equal(t, core.ConclusionDegraded, BuildAlertsReport(overview).Conclusion)

	require.Equal(t, core.ConclusionInconclusive, BuildAlertsReport(nil).Conclusion)
}

func TestRenderAlertsFormats(t *testing.T) {
	overview := healthyOverview()
	overview.Hosts = append(overview.Hosts, &monitorpb.HealthHost{HostId: "overseas", Status: "down", Reason: "主机采集代理停止上报"})
	report := BuildAlertsReport(overview)

	text, err := RenderAlerts(report, "text")
	require.NoError(t, err)
	require.Contains(t, string(text), "[critical/host] 主机 overseas")
	require.Contains(t, string(text), "提示：")

	markdown, err := RenderAlerts(report, "markdown")
	require.NoError(t, err)
	require.Contains(t, string(markdown), "# MooX 告警清单")

	empty, err := RenderAlerts(BuildAlertsReport(healthyOverview()), "text")
	require.NoError(t, err)
	require.Contains(t, string(empty), "没有需要处理的问题")

	_, err = RenderAlerts(report, "yaml")
	require.Error(t, err)
}
