package paper

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-go/log"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

const (
	// FundingCheckInterval 是模拟盘会话检查资金费的间隔。
	FundingCheckInterval = time.Minute
	// fundingSettleDelay：结算时刻过去这么久才结算。撮合的成交时间在落库前就定了，等它们落库，持仓才推得准；也等交易所公布记录。
	fundingSettleDelay = time.Minute
	// FundingBackfillLimit：Trade 停机后最多补算这么久。
	FundingBackfillLimit = 7 * 24 * time.Hour
	// fundingSegment 是每次向交易所查询、入账的最长时间段：一段入账后就推进进度，失败只重做失败的那一段。
	fundingSegment = 4 * time.Hour
	// fundingMarkWait：记录缺标记价格时，结算时刻距今不超过这么久就等下一轮重查（交易所可能还没补上）；超过仍缺就用参考价结算。
	fundingMarkWait = 6 * time.Hour
	// 失败后退避：从 1 分钟起每次翻倍，最长 30 分钟，避免每分钟原样重查整段、触发整个域名的限流。
	fundingRetryBase = time.Minute
	fundingRetryMax  = 30 * time.Minute
)

// fundingStore 是结算资金费要用的事实存储（*store.Store 实现）；测试替身没有实现时不结算。
type fundingStore interface {
	LatestPaperFundingSettlement(ctx context.Context, spaceID, tradingAccountID string) (int64, error)
	PaperSwapActivity(ctx context.Context, spaceID, tradingAccountID string, since int64) (store.PaperSwapActivity, error)
	SettlePaperFunding(ctx context.Context, spaceID, tradingAccountID string, rates []exchange.FundingRate) ([]store.PaperFundingFeeRecord, error)
}

// fundingWindow 是一段要向交易所查资金费率的时间（含两端，毫秒精度）。
type fundingWindow struct {
	start, end time.Time
}

// fundingProgress 是资金费结算在内存里的进度（不落库，重启后从表里最大的结算时刻重新推）。
type fundingProgress struct {
	mu sync.Mutex
	// checked 是已经查完的最近一个整点，同一个整点不再查交易所。
	checked time.Time
	// next 是下一次查询的起点：查完一轮后是这一轮的整点（下个整点从这里回看），中途失败时是失败那一段的起点。
	next     time.Time
	failures int
	retryAt  time.Time
}

// SettleFunding 结算到期的永续资金费，返回本次新记入的条数。模拟盘会话每分钟调用一次，同一个整点只查完一轮。
//
// 结算时刻的持仓按成交推：模拟盘持仓只由本地成交投影，当前持仓减去结算时刻及之后的成交就是那一刻的持仓，
// 停机期间的结算点也能补算，不需要整点取持仓快照。重启后的起点是表里最大结算时刻所在整点的前一个小时
// （同一整点里还没结算的合约也能补上，已结算的靠唯一键去重），最早不超过 7 天前；之后每过一个整点从上一个整点起回看，
// 交易所晚公布的记录也能补上。没有持仓的时段不查。按段查询、入账，失败后退避重试，只重做没入账的那几段。
func (a *Adapter) SettleFunding(ctx context.Context) (int, error) {
	if a == nil || !a.Account.SupportsMarket(string(exchange.MarketTypeSwap)) {
		return 0, nil
	}
	source := execution.FundingRatesOf(a.MarketData)
	facts, ok := a.Store.(fundingStore)
	if source == nil || !ok {
		return 0, nil
	}
	progress := &a.funding
	progress.mu.Lock()
	defer progress.mu.Unlock()
	now := a.now()
	if now.Before(progress.retryAt) {
		return 0, nil
	}
	until := now.Add(-fundingSettleDelay)
	hour := until.Truncate(time.Hour)
	if !progress.checked.IsZero() && !hour.After(progress.checked) {
		return 0, nil
	}
	from := progress.next
	if from.IsZero() {
		latest, err := facts.LatestPaperFundingSettlement(ctx, a.Account.SpaceID, a.Account.TradingAccountID)
		if err != nil {
			return 0, progress.fail(now, storageError(err))
		}
		if latest > 0 {
			from = time.UnixMilli(latest).UTC().Truncate(time.Hour).Add(-time.Hour)
		}
	}
	if floor := now.Add(-FundingBackfillLimit); from.Before(floor) {
		from = floor
	}
	count, waiting, err := a.settleFundingFrom(ctx, source, facts, progress, from, until)
	if err != nil {
		return count, progress.fail(now, err)
	}
	progress.failures, progress.retryAt = 0, time.Time{}
	if waiting {
		// 进度停在缺标记价格的那一期，不记这个整点已查完：下一轮从那一期重查。
		return count, nil
	}
	progress.checked, progress.next = hour, hour
	return count, nil
}

// fail 记一次失败，按失败次数退避。
func (p *fundingProgress) fail(now time.Time, err error) error {
	p.failures++
	wait := fundingRetryMax
	if p.failures <= 6 {
		wait = min(fundingRetryBase<<(p.failures-1), fundingRetryMax)
	}
	p.retryAt = now.Add(wait)
	return err
}

// settleFundingFrom 从 from 到 until 逐段查询、入账；每段入账后就把进度推到这一段之后。
// 某一段里有记录缺标记价格、且结算时刻距 until 不超过 fundingMarkWait 时，这一段其余记录照常入账，
// 进度停在缺价的那一期（waiting 为真），后面的段留到下一轮；缺价超过 fundingMarkWait 的用参考价结算。
func (a *Adapter) settleFundingFrom(ctx context.Context, source execution.FundingRateSource, facts fundingStore, progress *fundingProgress, from, until time.Time) (int, bool, error) {
	activity, err := facts.PaperSwapActivity(ctx, a.Account.SpaceID, a.Account.TradingAccountID, from.UnixMilli())
	if err != nil {
		return 0, false, storageError(err)
	}
	held := activity.Symbols()
	symbols := make([]string, 0, len(held))
	for symbol := range held {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)
	total := 0
	for _, segment := range splitWindows(exposedWindows(activity, from, until), fundingSegment) {
		fetched, err := source.ListFundingRates(ctx, segment.start, segment.end, symbols)
		if err != nil {
			return total, false, err
		}
		var rates []exchange.FundingRate
		var pending time.Time // 本段最早一条还在等标记价格的结算时刻
		for _, rate := range fetched {
			if _, found := held[rate.ExchangeSymbol]; !found || rate.FundingTime.After(until) {
				continue
			}
			if rate.MarkPrice.IsZero() || rate.MarkPrice.IsNegative() {
				if until.Sub(rate.FundingTime) <= fundingMarkWait {
					if pending.IsZero() || rate.FundingTime.Before(pending) {
						pending = rate.FundingTime
					}
					continue
				}
				price, err := a.fundingReferencePrice(ctx, fetched, rate)
				if err != nil {
					return total, false, err
				}
				log.WarnContextf(ctx, "模拟盘资金费：%s 在 %s 的结算记录一直没有标记价格，改用参考价 %s 结算：account=%s",
					rate.ExchangeSymbol, rate.FundingTime.Format(time.RFC3339), price.String(), a.Account.TradingAccountID)
				rate.MarkPrice = price
			}
			rates = append(rates, rate)
		}
		inserted, err := facts.SettlePaperFunding(ctx, a.Account.SpaceID, a.Account.TradingAccountID, rates)
		if err != nil {
			return total, false, storageError(err)
		}
		total += len(inserted)
		if !pending.IsZero() {
			progress.next = pending
			return total, true, nil
		}
		progress.next = segment.end.Add(time.Millisecond)
	}
	return total, false, nil
}

// fundingReferencePrice 是缺标记价格的记录超过 fundingMarkWait 仍补不上时用的参考价：
// 优先取同一合约在本段里时间最近的、有标记价格的记录（前后都算，一样近取较早的），没有就取交易所现在的参考价。
func (a *Adapter) fundingReferencePrice(ctx context.Context, fetched []exchange.FundingRate, missing exchange.FundingRate) (shared.Decimal, error) {
	var nearest *exchange.FundingRate
	var gap time.Duration
	for i := range fetched {
		candidate := &fetched[i]
		if candidate.ExchangeSymbol != missing.ExchangeSymbol || candidate.MarkPrice.IsZero() || candidate.MarkPrice.IsNegative() {
			continue
		}
		distance := candidate.FundingTime.Sub(missing.FundingTime).Abs()
		if nearest == nil || distance < gap || (distance == gap && candidate.FundingTime.Before(nearest.FundingTime)) {
			nearest, gap = candidate, distance
		}
	}
	if nearest != nil {
		return nearest.MarkPrice, nil
	}
	reference, err := a.MarketData.GetReferencePrice(ctx, exchange.NewSymbolKey(exchange.MarketTypeSwap, missing.ExchangeSymbol))
	if err != nil {
		return shared.Decimal{}, err
	}
	if reference.Price.IsZero() || reference.Price.IsNegative() {
		return shared.Decimal{}, errors.New("参考价无效：" + missing.ExchangeSymbol)
	}
	return reference.Price, nil
}

// splitWindows 把每段切成不超过 limit 的小段。
func splitWindows(windows []fundingWindow, limit time.Duration) []fundingWindow {
	var result []fundingWindow
	for _, window := range windows {
		for start := window.start; !start.After(window.end); {
			end := start.Add(limit - time.Millisecond)
			if end.After(window.end) {
				end = window.end
			}
			result = append(result, fundingWindow{start: start, end: end})
			start = end.Add(time.Millisecond)
		}
	}
	return result
}

// exposedWindows 把 [from, until] 按整点切段，只留下段内有永续持仓（段首持仓不为零，或段内有成交）的段，相邻的合并。
func exposedWindows(activity store.PaperSwapActivity, from, until time.Time) []fundingWindow {
	if until.Before(from) {
		return nil
	}
	positions := activity.Sweep()
	var windows []fundingWindow
	for start := from; !start.After(until); {
		end := start.Truncate(time.Hour).Add(time.Hour - time.Millisecond)
		if end.After(until) {
			end = until
		}
		positions.AdvanceTo(start.UnixMilli())
		if positions.ExposedUntil(end.UnixMilli()) {
			if last := len(windows) - 1; last >= 0 && windows[last].end.Add(time.Millisecond).Equal(start) {
				windows[last].end = end
			} else {
				windows = append(windows, fundingWindow{start: start, end: end})
			}
		}
		start = end.Add(time.Millisecond)
	}
	return windows
}
