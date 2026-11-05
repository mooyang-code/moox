// Package telemetry 是事件中心的指标：需要人看的情况（关注项）和事件处理的错误计数。
package telemetry

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// 需要人看的情况。Monitor 按 kind 生成检查，detail 是规则名和原因。
const (
	// AttentionExecutionFailed 是通知类的执行记录用完重试置为失败，直到用户重新执行或确认。
	AttentionExecutionFailed = "execution_failed"
	// AttentionDelistDetected 是收到新的“标的即将下架”并且当前有策略持有，约 3 分钟：触发一次、恢复一次，相当于一条通知。
	AttentionDelistDetected = "delist_detected"
	// AttentionActionFailed 是系统动作某一步用完重试、核对失败或有零头，直到用户处理。
	AttentionActionFailed = "action_failed"
)

// DelistDetectedWindow 是 delist_detected 持续的时间。
const DelistDetectedWindow = 3 * time.Minute

// attentionKinds 里每种情况都要上报，恢复后 Monitor 才能看到值回到 0。
var attentionKinds = []string{AttentionExecutionFailed, AttentionDelistDetected, AttentionActionFailed}

// maxAttentionDetail 是标签的字节上限：Monitor 超过 512 字节会拒收整份上报。
const maxAttentionDetail = 480

var (
	// Attention 是关注项：值为 1 表示该种类当前有需要处理的情况，detail 里列出它们。
	Attention = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_callbackdispatcher_attention_active", Help: "Event center conditions that need a person, with rule and reason in the detail label."}, []string{"kind", "detail"})
	// EventErrors 统计收到后无法处理的事件（解码失败等），这类消息会被终止，不再重投。
	EventErrors = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_callbackdispatcher_event_errors_total", Help: "Events that could not be decoded or handled and were terminated."}, []string{"reason"})
)

func init() {
	prometheus.MustRegister(Attention, EventErrors)
}

// PublishAttention 用本轮结果替换上报值，items 按 kind 列出每条详情。
func PublishAttention(items map[string][]string) {
	Attention.Reset()
	for _, kind := range attentionKinds {
		details := append([]string(nil), items[kind]...)
		if len(details) == 0 {
			Attention.WithLabelValues(kind, "").Set(0)
			continue
		}
		sort.Strings(details)
		detail := strings.Join(details, "；")
		if len(detail) > maxAttentionDetail {
			detail = strings.ToValidUTF8(detail[:maxAttentionDetail], "") + "…"
		}
		Attention.WithLabelValues(kind, detail).Set(1)
	}
}

// 没有落库状态的关注项（delist_detected）记在这里，在窗口内持续上报。
var (
	notesMu sync.Mutex
	notes   = map[string]map[string]time.Time{}
)

// Note 记录一条关注项，detail 相同的只保留最近一次时间。
func Note(kind, detail string, at time.Time) {
	notesMu.Lock()
	defer notesMu.Unlock()
	if notes[kind] == nil {
		notes[kind] = map[string]time.Time{}
	}
	notes[kind][detail] = at
}

// NoteDelistDetected 记录一次“收到下架并且有策略持有”。系统动作处理器发现有持有者时调用。
func NoteDelistDetected(detail string, at time.Time) {
	Note(AttentionDelistDetected, detail, at)
}

// Recent 返回窗口内的关注项，并清掉已过窗口的。
func Recent(kind string, now time.Time, window time.Duration) []string {
	notesMu.Lock()
	defer notesMu.Unlock()
	details := make([]string, 0, len(notes[kind]))
	for detail, at := range notes[kind] {
		if now.Sub(at) > window {
			delete(notes[kind], detail)
			continue
		}
		details = append(details, detail)
	}
	return details
}
