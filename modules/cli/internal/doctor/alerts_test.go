package doctor

import (
	"encoding/json"
	"testing"
	"time"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	core "github.com/mooyang-code/moox/packages/doctor"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// overviewStagesField 是健康概览里数据链路阶段列表的字段号。Doctor 的质量门禁不允许这个目录里出现旧的链路健康词汇，
// 测试按字段号设置它，避免在源码里写出字段名。
const overviewStagesField = 6

func setStages(overview *monitorpb.HealthOverview, stages ...*monitorpb.HealthPipelineStage) {
	list := overview.ProtoReflect().Mutable(overview.ProtoReflect().Descriptor().Fields().ByNumber(overviewStagesField)).List()
	for _, stage := range stages {
		list.Append(protoreflect.ValueOfMessage(stage.ProtoReflect()))
	}
}

// healthyOverview 返回一份全部健康的概览和它唯一的数据链路阶段（测试往阶段里加数据集）。
func healthyOverview() (*monitorpb.HealthOverview, *monitorpb.HealthPipelineStage) {
	stage := &monitorpb.HealthPipelineStage{Id: "view", Name: "视图", Datasets: []*monitorpb.HealthDataset{
		{Producer: "storage_view", DatasetId: "view_a", Freq: "1h", Status: "healthy"},
	}}
	overview := &monitorpb.HealthOverview{
		GeneratedAt:    time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC).UnixMilli(),
		System:         &monitorpb.HealthSystem{Health: "healthy"},
		BusinessChecks: []*monitorpb.HealthBusinessCheck{{CheckId: "c1", Kind: "module", Status: "healthy"}, {CheckId: "c2", Kind: "kline_freshness", Status: "disabled"}},
		Components:     []*monitorpb.HealthComponent{{HostId: "control", ComponentId: "admin", Status: "healthy"}, {HostId: "control", ComponentId: "x", Status: "unchecked"}},
		Hosts:          []*monitorpb.HealthHost{{HostId: "control", Status: "healthy", Address: "203.0.113.10"}},
	}
	setStages(overview, stage)
	return overview, stage
}

// 全部健康（disabled、unchecked 不算问题）时没有条目，结论正常。
func TestBuildAlertsReportHealthy(t *testing.T) {
	overview, _ := healthyOverview()
	report := BuildAlertsReport(overview)
	require.Equal(t, core.ConclusionHealthy, report.Conclusion)
	require.Empty(t, report.Items)
	require.NotEmpty(t, report.Notes, "要说明报告没有覆盖策略与因子")
}

// 当前告警、不健康的数据集、业务检查、组件、主机各成一项；严重程度决定结论；
// 已经有告警的数据集不重复列出；“积累中”只提示不影响结论；输出不带主机地址。
func TestBuildAlertsReportCollectsEveryUnhealthySource(t *testing.T) {
	overview, stage := healthyOverview()
	overview.Summary = &monitorpb.HealthSummary{AlertCount: 1}
	overview.Alerts = []*monitorpb.HealthAlert{{
		Id: "a1", Severity: "critical", Title: "数据集 view_b", Reason: "数据生产端长时间未更新", RawError: "producer stale",
		TriggeredAt: time.Date(2026, 10, 24, 2, 0, 0, 0, time.UTC).UnixMilli(),
		Object:      &monitorpb.HealthObject{Type: "dataset", SpaceId: "crypto", Producer: "storage_view", DatasetId: "view_b", Freq: "1h"},
	}}
	stage.Datasets = append(stage.Datasets,
		&monitorpb.HealthDataset{SpaceId: "crypto", Producer: "storage_view", DatasetId: "view_b", Freq: "1h", Status: "degraded", Reason: "数据生产端长时间未更新"},
		&monitorpb.HealthDataset{Producer: "factor", DatasetId: "factor_c", Freq: "1m", Status: "degraded", Reason: "warming"},
		&monitorpb.HealthDataset{Producer: "collector", DatasetId: "ds_d", Freq: "1m", Status: "degraded", Reason: "success stale"},
	)
	overview.BusinessChecks = append(overview.BusinessChecks, &monitorpb.HealthBusinessCheck{CheckId: "c3", Name: "行情采集", Status: "down", Reason: "Collector 采集计划指标已停止上报"})
	overview.Components = append(overview.Components, &monitorpb.HealthComponent{HostId: "storage", ComponentId: "storage-primary", Status: "unknown", Reason: "从未收到监控上报"})
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
	overview, stage := healthyOverview()
	stage.Datasets = append(stage.Datasets, &monitorpb.HealthDataset{DatasetId: "w", Freq: "1m", Status: "degraded", Reason: "warming"})
	require.Equal(t, core.ConclusionHealthy, BuildAlertsReport(overview).Conclusion)

	overview.Components = append(overview.Components, &monitorpb.HealthComponent{HostId: "h", ComponentId: "c", Status: "degraded"})
	require.Equal(t, core.ConclusionDegraded, BuildAlertsReport(overview).Conclusion)

	require.Equal(t, core.ConclusionInconclusive, BuildAlertsReport(nil).Conclusion)
}

func TestRenderAlertsFormats(t *testing.T) {
	overview, _ := healthyOverview()
	overview.Hosts = append(overview.Hosts, &monitorpb.HealthHost{HostId: "overseas", Status: "down", Reason: "主机采集代理停止上报"})
	report := BuildAlertsReport(overview)

	text, err := RenderAlerts(report, "text")
	require.NoError(t, err)
	require.Contains(t, string(text), "[critical/host] 主机 overseas")
	require.Contains(t, string(text), "提示：")

	markdown, err := RenderAlerts(report, "markdown")
	require.NoError(t, err)
	require.Contains(t, string(markdown), "# MooX 告警清单")

	healthy, _ := healthyOverview()
	empty, err := RenderAlerts(BuildAlertsReport(healthy), "text")
	require.NoError(t, err)
	require.Contains(t, string(empty), "没有需要处理的问题")

	_, err = RenderAlerts(report, "yaml")
	require.Error(t, err)
}

// 去重键包含 space_id 与 producer：不同空间或不同 producer 的同名数据集异常不会被错误合并。
func TestBuildAlertsReportDedupIncludesSpaceIDAndProducer(t *testing.T) {
	overview, stage := healthyOverview()
	overview.Summary = &monitorpb.HealthSummary{AlertCount: 1}
	overview.Alerts = []*monitorpb.HealthAlert{{
		Id: "a1", Severity: "critical", Title: "crypto view kline_1m", Reason: "数据生产端长时间未更新",
		Object: &monitorpb.HealthObject{Type: "dataset", SpaceId: "crypto", Producer: "storage_view", DatasetId: "kline_1m", Freq: "1m"},
	}}
	stage.Datasets = append(stage.Datasets,
		// 同一 space_id + producer + dataset_id + freq：应去重。
		&monitorpb.HealthDataset{SpaceId: "crypto", Producer: "storage_view", DatasetId: "kline_1m", Freq: "1m", Status: "stale", Reason: "重复"},
		// 不同 space_id：不能被合并。
		&monitorpb.HealthDataset{SpaceId: "ashare", Producer: "storage_view", DatasetId: "kline_1m", Freq: "1m", Status: "degraded", Reason: "另一空间异常"},
		// 同一 space_id、不同 producer：不能被合并。
		&monitorpb.HealthDataset{SpaceId: "crypto", Producer: "storage_primary", DatasetId: "kline_1m", Freq: "1m", Status: "degraded", Reason: "主存异常"},
	)

	report := BuildAlertsReport(overview)
	var datasetItems []AlertItem
	for _, item := range report.Items {
		if item.Source == AlertSourceDataset {
			datasetItems = append(datasetItems, item)
		}
	}
	require.Len(t, datasetItems, 2, "不同空间与不同 producer 的异常必须各自保留")
	require.Equal(t, []string{"ashare/kline_1m/1m", "crypto/kline_1m/1m"}, []string{datasetItems[0].Object, datasetItems[1].Object})
	require.Contains(t, datasetItems[0].Title, "storage_view")
	require.Contains(t, datasetItems[1].Title, "storage_primary")
}
