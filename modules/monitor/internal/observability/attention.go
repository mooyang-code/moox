package observability

import (
	"context"
	"strings"
	"sync"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/report"
)

// AttentionCheckKind 是关注项业务检查的种类：检查 ID 形如 attention:<服务名>:<kind>，名称与级别由生产者在指标标签里给出。
const AttentionCheckKind = "attention"

// AttentionCheckID 返回某个服务某种关注项的检查 ID。
func AttentionCheckID(service, kind string) string {
	return AttentionCheckKind + ":" + service + ":" + kind
}

// ParseAttentionCheckID 从检查 ID 里取出服务名与种类；不是关注项检查时 ok 为 false。
func ParseAttentionCheckID(checkID string) (service, kind string, ok bool) {
	parts := strings.Split(checkID, ":")
	if len(parts) != 3 || parts[0] != AttentionCheckKind || parts[1] == "" || parts[2] == "" {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// attentionSeverities 记下每项关注项检查最近一次上报的级别（检查 ID → critical / warning），告警分级按它判断。
var attentionSeverities sync.Map

// AttentionSeverity 返回关注项检查最近一次上报的级别；还没读到过时返回空。
func AttentionSeverity(checkID string) string {
	if value, ok := attentionSeverities.Load(checkID); ok {
		return value.(string)
	}
	return ""
}

// buildAttentionStatuses 把各服务上报的 moox_attention_active 变成业务检查：按（服务名, kind）分组，组内取最近一次上报的那条。
// detail 标签带对象名和原因，内容变化后旧序列不再更新，所以只看没有过期的序列。
// 值为 1 时 critical 记 down，warning 记 degraded（需关注，告警级别 warning）；值为 0 是正常。
func (b Builder) buildAttentionStatuses(ctx context.Context) ([]BusinessStatus, error) {
	if b.Metrics == nil || b.Metrics.Catalog() == nil {
		return nil, nil
	}
	series, _, err := b.Metrics.Catalog().ListSeries(ctx, "", report.AttentionMetric, "", 0, 500)
	if err != nil {
		return nil, err
	}
	statuses := map[string]BusinessStatus{}
	for _, item := range series {
		kind := labelValue(item.LabelsJSON, "kind")
		if item.IsStale || item.ServiceName == "" || kind == "" {
			continue
		}
		latest, err := b.Metrics.Latest(ctx, item.SeriesID)
		if err != nil {
			return nil, err
		}
		if latest == nil {
			continue
		}
		checkID := AttentionCheckID(item.ServiceName, kind)
		name := labelValue(item.LabelsJSON, "name")
		if name == "" {
			name = kind
		}
		severity := labelValue(item.LabelsJSON, "severity")
		status := BusinessStatus{
			SpaceID: events.SystemSpaceID, Kind: AttentionCheckKind, Module: item.ServiceName, CheckID: checkID, Name: name,
			Status: "healthy", Reason: "正常", LastCheckedAt: latest.ObservedAt,
		}
		if latest.Value > 0 {
			status.Status, status.Reason = "down", labelValue(item.LabelsJSON, "detail")
			if severity == report.AttentionWarning {
				status.Status = "degraded"
			}
		}
		if current, ok := statuses[checkID]; !ok || status.LastCheckedAt.After(current.LastCheckedAt) {
			statuses[checkID] = status
			if severity == report.AttentionWarning {
				attentionSeverities.Store(checkID, report.AttentionWarning)
			} else {
				attentionSeverities.Store(checkID, report.AttentionCritical)
			}
		}
	}
	out := make([]BusinessStatus, 0, len(statuses))
	for _, status := range statuses {
		out = append(out, status)
	}
	return out, nil
}
