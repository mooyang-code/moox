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
	// AttentionInspectionBlocked 是交易账户的密钥检测发现阻断项（提现权限、疑似统一账户旧密钥等）。
	AttentionInspectionBlocked = "inspection_blocked"
	// AttentionInspectionFailing 是密钥复查连续多次没有检测成功。
	AttentionInspectionFailing = "inspection_failing"
	// AttentionTransferShortfall 是普通账户合约钱包可划转的资金不够，现货买入被按比例缩小。
	AttentionTransferShortfall = "transfer_shortfall"
	// AttentionTransferFailing 是钱包划转、归集出了问题：连续失败、查询持续失败、未结划转迟迟没确认。
	AttentionTransferFailing = "transfer_failing"
	// AttentionMarginRatioLow 是实盘账户保证金率跌到阈值以下，风控已暂停组合并减仓，等用户确认安全后手动恢复。
	AttentionMarginRatioLow = "margin_ratio_low"
	// AttentionValuationDegraded 是受管资产取不到价（已下架、停止交易、交易所确认标的不存在或暂时取价失败），
	// 权益按最后已知价或 0 计，没有阻断采样。
	AttentionValuationDegraded = "valuation_degraded"
	// AttentionFlattenOrdersUnsettled 是清仓、减仓、下架清仓结束时有子单没收拾掉（收尾满 30 分钟仍解析不出结果、
	// 交易所确认标的不存在），动作照常结束，这些子单要人工到交易所核对。
	AttentionFlattenOrdersUnsettled = "flatten_orders_unsettled"
	// AttentionReleaseOrdersUnverified 是释放策略会话时凭证失效、交易账户已停用或异常、标的已不存在，目标单没能撤下或确认，
	// 只按本地记录释放，这些单要人工到交易所核对。
	AttentionReleaseOrdersUnverified = "release_orders_unverified"
	// AttentionReleaseOrdersStuck 是释放策略会话连续失败超过 10 分钟（例如交易账户启用但会话一直建不起来）：目标单没撤下，
	// 会话还没释放，策略仍在重试。
	AttentionReleaseOrdersStuck = "release_orders_stuck"
	// AttentionOrdersInstrumentGone 是账户同步发现订单的标的已下架、交易所查不到（币安 -1121、OKX 51001）：未结束的单
	// 已按过期结束并释放资金预留；订单（含已结束的单）的累计成交领先已入账成交明细的，缺口已按已入账值收掉（成交明细缺失）。
	// 这些单实际成交了多少要人工到交易所核对，持仓、余额以同步结果为准。
	AttentionOrdersInstrumentGone = "orders_instrument_gone"
	// AttentionClockSkew 是下单或查询遇到时间戳超窗（币安 -1021、OKX 50102）：本机时钟与交易所相差过大，请检查 NTP。
	AttentionClockSkew = "clock_skew"
	// AttentionIsolatedPosition 是交易所账户里有逐仓持仓：moox 只支持全仓单向，该持仓不入投影，所在组合已暂停（已暂停的不改原因）。
	AttentionIsolatedPosition = "isolated_position"
)

// attentionKinds 里每种情况都要上报，恢复后 Monitor 才能看到值回到 0。
var attentionKinds = []string{
	AttentionTargetRejected, AttentionAutoPaused, AttentionTargetBlocked,
	AttentionAccountNotReady, AttentionTargetExpired, AttentionTargetReversal,
	AttentionRateLimited, AttentionTurnoverHigh, AttentionUnknownResults, AttentionMarginRatioLow,
	AttentionInspectionBlocked, AttentionInspectionFailing, AttentionTransferShortfall, AttentionTransferFailing,
	AttentionValuationDegraded, AttentionFlattenOrdersUnsettled, AttentionReleaseOrdersUnverified, AttentionReleaseOrdersStuck,
	AttentionOrdersInstrumentGone, AttentionClockSkew, AttentionIsolatedPosition,
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

// Forget 删掉一条关注项：情况已经解除，不必等保留窗口过去。
func Forget(kind, detail string) {
	notesMu.Lock()
	defer notesMu.Unlock()
	delete(notes[kind], detail)
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
