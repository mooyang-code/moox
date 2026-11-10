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

type fundingWindowCall struct {
	start, end time.Time
	symbols    []string
}

// fundingMarket 是能查资金费率的公共行情替身：按查询窗口返回 rates 里落在窗口内的记录。
type fundingMarket struct {
	PublicMarketData
	rates []exchange.FundingRate
	calls []fundingWindowCall
	// referencePrice 是 GetReferencePrice 返回的现价，为空时返回错误。
	referencePrice string
	// failAt 是第几次调用（从 1 起）返回错误；为 0 不出错。
	failAt int
}

func (m *fundingMarket) ListFundingRates(_ context.Context, start, end time.Time, symbols []string) ([]exchange.FundingRate, error) {
	m.calls = append(m.calls, fundingWindowCall{start, end, symbols})
	if m.failAt == len(m.calls) {
		return nil, errors.New("限流")
	}
	var out []exchange.FundingRate
	for _, rate := range m.rates {
		if !rate.FundingTime.Before(start) && !rate.FundingTime.After(end) {
			out = append(out, rate)
		}
	}
	return out, nil
}

func (m *fundingMarket) GetReferencePrice(_ context.Context, key exchange.SymbolKey) (exchange.ReferencePrice, error) {
	if m.referencePrice == "" {
		return exchange.ReferencePrice{}, errors.New("没有参考价")
	}
	return exchange.ReferencePrice{Price: shared.MustDecimal(m.referencePrice)}, nil
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

// requireContiguousSegments 断言查询按不超过 4 小时的段首尾相接，从 start 到 end。
func requireContiguousSegments(t *testing.T, calls []fundingWindowCall, start, end time.Time) {
	t.Helper()
	if len(calls) == 0 || !calls[0].start.Equal(start) || !calls[len(calls)-1].end.Equal(end) {
		t.Fatalf("calls = %+v", calls)
	}
	for i, call := range calls {
		if call.end.Sub(call.start) >= fundingSegment {
			t.Fatalf("第 %d 段超过 4 小时：%+v", i, call)
		}
		if i > 0 && !calls[i-1].end.Add(time.Millisecond).Equal(call.start) {
			t.Fatalf("第 %d 段和上一段没有首尾相接：%+v", i, calls[i-1:i+1])
		}
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
		fundingRate("ETHUSDT", hour, "1"), // 没有持有过
	}}
	adapter := swapPaperAdapter(facts, market, &now)
	if _, err := adapter.SettleFunding(context.Background()); err != nil {
		t.Fatal(err)
	}
	floor := now.Add(-FundingBackfillLimit)
	requireContiguousSegments(t, market.calls, floor, now.Add(-fundingSettleDelay))
	if got := market.calls[0].symbols; len(got) != 1 || got[0] != "BTCUSDT" {
		t.Fatalf("只查持有过的合约，symbols = %v", got)
	}
	if len(facts.since) != 1 || facts.since[0] != floor.UnixMilli() {
		t.Fatalf("since = %+v", facts.since)
	}
	var settled []exchange.FundingRate
	for _, batch := range facts.settled {
		settled = append(settled, batch...)
	}
	if len(settled) != 2 || !settled[0].FundingTime.Equal(now.Add(-6*24*time.Hour)) || !settled[1].FundingTime.Equal(hour) {
		t.Fatalf("settled = %+v", settled)
	}
	if len(facts.settled) != len(market.calls) {
		t.Fatalf("每段查完就入账：settled=%d calls=%d", len(facts.settled), len(market.calls))
	}

	// 同一个整点不再查交易所（10:00:50 往前 1 分钟仍在 9 点这一小时）。
	calls := len(market.calls)
	now = now.Add(20 * time.Second)
	if _, err := adapter.SettleFunding(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(market.calls) != calls {
		t.Fatalf("同一整点又查了一次：%+v", market.calls[calls:])
	}
	// 过了下一个整点：从上一个整点起回看，晚公布的记录也能补上。
	now = time.Date(2026, 11, 7, 11, 1, 0, 0, time.UTC)
	if _, err := adapter.SettleFunding(context.Background()); err != nil {
		t.Fatal(err)
	}
	requireContiguousSegments(t, market.calls[calls:], hour, now.Add(-fundingSettleDelay))
}

// 重启后从表里最大结算时刻所在整点的前一个小时开始：同一整点里还没结算的合约也能补上。
func TestSettleFundingResumesBeforeTheLatestSettlementHour(t *testing.T) {
	now := time.Date(2026, 11, 7, 10, 5, 0, 0, time.UTC)
	latest := time.Date(2026, 11, 6, 16, 0, 0, 1_000_000, time.UTC)
	facts := &fundingFacts{latest: latest.UnixMilli(), activity: store.PaperSwapActivity{Positions: map[string]shared.Decimal{"BTCUSDT": shared.MustDecimal("-2")}}}
	market := &fundingMarket{}
	if _, err := swapPaperAdapter(facts, market, &now).SettleFunding(context.Background()); err != nil {
		t.Fatal(err)
	}
	requireContiguousSegments(t, market.calls, time.Date(2026, 11, 6, 15, 0, 0, 0, time.UTC), now.Add(-fundingSettleDelay))
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

// 中途失败：已入账的段不重做；退避期内不再请求，退避时间逐次翻倍；退避过后从失败那一段接着查。
func TestSettleFundingResumesFailedSegmentWithBackoff(t *testing.T) {
	now := time.Date(2026, 11, 7, 10, 5, 0, 0, time.UTC)
	facts := &fundingFacts{activity: store.PaperSwapActivity{Positions: map[string]shared.Decimal{"BTCUSDT": shared.MustDecimal("1")}}}
	market := &fundingMarket{failAt: 3}
	adapter := swapPaperAdapter(facts, market, &now)
	if _, err := adapter.SettleFunding(context.Background()); err == nil {
		t.Fatal("取不到费率要报错")
	}
	if len(market.calls) != 3 || len(facts.settled) != 2 {
		t.Fatalf("前两段已入账：calls=%d settled=%d", len(market.calls), len(facts.settled))
	}
	failed := market.calls[2]
	// 退避 1 分钟内不请求。
	now = now.Add(30 * time.Second)
	if _, err := adapter.SettleFunding(context.Background()); err != nil || len(market.calls) != 3 {
		t.Fatalf("退避期内又请求了：err=%v calls=%d", err, len(market.calls))
	}
	// 退避过后再失败一次：从失败那一段接着查，下一次退避 2 分钟。
	now = now.Add(31 * time.Second)
	market.failAt = 4
	if _, err := adapter.SettleFunding(context.Background()); err == nil {
		t.Fatal("第二次也失败")
	}
	if !market.calls[3].start.Equal(failed.start) {
		t.Fatalf("没有从失败那一段接着查：%+v", market.calls[3])
	}
	now = now.Add(time.Minute + 30*time.Second)
	if _, err := adapter.SettleFunding(context.Background()); err != nil || len(market.calls) != 4 {
		t.Fatalf("第二次退避是 2 分钟：err=%v calls=%d", err, len(market.calls))
	}
	now = now.Add(time.Minute)
	market.failAt = 0
	if _, err := adapter.SettleFunding(context.Background()); err != nil {
		t.Fatal(err)
	}
	requireContiguousSegments(t, market.calls[4:], failed.start, now.Add(-fundingSettleDelay))
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

// settledFundingRates 把各次入账的记录拍平。
func settledFundingRates(facts *fundingFacts) []exchange.FundingRate {
	var settled []exchange.FundingRate
	for _, batch := range facts.settled {
		settled = append(settled, batch...)
	}
	return settled
}

// 缺标记价格、结算时刻在 6 小时以内：进度停在这一期，下一轮从这一期重查；补上价格后这一期结算一次。
func TestSettleFundingWaitsForMissingMarkPrice(t *testing.T) {
	now := time.Date(2026, 11, 7, 10, 5, 0, 0, time.UTC)
	at := time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)
	facts := &fundingFacts{activity: store.PaperSwapActivity{Positions: map[string]shared.Decimal{"BTCUSDT": shared.MustDecimal("1")}}}
	market := &fundingMarket{rates: []exchange.FundingRate{fundingRate("BTCUSDT", at, "0")}}
	adapter := swapPaperAdapter(facts, market, &now)
	if _, err := adapter.SettleFunding(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := settledFundingRates(facts); len(got) != 0 {
		t.Fatalf("缺价的这一期不能结算：%+v", got)
	}
	calls := len(market.calls)

	// 交易所补上标记价格：同一个整点里也重查，从缺价的那一期起。
	market.rates = []exchange.FundingRate{fundingRate("BTCUSDT", at, "50000")}
	now = now.Add(time.Minute)
	if _, err := adapter.SettleFunding(context.Background()); err != nil {
		t.Fatal(err)
	}
	requireContiguousSegments(t, market.calls[calls:], at, now.Add(-fundingSettleDelay))
	got := settledFundingRates(facts)
	if len(got) != 1 || !got[0].FundingTime.Equal(at) || got[0].MarkPrice.String() != "50000" {
		t.Fatalf("补上价格后应结算一次：%+v", got)
	}

	// 结算完成后进度推到整点，同一整点不再查。
	calls = len(market.calls)
	now = now.Add(time.Minute)
	if _, err := adapter.SettleFunding(context.Background()); err != nil || len(market.calls) != calls {
		t.Fatalf("结算完成后又查了：err=%v calls=%d", err, len(market.calls)-calls)
	}
}

// 缺标记价格超过 6 小时：先取同一合约时间最近的有价记录，本段里没有就取交易所现在的参考价。
func TestSettleFundingUsesReferencePriceAfterSixHours(t *testing.T) {
	now := time.Date(2026, 11, 7, 10, 5, 0, 0, time.UTC)
	missing := time.Date(2026, 11, 7, 2, 0, 0, 0, time.UTC)
	positions := map[string]shared.Decimal{"BTCUSDT": shared.MustDecimal("1")}

	facts := &fundingFacts{activity: store.PaperSwapActivity{Positions: positions}}
	market := &fundingMarket{referencePrice: "777", rates: []exchange.FundingRate{
		fundingRate("BTCUSDT", missing.Add(-time.Hour), "100"), // 同一段里最近的有价记录
		fundingRate("BTCUSDT", missing, "0"),
	}}
	if _, err := swapPaperAdapter(facts, market, &now).SettleFunding(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := settledFundingRates(facts)
	if len(got) != 2 || !got[1].FundingTime.Equal(missing) || got[1].MarkPrice.String() != "100" {
		t.Fatalf("应取最近记录的价格：%+v", got)
	}

	alone := &fundingFacts{activity: store.PaperSwapActivity{Positions: positions}}
	market = &fundingMarket{referencePrice: "777", rates: []exchange.FundingRate{fundingRate("BTCUSDT", missing, "0")}}
	if _, err := swapPaperAdapter(alone, market, &now).SettleFunding(context.Background()); err != nil {
		t.Fatal(err)
	}
	got = settledFundingRates(alone)
	if len(got) != 1 || got[0].MarkPrice.String() != "777" {
		t.Fatalf("本段没有有价记录应取现在的参考价：%+v", got)
	}

	// 参考价也取不到：报错重试，不跳过。
	failing := &fundingFacts{activity: store.PaperSwapActivity{Positions: positions}}
	market = &fundingMarket{rates: []exchange.FundingRate{fundingRate("BTCUSDT", missing, "0")}}
	if _, err := swapPaperAdapter(failing, market, &now).SettleFunding(context.Background()); err == nil {
		t.Fatal("取不到参考价要报错")
	}
	if got := settledFundingRates(failing); len(got) != 0 {
		t.Fatalf("取不到参考价不能结算：%+v", got)
	}
}
