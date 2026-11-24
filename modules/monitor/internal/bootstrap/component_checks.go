package bootstrap

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerttext"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/packages/events"
)

// componentCheckPrefixes 是按部署生成的检查：部署停用或删除后不再生成，已有的检查随之停用；重新部署时恢复启用。
var componentCheckPrefixes = []string{"reporter:", "ready:", "restart:"}

func componentCheck(checkID string) bool {
	for _, prefix := range componentCheckPrefixes {
		if strings.HasPrefix(checkID, prefix) {
			return true
		}
	}
	return false
}

// componentItem 是一项组件检查；frozen 表示这一轮保持原状态（无法确认）。
type componentItem struct {
	businessFreshnessItem
	frozen bool
}

// componentHealthItems 按各组件的上报生成三类检查（设计 3.8，组件健康只看上报）：
//   - 停止上报 reporter:{主机}:{组件}：组件自己经 packages/report 的上报超过无数据时限，或从没上报过。
//     host-agent 由主机类检查（采集器停报、主机失联）负责，因子引擎与归档 worker 由管理端按心跳评估的在线状态检查负责，不重复生成；
//   - 未就绪 ready:{主机}:{组件}：上报新鲜时按上报里的就绪判断；停止上报期间保持原状态，由停止上报那一条说明；
//   - 反复重启 restart:{主机}:{组件}：10 分钟内启动标识变了 3 次（全新部署的第一次启动不算变化）。
//
// 三类都依赖观测输入：观测输入中断或刚恢复时随其余依赖观测输入的检查一起冻结（见 observationIndependent）。
func componentHealthItems(services []monitorobservability.ServiceStatus) []componentItem {
	var out []componentItem
	for _, service := range services {
		if !service.Enabled || service.Source == monitorobservability.SourceHostSnapshot {
			continue
		}
		subject := alerttext.Service(service.ServiceName) + "（" + alerttext.Node(service.NodeID) + "）"
		suffix := service.NodeID + ":" + service.ServiceName
		if service.Source == monitorobservability.SourceReporter {
			out = append(out, componentItem{businessFreshnessItem: businessFreshnessItem{
				spaceID: events.SystemSpaceID, checkID: "reporter:" + suffix,
				name:    reporterCheckName(service.ServiceName, service.NodeID),
				success: service.ReporterStatus == "healthy", reason: reporterReasonText(service.ReporterStatus),
			}})
		}
		ready := businessFreshnessItem{spaceID: events.SystemSpaceID, checkID: "ready:" + suffix, name: checkName(subject, "就绪")}
		switch {
		case service.ReporterStatus != "healthy":
			out = append(out, componentItem{businessFreshnessItem: ready, frozen: true})
		case service.Readiness == monmetrics.ReadinessReady:
			ready.success, ready.reason = true, "组件已就绪"
			out = append(out, componentItem{businessFreshnessItem: ready})
		case service.Readiness == monmetrics.ReadinessNotReady:
			ready.reason = "组件未就绪：" + firstNonEmptyString(service.NotReadyReason, "组件报告未就绪，没有说明原因")
			out = append(out, componentItem{businessFreshnessItem: ready})
		}
		if service.ReporterStatus == "missing" {
			continue
		}
		restart := businessFreshnessItem{spaceID: events.SystemSpaceID, checkID: "restart:" + suffix, name: checkName(subject, "反复重启"), success: true, reason: "最近 10 分钟没有反复重启"}
		if service.BootChanges >= monmetrics.RestartChanges {
			restart.success = false
			restart.reason = fmt.Sprintf("最近 10 分钟重启了 %d 次（启动标识变了 %d 次）：进程可能在退出后被反复拉起，查看 %s 的日志找退出原因", service.BootChanges, service.BootChanges, service.ServiceName)
		}
		out = append(out, componentItem{businessFreshnessItem: restart})
	}
	return out
}
