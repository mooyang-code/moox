package observability

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/report"
)

// AttentionCheckKind 是关注项业务检查的种类：检查 ID 形如 attention:<服务名>:<kind>，名称与级别由生产者在指标标签里给出。
const AttentionCheckKind = "attention"

// attentionSeriesLimit 是一轮读取的关注项序列上限：每个服务每种一条（detail 变化时旧序列过期），超过时本轮报错。
const attentionSeriesLimit = 2000

// attentionFreshExtra 是关注项序列比服务判定多放的过期时间（一轮）：序列按样本时刻、服务按接收时刻记最后上报，
// 生产者刚停报时序列可能比“组件停止上报”早一轮过期；多放一轮，停报时先由组件检查失败把关注项冻结住，不推假恢复。
const attentionFreshExtra = 30 * time.Second

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

// buildAttentionStatuses 把各服务上报的 moox_attention_active 变成业务检查：只取未过期的序列，按（服务名, kind）分组，
// 组内取最近一次上报的那条。detail 标签带对象名和原因，内容变化后旧序列不再更新、随后过期。
// 值为 1 时失败（warning 级别显示“需关注”，告警级别 warning）；值为 0 是正常。组内没有未过期序列的不产出，
// 由调用方按“该服务是否停止上报”决定冻结还是删除。
func (b Builder) buildAttentionStatuses(ctx context.Context, now time.Time) ([]BusinessStatus, error) {
	if b.Metrics == nil || b.Metrics.Catalog() == nil {
		return nil, nil
	}
	series, err := b.Metrics.Catalog().ListFreshSeriesByMetricAt(ctx, report.AttentionMetric, now, attentionFreshExtra, attentionSeriesLimit)
	if err != nil {
		return nil, err
	}
	statuses := map[string]BusinessStatus{}
	for _, item := range series {
		kind := labelValue(item.LabelsJSON, "kind")
		if item.ServiceName == "" || kind == "" {
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
		severity := report.AttentionCritical
		if labelValue(item.LabelsJSON, "severity") == report.AttentionWarning {
			severity = report.AttentionWarning
		}
		status := BusinessStatus{
			SpaceID: events.SystemSpaceID, Kind: AttentionCheckKind, Module: item.ServiceName, CheckID: checkID, Name: name,
			Severity: severity, Status: "healthy", Reason: "正常", LastCheckedAt: item.LastSeenAt,
		}
		if latest.Value > 0 {
			status.Status, status.Reason = "down", labelValue(item.LabelsJSON, "detail")
			if severity == report.AttentionWarning {
				status.Status = "degraded"
			}
		}
		if current, ok := statuses[checkID]; !ok || status.LastCheckedAt.After(current.LastCheckedAt) {
			statuses[checkID] = status
		}
	}
	out := make([]BusinessStatus, 0, len(statuses))
	for _, status := range statuses {
		out = append(out, status)
	}
	return out, nil
}

// labelValue 取序列标签里的一项；标签不是合法 JSON 时返回空。
func labelValue(labelsJSON, key string) string {
	var labels map[string]string
	if json.Unmarshal([]byte(labelsJSON), &labels) != nil {
		return ""
	}
	return labels[key]
}
