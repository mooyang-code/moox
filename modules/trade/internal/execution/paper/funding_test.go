package paper

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

type fundingFacts struct {
	FactStore
	latest   int64
	activity store.PaperSwapActivity
	since    []int64
	settled  [][]exchange.FundingRate
}

func (f *fundingFacts) LatestPaperFundingSettlement(context.Context, string, string) (int64, error) {
	return f.latest, nil
}

func (f *fundingFacts) PaperSwapActivity(_ context.Context, _, _ string, since int64) (store.PaperSwapActivity, error) {
	f.since = append(f.since, since)
	activity := f.activity
	activity.Since = since
	var fills []store.PaperSwapFill
	for _, fill := range f.activity.Fills {
		if fill.TradedAt >= since {
			fills = append(fills, fill)
		}
	}
	activity.Fills = fills
	return activity, nil
}

func (f *fundingFacts) SettlePaperFunding(_ context.Context, _, _ string, rates []exchange.FundingRate) ([]store.PaperFundingFeeRecord, error) {
	f.settled = append(f.settled, rates)
	out := make([]store.PaperFundingFeeRecord, len(rates))
	return out, nil
}

type fundingWindowCall struct{ start, end time.Time }

// fundingMarket 是能查资金费率的公共行情替身：按查询窗口返回 rates 里落在窗口内的记录。
type fundingMarket struct {
	PublicMarketData
	rates []exchange.FundingRate
	calls []fundingWindowCall
	err   error
}

func (m *fundingMarket) ListFundingRates(_ context.Context, start, end time.Time) ([]exchange.FundingRate, error) {
	m.calls = append(m.calls, fundingWindowCall{start, end})
	if m.err != nil {
		return nil, m.err
	}
	var out []exchange.FundingRate
	for _, rate := range m.rates {
		if !rate.FundingTime.Before(start) && !rate.FundingTime.After(end) {
			out = append(out, rate)
		}
	}
	return out, nil
}

func fundingRate(symbol string, at time.Time, mark string) exchange.FundingRate {
	return exchange.FundingRate{ExchangeSymbol: symbol, FundingTime: at, Rate: shared.MustDecimal("0.0001"), MarkPrice: shared.MustDecimal(mark)}
}

func swapPaperAdapter(facts *fundingFacts, market *fundingMarket, now *time.Time) *Adapter {
	return &Adapter{
		Account: store.TradingAccountRecord{SpaceID: "s", TradingAccountID: "paper-1", Markets: []string{"SWAP"}, SettlementAsset: "USDT"},
		Store:   facts, MarketData: market, Now: func() time.Time { return *now },
	}
}

func TestSettleFundingBackfillsAtMostSevenDaysAndFiltersRates(t *testing.T) {
	now := time.Date(2026, 11, 7, 10, 0, 30, 0, time.UTC)
	hour := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	facts := &fundingFacts{activity: store.PaperSwapActivity{Positions: map[string]shared.Decimal{"BTCUSDT": shared.MustDecimal("1")}}}
	market := &fundingMarket{rates: []exchange.FundingRate{
		fundingRate("BTCUSDT", now.Add(-8*24*time.Hour), "1"), // 超过 7 天，不在查询窗口里
		fundingRate("BTCUSDT", now.Add(-6*24*time.Hour), "1"),
		fundingRate("BTCUSDT", hour, "1"),
		fundingRate("ETHUSDT", hour, "1"),                       // 没有持有过
		fundingRate("BTCUSDT", hour.Add(time.Millisecond), "0"), // 没有标记价格
	}}
	adapter := swapPaperAdapter(facts, market, &now)
	if _, err := adapter.SettleFunding(context.Background()); err != nil {
		t.Fatal(err)
	}
	floor := now.Add(-FundingBackfillLimit)
	if len(market.calls) != 1 || !market.calls[0].start.Equal(floor) || !market.calls[0].end.Equal(now.Add(-fundingSettleDelay)) {
		t.Fatalf("calls = %+v", market.calls)
	}
	if len(facts.since) != 1 || facts.since[0] != floor.UnixMilli() {
		t.Fatalf("since = %+v", facts.since)
	}
	if len(facts.settled) != 1 || len(facts.settled[0]) != 2 ||
		!facts.settled[0][0].FundingTime.Equal(now.Add(-6*24*time.Hour)) || !facts.settled[0][1].FundingTime.Equal(hour) {
		t.Fatalf("settled = %+v", facts.settled)
	}

	// 同一个整点不再查交易所（10:00:50 往前 1 分钟仍在 9 点这一小时）。
	now = now.Add(20 * time.Second)
	if _, err := adapter.SettleFunding(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(market.calls) != 1 {
		t.Fatalf("同一整点又查了一次：%+v", market.calls)
	}
	// 过了下一个整点：从上一个整点起回看，晚公布的记录也能补上。
	now = time.Date(2026, 11, 7, 11, 1, 0, 0, time.UTC)
	if _, err := adapter.SettleFunding(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(market.calls) != 2 || !market.calls[1].start.Equal(hour) || !market.calls[1].end.Equal(now.Add(-fundingSettleDelay)) {
		t.Fatalf("calls = %+v", market.calls)
	}
}

func TestSettleFundingResumesFromLatestSettlement(t *testing.T) {
	now := time.Date(2026, 11, 7, 10, 5, 0, 0, time.UTC)
	latest := time.Date(2026, 11, 6, 16, 0, 0, 0, time.UTC)
	facts := &fundingFacts{latest: latest.UnixMilli(), activity: store.PaperSwapActivity{Positions: map[string]shared.Decimal{"BTCUSDT": shared.MustDecimal("-2")}}}
	market := &fundingMarket{}
	if _, err := swapPaperAdapter(facts, market, &now).SettleFunding(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(market.calls) != 1 || !market.calls[0].start.Equal(latest) {
		t.Fatalf("calls = %+v", market.calls)
	}
}

func TestSettleFundingSkipsHoursWithoutPositions(t *testing.T) {
	now := time.Date(2026, 11, 7, 10, 5, 0, 0, time.UTC)
	opened := time.Date(2026, 11, 5, 3, 20, 0, 0, time.UTC)
	closed := time.Date(2026, 11, 5, 5, 10, 0, 0, time.UTC)
	facts := &fundingFacts{activity: store.PaperSwapActivity{
		Positions: map[string]shared.Decimal{},
		Fills: []store.PaperSwapFill{
			{ExchangeSymbol: "BTCUSDT", TradedAt: opened.UnixMilli(), Delta: shared.MustDecimal("1")},
			{ExchangeSymbol: "BTCUSDT", TradedAt: closed.UnixMilli(), Delta: shared.MustDecimal("-1")},
		},
	}}
	market := &fundingMarket{}
	if _, err := swapPaperAdapter(facts, market, &now).SettleFunding(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantStart := time.Date(2026, 11, 5, 3, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 11, 5, 6, 0, 0, 0, time.UTC).Add(-time.Millisecond)
	if len(market.calls) != 1 || !market.calls[0].start.Equal(wantStart) || !market.calls[0].end.Equal(wantEnd) {
		t.Fatalf("只查有持仓的那几个小时，calls = %+v", market.calls)
	}

	// 一直空仓：不查交易所。
	empty := &fundingFacts{activity: store.PaperSwapActivity{Positions: map[string]shared.Decimal{}}}
	idle := &fundingMarket{}
	if _, err := swapPaperAdapter(empty, idle, &now).SettleFunding(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(idle.calls) != 0 {
		t.Fatalf("calls = %+v", idle.calls)
	}
}

func TestSettleFundingRetriesAfterFailure(t *testing.T) {
	now := time.Date(2026, 11, 7, 10, 5, 0, 0, time.UTC)
	facts := &fundingFacts{activity: store.PaperSwapActivity{Positions: map[string]shared.Decimal{"BTCUSDT": shared.MustDecimal("1")}}}
	market := &fundingMarket{err: errors.New("限流")}
	adapter := swapPaperAdapter(facts, market, &now)
	if _, err := adapter.SettleFunding(context.Background()); err == nil {
		t.Fatal("取不到费率要报错")
	}
	market.err = nil
	if _, err := adapter.SettleFunding(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(market.calls) != 2 || len(facts.settled) != 1 {
		t.Fatalf("失败后同一整点要重试：calls=%d settled=%d", len(market.calls), len(facts.settled))
	}
}

func TestSettleFundingNeedsSwapAndFundingSource(t *testing.T) {
	now := time.Date(2026, 11, 7, 10, 5, 0, 0, time.UTC)
	facts := &fundingFacts{activity: store.PaperSwapActivity{Positions: map[string]shared.Decimal{"BTCUSDT": shared.MustDecimal("1")}}}
	market := &fundingMarket{}
	spot := swapPaperAdapter(facts, market, &now)
	spot.Account.Markets = []string{"SPOT"}
	if _, err := spot.SettleFunding(context.Background()); err != nil || len(market.calls) != 0 {
		t.Fatalf("现货账户不结算资金费：err=%v calls=%d", err, len(market.calls))
	}
	// 公共行情没有资金费率（例如 OKX）：不结算。
	var plain PublicMarketData = struct{ PublicMarketData }{}
	if execution.FundingRatesOf(plain) != nil {
		t.Fatal("替身不应提供资金费率")
	}
	noSource := swapPaperAdapter(facts, market, &now)
	noSource.MarketData = plain
	if _, err := noSource.SettleFunding(context.Background()); err != nil || len(facts.settled) != 0 {
		t.Fatalf("err=%v settled=%d", err, len(facts.settled))
	}
}
