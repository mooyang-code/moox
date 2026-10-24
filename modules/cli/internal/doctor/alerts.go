package doctor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	core "github.com/mooyang-code/moox/packages/doctor"
)

// 告警条目的来源。
const (
	AlertSourceAlert         = "alert"
	AlertSourceDataset       = "dataset"
	AlertSourceBusinessCheck = "business_check"
	AlertSourceComponent     = "component"
	AlertSourceHost          = "host"
)

// 告警条目的严重程度：critical 是服务不可用或数据中断，warning 是需要关注的风险，info 只提示、不影响结论。
const (
	AlertSeverityCritical = "critical"
	AlertSeverityWarning  = "warning"
	AlertSeverityInfo     = "info"
)

// datasetWarmingReason 是数据集“积累中”的原因码：按设计只产出部分结果，页面显示降级但不是故障，不告警。
const datasetWarmingReason = "warming"

// AlertItem 是一条需要处理的问题。字段面向 Agent：Hint 给出该类问题最常见的原因和第一步，具体流程见 skills/moox/references/monitor-attention.md。
type AlertItem struct {
	Source   string `json:"source"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Reason   string `json:"reason,omitempty"`
	RawError string `json:"raw_error,omitempty"`
	Since    string `json:"since,omitempty"`
	Object   string `json:"object,omitempty"`
	Hint     string `json:"hint,omitempty"`
}

// AlertsReport 是 Monitor 健康概览里“需要处理”的部分：当前告警，以及不健康的数据集、业务检查、组件和主机。
type AlertsReport struct {
	GeneratedAt string          `json:"generated_at"`
	Conclusion  core.Conclusion `json:"conclusion"`
	SystemState string          `json:"system_health,omitempty"`
	AlertCount  int32           `json:"alert_count"`
	Items       []AlertItem     `json:"items"`
	// Notes 说明这份报告没有覆盖的部分，避免把“这里没有问题”当成“首页没有问题”。
	Notes []string `json:"notes,omitempty"`
}

// 按顺序取第一个命中原因文字的提示。
var alertHints = []struct{ keyword, hint string }{
	{"producer stale", "生产端停止上报：先确认对应视图或数据集还在（已删除的会被 Monitor 自动隐藏并清理）；仍在的看 Storage 是否刚重启（周期不短于 5 分钟的视图要等下一次提交才重新出现指标，Monitor 已容忍一个周期加 5 分钟）。"},
	{"run stale", "生产者超过容忍时间没有运行记录：看它的进程和日志。"},
	{"success stale", "生产者在运行但一直没有成功：看它日志里的错误。"},
	{"late_confirmed", "应收的最新一根 K 线晚于允许迟到仍没到：按 view-catchup.md 分别量 Primary、View、Factor 三段。"},
	{"inventory_stale", "期望的数据集清单读取失败：看 Collector、Factor 是否可用。"},
	{"尚未上报", "还没收到过上报：新对象等一个周期；一直如此就看生产端是否启动、EventBus 是否通。"},
	{"已停止上报", "指标上报停了：看对应组件进程和 EventBus。"},
	{"运行指标已中断", "运行指标上报中断：服务可能卡住、重启中，或连不上消息总线，看对应组件进程。"},
	{"采集代理", "主机采集代理停止上报：看主机是否在线、host-agent 进程是否在。"},
	{"失联", "主机失联：看主机是否在线、host-agent 进程是否在。"},
	{"暂不可用", "依赖的服务暂时不可用：多半是对方重启或发布中，等它恢复；持续不恢复再看对方进程和日志。"},
}

func alertHint(texts ...string) string {
	joined := strings.Join(texts, " ")
	for _, entry := range alertHints {
		if strings.Contains(joined, entry.keyword) {
			return entry.hint
		}
	}
	return ""
}

// statusSeverity 把健康状态映射为严重程度；healthy、disabled、unchecked 不是问题，返回空。
func statusSeverity(status string) string {
	switch status {
	case "", "healthy", "ok", "disabled", "unchecked":
		return ""
	case "down", "stale", "firing":
		return AlertSeverityCritical
	default:
		return AlertSeverityWarning
	}
}

func alertSeverity(severity string) string {
	if severity == AlertSeverityCritical {
		return AlertSeverityCritical
	}
	return AlertSeverityWarning
}

func objectLabel(object *monitorpb.HealthObject) string {
	if object == nil {
		return ""
	}
	parts := []string{}
	for _, part := range []string{object.GetType(), object.GetHostId(), object.GetComponentId(), object.GetProducer(), object.GetDatasetId(), object.GetFreq(), object.GetKind()} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "/")
}

// BuildAlertsReport 把健康概览整理成需要处理的问题清单。不读取任何配置或凭据，也不输出主机地址。
func BuildAlertsReport(overview *monitorpb.HealthOverview) AlertsReport {
	report := AlertsReport{Conclusion: core.ConclusionHealthy, Items: []AlertItem{}}
	if overview == nil {
		report.Conclusion = core.ConclusionInconclusive
		report.Notes = append(report.Notes, "Monitor 没有返回健康概览")
		return report
	}
	report.GeneratedAt = overview.GetGeneratedAt()
	report.SystemState = overview.GetSystem().GetHealth()
	report.AlertCount = overview.GetSummary().GetAlertCount()

	alerted := map[string]bool{}
	for _, alert := range overview.GetAlerts() {
		reason := alert.GetReason()
		if alert.GetRecovering() {
			reason += "（最近一次检查已通过，等待连续成功确认恢复）"
		}
		report.Items = append(report.Items, AlertItem{
			Source: AlertSourceAlert, Severity: alertSeverity(alert.GetSeverity()), Title: alert.GetTitle(), Reason: reason,
			RawError: alert.GetRawError(), Since: alert.GetTriggeredAt(), Object: objectLabel(alert.GetObject()),
			Hint: alertHint(alert.GetReason(), alert.GetRawError()),
		})
		if object := alert.GetObject(); object != nil && object.GetType() == "dataset" {
			alerted[object.GetDatasetId()+"/"+object.GetFreq()] = true
		}
	}
	for _, stage := range overview.GetPipeline() {
		for _, dataset := range stage.GetDatasets() {
			severity := statusSeverity(dataset.GetStatus())
			if severity == "" || alerted[dataset.GetDatasetId()+"/"+dataset.GetFreq()] {
				continue
			}
			if dataset.GetReason() == datasetWarmingReason {
				severity = AlertSeverityInfo
			}
			title := fmt.Sprintf("[%s] %s · %s · %s", stage.GetName(), dataset.GetProducer(), dataset.GetDatasetId(), dataset.GetFreq())
			report.Items = append(report.Items, AlertItem{
				Source: AlertSourceDataset, Severity: severity, Title: title, Reason: dataset.GetReason(), RawError: dataset.GetRawError(),
				Since: dataset.GetLastReportedAt(), Object: dataset.GetSpaceId() + "/" + dataset.GetDatasetId() + "/" + dataset.GetFreq(),
				Hint: alertHint(dataset.GetReason(), dataset.GetRawError()),
			})
		}
	}
	for _, check := range overview.GetBusinessChecks() {
		if severity := statusSeverity(check.GetStatus()); severity != "" {
			title := check.GetName()
			if title == "" {
				title = check.GetKind() + " · " + check.GetModule()
			}
			report.Items = append(report.Items, AlertItem{
				Source: AlertSourceBusinessCheck, Severity: severity, Title: title, Reason: check.GetReason(), RawError: check.GetRawError(),
				Since: check.GetCheckedAt(), Object: check.GetCheckId(), Hint: alertHint(check.GetReason(), check.GetRawError()),
			})
		}
	}
	for _, component := range overview.GetComponents() {
		if severity := statusSeverity(component.GetStatus()); severity != "" {
			report.Items = append(report.Items, AlertItem{
				Source: AlertSourceComponent, Severity: severity, Title: component.GetHostId() + " / " + component.GetComponentId(),
				Reason: component.GetReason(), Since: component.GetStatusSince(), Object: component.GetHostId() + "/" + component.GetComponentId(),
				Hint: alertHint(component.GetReason()),
			})
		}
	}
	for _, host := range overview.GetHosts() {
		if severity := statusSeverity(host.GetStatus()); severity != "" {
			report.Items = append(report.Items, AlertItem{
				Source: AlertSourceHost, Severity: severity, Title: "主机 " + host.GetHostId(), Reason: host.GetReason(),
				Since: host.GetLastReportedAt(), Object: host.GetHostId(), Hint: alertHint(host.GetReason()),
			})
		}
	}

	for _, item := range report.Items {
		switch item.Severity {
		case AlertSeverityCritical:
			report.Conclusion = core.ConclusionUnhealthy
		case AlertSeverityWarning:
			if report.Conclusion != core.ConclusionUnhealthy {
				report.Conclusion = core.ConclusionDegraded
			}
		}
	}
	report.Notes = append(report.Notes, "首页“需要处理”还包含策略实例与因子任务的问题，这两类用控制台对应页面或 moox-cli 的策略、因子命令查看。")
	return report
}

// RenderAlerts 按格式输出告警报告：json（默认，给 Agent）、text、markdown。
func RenderAlerts(report AlertsReport, format string) ([]byte, error) {
	switch format {
	case "json":
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return nil, err
		}
		return append(raw, '\n'), nil
	case "text":
		return renderAlertsText(report, false), nil
	case "markdown":
		return renderAlertsText(report, true), nil
	default:
		return nil, fmt.Errorf("不支持的 doctor 输出格式 %q", format)
	}
}

func renderAlertsText(report AlertsReport, markdown bool) []byte {
	var out bytes.Buffer
	bullet, indent := "- ", "    "
	if markdown {
		fmt.Fprintf(&out, "# MooX 告警清单：%s\n\n", report.Conclusion)
	} else {
		fmt.Fprintf(&out, "MooX 告警清单 %s\n", report.Conclusion)
	}
	fmt.Fprintf(&out, "概览生成于 %s，系统状态 %s，当前告警 %d 条，合计 %d 项。\n\n", report.GeneratedAt, report.SystemState, report.AlertCount, len(report.Items))
	if len(report.Items) == 0 {
		out.WriteString("没有需要处理的问题。\n")
	}
	for _, item := range report.Items {
		fmt.Fprintf(&out, "%s[%s/%s] %s\n", bullet, item.Severity, item.Source, item.Title)
		if item.Reason != "" {
			fmt.Fprintf(&out, "%s原因：%s\n", indent, item.Reason)
		}
		if item.RawError != "" {
			fmt.Fprintf(&out, "%s原始信息：%s\n", indent, item.RawError)
		}
		if item.Since != "" {
			fmt.Fprintf(&out, "%s起始/最近：%s\n", indent, item.Since)
		}
		if item.Hint != "" {
			fmt.Fprintf(&out, "%s提示：%s\n", indent, item.Hint)
		}
	}
	for _, note := range report.Notes {
		fmt.Fprintf(&out, "\n注：%s\n", note)
	}
	return out.Bytes()
}
