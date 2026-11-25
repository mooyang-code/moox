package relay

import (
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/packages/report"
)

// outboxBacklogGrace 是数据变更投递积压的告警线：有积压、且最早一条已等待超过 2 分钟。中继每 10 秒轮询一次，
// 正常情况下新写入的标记秒级就发布出去；超过 2 分钟说明 Primary 到 View 的投递中断，赶在数据集输出水位过期之前发现。
const outboxBacklogGrace = 2 * time.Minute

// OutboxAttention 返回采集时现算的关注项来源：投递积压超过 2 分钟时上报 outbox_backlog。
// detail 只写最早一条的时刻，不写积压条数（条数每轮都变，在本机指标 moox_storage_outbox_pending_entries 里看）。
func OutboxAttention(metrics *observability.ViewMetrics) func(now time.Time) map[string][]string {
	return func(now time.Time) map[string][]string {
		pending, oldest := metrics.OutboxOldest()
		if pending <= 0 || oldest.IsZero() || now.Sub(oldest) <= outboxBacklogGrace {
			return nil
		}
		return map[string][]string{observability.AttentionOutboxBacklog: {
			"最早一条自 " + report.AttentionTime(oldest) + " 起没有投递出去：Primary 到 View 的数据变更投递中断，看 storage-primary 日志里的发布失败原因",
		}}
	}
}
