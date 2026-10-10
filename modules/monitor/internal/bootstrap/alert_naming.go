package bootstrap

import (
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerttext"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
)

// datasetSubject names what a dataset check watches: the dataset's human
// name when Storage knows it, otherwise its ID with the frequency.
func datasetSubject(name, datasetID, freq string) string {
	if strings.TrimSpace(name) != "" {
		return name
	}
	return datasetID + "（" + alerttext.Frequency(freq) + "）"
}

// datasetCheckName names a dataset freshness check by what produces the data.
func datasetCheckName(producer, subject string) string {
	switch producer {
	case "collector":
		return subject + " · 采集"
	case "storage":
		return subject + " · 写入存储"
	case "storage_view":
		return subject + " · 结果视图"
	case "factor":
		return subject + " · 因子计算"
	default:
		return subject + " · " + producer
	}
}

func klineCheckName(subject string) string { return subject + " · K线结果" }

func reporterCheckName(service, node string) string {
	return alerttext.Service(service) + "（" + alerttext.Node(node) + "）· 运行指标上报"
}

// reporterReasonText explains a reporter status to people.
func reporterReasonText(status string) string {
	switch status {
	case "healthy":
		return "运行指标正常上报"
	case "stale":
		return "运行指标已中断：服务可能卡住、重启中，或无法连接消息总线"
	case "missing":
		return "没有收到该服务的运行指标：服务可能未启动，或无法连接消息总线"
	default:
		return "还没有收到该服务的运行指标"
	}
}

// klineReasonText explains a K-line freshness report to people; the machine
// diagnostic stays in the check result's details.
func klineReasonText(report monmetrics.KlineFreshnessReport, now time.Time) string {
	switch report.Reason {
	case "fresh":
		return "数据按时更新"
	case "no_observation":
		return "还没有收到任何结果数据：请确认采集任务在运行、结果视图已生成"
	case "subject_catalog_unavailable":
		return "无法读取采集对象清单，暂时无法判断哪些对象落后"
	case "task_result_error":
		return "采集任务的结果状态异常，请检查采集任务"
	case "view_behind_latest_completed_period":
		if report.OldestDataTime.IsZero() {
			return fmt.Sprintf("采集已完成到 %s 这一期，但还没有看到该结果的数据（已等待 %s）",
				alerttext.Time(report.Rule.LatestCompletedPeriod), alerttext.Duration(report.StaleAge))
		}
		return fmt.Sprintf("采集已完成到 %s 这一期，但结果只更新到 %s，落后 %s",
			alerttext.Time(report.Rule.LatestCompletedPeriod), alerttext.Time(report.OldestDataTime), alerttext.Duration(report.StaleAge))
	case "business_data_stale":
		if report.ViewStale {
			return fmt.Sprintf("整体停止更新：最新数据停在 %s，下一根K线已逾期 %s", alerttext.Time(report.OldestDataTime), alerttext.Duration(report.StaleAge))
		}
		var parts []string
		if lagging := report.StaleCount - report.MissingCount; lagging > 0 {
			part := fmt.Sprintf("%d 个对象数据落后", lagging)
			if names := alerttext.Subjects(report.StaleSubjects, lagging); names != "" {
				part += "（" + names + "）"
			}
			if report.StaleAge > 0 {
				part += "，最久已逾期 " + alerttext.Duration(report.StaleAge)
			}
			parts = append(parts, part)
		}
		if report.MissingCount > 0 {
			parts = append(parts, fmt.Sprintf("%d 个对象没有任何数据", report.MissingCount))
		}
		if len(parts) == 0 {
			return "部分对象数据落后"
		}
		return strings.Join(parts, "；")
	default:
		return report.Reason
	}
}

// datasetReasonText explains a dataset freshness status to people. The
// overview's reasons are codes that the health view and web UI translate;
// alerts need whole sentences with Beijing times.
func datasetReasonText(dataset monitorobservability.DatasetFrequencyStatus, now time.Time) string {
	switch {
	case dataset.Status == "healthy" && dataset.OutputWatermarkAt.IsZero():
		return "运行正常，本周期没有产出数据"
	case dataset.Status == "healthy":
		return "数据按时更新"
	case dataset.Reason == "run stale":
		return fmt.Sprintf("已 %s没有运行（上次运行 %s）", alerttext.Duration(now.Sub(dataset.LastRunAt)), alerttext.Time(dataset.LastRunAt))
	case dataset.Reason == "success stale":
		return fmt.Sprintf("一直运行失败：已 %s没有成功（上次成功 %s）", alerttext.Duration(now.Sub(dataset.LastSuccessAt)), alerttext.Time(dataset.LastSuccessAt))
	case dataset.Reason == "inventory_stale":
		return "数据生产服务超过 10 分钟没有更新数据集清单，可能已停止"
	case dataset.Reason == "尚未上报":
		return "还没有收到任何运行记录"
	case dataset.Reason == "尚无成功运行":
		return "已在运行，但还没有成功过"
	case dataset.Status == "stale" && !dataset.OutputWatermarkAt.IsZero():
		return fmt.Sprintf("数据停止更新：最新数据停在 %s，已落后 %s", alerttext.Time(dataset.OutputWatermarkAt), alerttext.Duration(time.Duration(dataset.LagSeconds)*time.Second))
	default:
		return dataset.Reason
	}
}
