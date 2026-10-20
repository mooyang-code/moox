package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerttext"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/packages/report"
)

// workerPresenceSources 是上报外部程序在线状态的管理端：指标前缀 → 检查对象与展示名。
var workerPresenceSources = []struct {
	metricModule, object, title string
}{
	{"factor", "factor-engine", "因子引擎"},
	{"archive", "archive-worker", "归档 worker"},
}

// WorkerPresenceName 是外部程序在线检查的名称，告警标题形如“因子引擎在线状态 · 失败”。
func WorkerPresenceName(object string) string {
	for _, source := range workerPresenceSources {
		if source.object == object {
			return source.title + "在线状态"
		}
	}
	return object + " 在线状态"
}

// buildWorkerPresence 把 factor-mgr、archive-mgr 上报的外部程序在线状态转成业务检查：离线时失败，原因写明机器与最后心跳；
// “不需要”（没有启用的因子集 / 归档任务）视为通过；管理端本身停止上报时视为通过并说明，管理端的故障由主机守护与服务检查告警。
func (b Builder) buildWorkerPresence(ctx context.Context, spaceID string, now time.Time) ([]BusinessStatus, error) {
	if spaceID != "" && spaceID != monmetrics.InternalMetricSpaceID {
		return nil, nil
	}
	if b.Metrics == nil || b.Metrics.Catalog() == nil {
		return nil, nil
	}
	var out []BusinessStatus
	for _, source := range workerPresenceSources {
		stateName := "moox_" + source.metricModule + report.PresenceStateSuffix
		series, err := b.Metrics.Catalog().FindSeriesAt(ctx, "", "", stateName, "", 100, now)
		if err != nil {
			return nil, err
		}
		if len(series) == 0 {
			continue // 管理端没有部署或还没上报过
		}
		var current *monmetrics.MetricLatest
		stale := true
		for _, item := range series {
			latest, err := b.Metrics.Latest(ctx, item.SeriesID)
			if err != nil {
				return nil, err
			}
			// 换机后旧标签的序列不再更新：取最近一次上报的那条。
			if latest != nil && (current == nil || latest.ObservedAt.After(current.ObservedAt)) {
				current, stale = latest, item.IsStale
			}
		}
		if current == nil {
			continue
		}
		worker := labelValue(current.LabelsJSON, "worker")
		status, reason := "healthy", source.title+"在线"
		switch {
		case stale:
			reason = "管理端没有按时上报在线状态，暂时无法判断" + source.title + "是否在线"
		case int(current.Value) == report.PresenceNotRequired:
			reason = "没有需要" + source.title + "工作的任务"
		case int(current.Value) == report.PresenceOffline:
			status = "down"
			reason = source.title + "离线：" + b.lastHeartbeatText(ctx, source.metricModule, worker, now)
		case worker != "":
			reason = source.title + " " + worker + " 在线"
		}
		out = append(out, BusinessStatus{
			SpaceID: monmetrics.InternalMetricSpaceID, Kind: "worker_presence", Module: source.object,
			Status: status, Reason: reason, LastCheckedAt: now,
		})
	}
	return out, nil
}

// lastHeartbeatText 写明离线的机器与最后心跳时间。
func (b Builder) lastHeartbeatText(ctx context.Context, metricModule, worker string, now time.Time) string {
	if worker == "" {
		return "从未收到心跳，请检查运维机上的程序是否已安装并启动"
	}
	series, err := b.Metrics.Catalog().FindSeriesAt(ctx, "", "", "moox_"+metricModule+report.PresenceLastHeartbeatSuffix, "", 100, now)
	if err != nil {
		return worker + " 超过 2 分钟没有心跳"
	}
	var last time.Time
	for _, item := range series {
		if labelValue(item.LabelsJSON, "worker") != worker {
			continue
		}
		if latest, err := b.Metrics.Latest(ctx, item.SeriesID); err == nil && latest != nil && latest.Value > 0 {
			last = time.Unix(int64(latest.Value), 0)
		}
	}
	if last.IsZero() {
		return worker + " 超过 2 分钟没有心跳"
	}
	return fmt.Sprintf("%s 最后心跳于 %s（北京时间），已 %d 分钟没有心跳", worker, alerttext.Time(last), max(2, int(now.Sub(last).Minutes())))
}

func labelValue(labelsJSON, key string) string {
	var labels map[string]string
	if json.Unmarshal([]byte(labelsJSON), &labels) != nil {
		return ""
	}
	return labels[key]
}
