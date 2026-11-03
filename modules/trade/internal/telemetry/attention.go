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
	// AttentionTargetReversal 是同一目标内同一账户同一标的出现反向子单：说明下多了。
	AttentionTargetReversal = "target_reversal"
	// AttentionRateLimited 是交易所接口限流或封禁暂停。
	AttentionRateLimited = "rate_limited"
	// AttentionTurnoverHigh 是组合账户 1 小时成交额异常偏高。
	AttentionTurnoverHigh = "turnover_high"
	// AttentionUnknownResults 是 1 分钟内下单结果未知偏多。
	AttentionUnknownResults = "unknown_results"
)

// attentionKinds 里每种情况都要上报，恢复后 Monitor 才能看到值回到 0。
var attentionKinds = []string{
	AttentionTargetRejected, AttentionAutoPaused, AttentionTargetBlocked,
	AttentionAccountNotReady, AttentionTargetExpired, AttentionTargetReversal,
	AttentionRateLimited, AttentionTurnoverHigh, AttentionUnknownResults,
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

// 没有落库状态的关注项（目标被拒绝、反向下单、接口限流）在这里记下来，在保留窗口内持续上报。
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

// NoteTargetRejection 记录一次被拒绝的目标。
func NoteTargetRejection(logicalAccountID, code string, at time.Time) {
	Note(AttentionTargetRejected, logicalAccountID+"："+code, at)
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

// 下单结果未知按账户记时间，扫描时数 1 分钟内的笔数。
var (
	unknownMu      sync.Mutex
	unknownResults = map[string][]time.Time{}
)

// NoteUnknownResult 记录一笔结果未知的下单。
func NoteUnknownResult(account string, at time.Time) {
	unknownMu.Lock()
	defer unknownMu.Unlock()
	unknownResults[account] = append(unknownResults[account], at)
}

// UnknownResultsSince 返回每个账户在 since 之后结果未知的笔数，并清掉更早的记录。
func UnknownResultsSince(since time.Time) map[string]int {
	unknownMu.Lock()
	defer unknownMu.Unlock()
	counts := make(map[string]int, len(unknownResults))
	for account, times := range unknownResults {
		kept := times[:0]
		for _, at := range times {
			if at.After(since) {
				kept = append(kept, at)
			}
		}
		if len(kept) == 0 {
			delete(unknownResults, account)
			continue
		}
		unknownResults[account] = kept
		counts[account] = len(kept)
	}
	return counts
}
