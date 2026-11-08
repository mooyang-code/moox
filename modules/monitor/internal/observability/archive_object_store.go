package observability

import (
	"context"
	"time"

	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/packages/report"
)

// ArchiveObjectStoreCheckName 是归档对象存储可用性检查的名称。
const ArchiveObjectStoreCheckName = "归档对象存储"

// buildArchiveObjectStore 把 archive-mgr 上报的对象存储可用状态转成检查：不可用超过 5 分钟时失败，原因带探测错误；
// 不可用未满 5 分钟只写在原因里。没有新鲜序列（未配置对象存储、worker 心跳不新鲜或 archive-mgr 停报）时不生成，
// 已有的检查随之按“不再需要”恢复；worker 与 archive-mgr 自身的故障由在线状态与服务检查告警。
func (b Builder) buildArchiveObjectStore(ctx context.Context, spaceID string, now time.Time) ([]BusinessStatus, error) {
	if spaceID != "" && spaceID != monmetrics.InternalMetricSpaceID {
		return nil, nil
	}
	if b.Metrics == nil || b.Metrics.Catalog() == nil {
		return nil, nil
	}
	series, err := b.Metrics.Catalog().FindSeriesAt(ctx, "", "", report.ArchiveObjectStoreStateMetric, "", 1000, now) // probe_error 在标签里，一天内可能留下多条已不上报的序列
	if err != nil {
		return nil, err
	}
	// probe_error 变化会换一条序列：取最近一次上报的那条。
	var current *monmetrics.MetricLatest
	for _, item := range series {
		if item.IsStale {
			continue
		}
		latest, err := b.Metrics.Latest(ctx, item.SeriesID)
		if err != nil {
			return nil, err
		}
		if latest != nil && (current == nil || latest.ObservedAt.After(current.ObservedAt)) {
			current = latest
		}
	}
	if current == nil {
		return nil, nil
	}
	worker, probeError := labelValue(current.LabelsJSON, "worker"), labelValue(current.LabelsJSON, "probe_error")
	status, reason := "healthy", worker+" 的对象存储可用"
	switch int(current.Value) {
	case report.ObjectStoreDown:
		status, reason = "down", worker+" 的对象存储不可用已超过 5 分钟："+probeError
	case report.ObjectStoreUnavailable:
		reason = worker + " 的对象存储暂时不可用（未满 5 分钟）：" + probeError
	}
	return []BusinessStatus{{
		SpaceID: monmetrics.InternalMetricSpaceID, Kind: "archive_object_store", Module: "archive-worker",
		Status: status, Reason: reason, LastCheckedAt: now,
	}}, nil
}
