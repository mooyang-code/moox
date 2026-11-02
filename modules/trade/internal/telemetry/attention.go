package telemetry

import (
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// 需要人看的情况。Monitor 按 kind 生成检查，detail 是账户名和原因。
const (
	AttentionTargetRejected  = "target_rejected"
	AttentionAutoPaused      = "auto_paused"
	AttentionTargetBlocked   = "target_blocked"
	AttentionAccountNotReady = "account_not_ready"
	AttentionTargetExpired   = "target_expired"
)

// attentionKinds 里每种情况都要上报，恢复后 Monitor 才能看到值回到 0。
var attentionKinds = []string{
	AttentionTargetRejected, AttentionAutoPaused, AttentionTargetBlocked,
	AttentionAccountNotReady, AttentionTargetExpired,
}

// maxAttentionDetail 是标签的字节上限：Monitor 超过 512 字节会拒收整份上报。
const maxAttentionDetail = 480

var urlPattern = regexp.MustCompile(`"?https?://[^\s"]+"?:?`)

// StableReason 去掉错误里的地址和时间戳：错误原文可能带签名参数，每次都不同，会让指标序列不断增长。
func StableReason(reason string) string {
	reason = urlPattern.ReplaceAllString(reason, "")
	if runes := []rune(reason); len(runes) > 60 {
		reason = string(runes[:60])
	}
	return reason
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

// 目标被 Trade 拒绝时不进目标表，这里记下来，在保留窗口内持续上报。
var (
	rejectionsMu sync.Mutex
	rejections   = map[string]time.Time{}
)

// NoteTargetRejection 记录一次被拒绝的目标。
func NoteTargetRejection(logicalAccountID, code string, at time.Time) {
	rejectionsMu.Lock()
	defer rejectionsMu.Unlock()
	rejections[logicalAccountID+"："+code] = at
}

// RecentTargetRejections 返回窗口内的拒绝记录，并清掉已过窗口的。
func RecentTargetRejections(now time.Time, window time.Duration) []string {
	rejectionsMu.Lock()
	defer rejectionsMu.Unlock()
	details := make([]string, 0, len(rejections))
	for detail, at := range rejections {
		if now.Sub(at) > window {
			delete(rejections, detail)
			continue
		}
		details = append(details, detail)
	}
	return details
}
