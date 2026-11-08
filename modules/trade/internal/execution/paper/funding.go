package paper

import (
	"context"
	"time"

	"trpc.group/trpc-go/trpc-go/log"

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

// SettleFunding 结算到期的永续资金费，返回本次新记入的条数。模拟盘会话每分钟调用一次，同一个整点只向交易所查一次。
//
// 结算时刻的持仓按成交推：模拟盘持仓只由本地成交投影，当前持仓减去结算时刻及之后的成交就是那一刻的持仓，
// 停机期间的结算点也能补算，不需要整点取持仓快照。查询起点是表里最大的结算时刻（含，唯一键去重），最早不超过 7 天前；
// 之后每过一个整点回看上一个整点起的这段，交易所晚公布的记录也能补上。没有持仓的时段不查。
func (a *Adapter) SettleFunding(ctx context.Context) (int, error) {
	if a == nil || !a.Account.SupportsMarket(string(exchange.MarketTypeSwap)) {
		return 0, nil
	}
	source := execution.FundingRatesOf(a.MarketData)
	facts, ok := a.Store.(fundingStore)
	if source == nil || !ok {
		return 0, nil
	}
	a.fundingMu.Lock()
	defer a.fundingMu.Unlock()
	now := a.now()
	until := now.Add(-fundingSettleDelay)
	hour := until.Truncate(time.Hour)
	if !a.fundingHour.IsZero() && !hour.After(a.fundingHour) {
		return 0, nil
	}
	from := a.fundingHour
	if from.IsZero() {
		latest, err := facts.LatestPaperFundingSettlement(ctx, a.Account.SpaceID, a.Account.TradingAccountID)
		if err != nil {
			return 0, storageError(err)
		}
		from = time.UnixMilli(latest).UTC()
	}
	if floor := now.Add(-FundingBackfillLimit); from.Before(floor) {
		from = floor
	}
	activity, err := facts.PaperSwapActivity(ctx, a.Account.SpaceID, a.Account.TradingAccountID, from.UnixMilli())
	if err != nil {
		return 0, storageError(err)
	}
	held := activity.Symbols()
	var rates []exchange.FundingRate
	for _, window := range exposedWindows(activity, from, until) {
		fetched, err := source.ListFundingRates(ctx, window.start, window.end)
		if err != nil {
			return 0, err
		}
		for _, rate := range fetched {
			if _, found := held[rate.ExchangeSymbol]; !found || rate.FundingTime.After(until) {
				continue
			}
			if rate.MarkPrice.IsZero() || rate.MarkPrice.IsNegative() {
				log.WarnContextf(ctx, "模拟盘资金费：%s 在 %s 的结算记录没有标记价格，跳过：account=%s",
					rate.ExchangeSymbol, rate.FundingTime.Format(time.RFC3339), a.Account.TradingAccountID)
				continue
			}
			rates = append(rates, rate)
		}
	}
	inserted, err := facts.SettlePaperFunding(ctx, a.Account.SpaceID, a.Account.TradingAccountID, rates)
	if err != nil {
		return 0, storageError(err)
	}
	a.fundingHour = hour
	return len(inserted), nil
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
