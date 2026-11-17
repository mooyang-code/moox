package stockfeed

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/packages/cnstock"
	"github.com/stretchr/testify/require"
)

// cst 把北京时间写成 UTC 时刻。
func cst(value string) time.Time {
	parsed, err := time.Parse("2006-01-02 15:04:05", value)
	if err != nil {
		panic(err)
	}
	return parsed.Add(-8 * time.Hour).UTC()
}

func TestMarketQuoteFromSnapshot(t *testing.T) {
	now := cst("2026-11-07 10:00:00")
	calls := 0
	market := &Market{Label: "A 股", Now: func() time.Time { return now }, Quotes: func(_ context.Context, ids []string) (map[string]cnstock.Quote, error) {
		calls++
		result := map[string]cnstock.Quote{}
		for _, id := range ids {
			switch id {
			case "600519.XSHG":
				result[id] = cnstock.Quote{InstrumentID: id, Last: "1453.00", PrevClose: "1452.00", Bid: "1452.00", Ask: "1453.00",
					LimitUp: "1597.20", LimitDown: "1306.80", Time: cst("2026-11-06 16:14:58")}
			case "002129.XSHE":
				result[id] = cnstock.Quote{InstrumentID: id, Last: "9.38", PrevClose: "9.38", LimitUp: "10.32", LimitDown: "8.44",
					Time: cst("2026-11-06 16:14:54"), Halted: true}
			}
		}
		return result, nil
	}}
	keys := []exchange.SymbolKey{exchange.NewSymbolKey(exchange.MarketTypeStock, "600519.XSHG"), exchange.NewSymbolKey(exchange.MarketTypeStock, "002129.XSHE")}
	require.NoError(t, market.PrefetchQuotes(context.Background(), keys))
	quote, err := market.GetQuote(context.Background(), keys[0])
	require.NoError(t, err)
	require.Equal(t, 1, calls, "预取后 2 秒内命中缓存")
	require.Equal(t, "1597.2", quote.LimitUp.String())
	require.Equal(t, "1306.8", quote.LimitDown.String())
	require.False(t, quote.Halted)
	require.Equal(t, cst("2026-11-06 16:14:58"), quote.SourceTime)
	halted, err := market.GetQuote(context.Background(), keys[1])
	require.NoError(t, err)
	require.True(t, halted.Halted)
	require.True(t, halted.Bid.IsZero() && halted.Ask.IsZero())
	require.Equal(t, now, halted.SourceTime, "停牌报价不判过期：时间按取价时刻")
	require.Equal(t, shared.MustDecimal("9.38").String(), halted.Last.String())
	now = now.Add(3 * time.Second)
	_, err = market.GetReferencePrice(context.Background(), keys[0])
	require.NoError(t, err)
	require.Equal(t, 2, calls, "超过 2 秒重新取")
	_, err = market.GetQuote(context.Background(), exchange.NewSymbolKey(exchange.MarketTypeStock, "000001.XSHE"))
	require.Error(t, err)
}

// 腾讯故障：批量预取失败后 2 秒内单只取价直接返回同一个错误，不再逐只请求（每只都要等超时）；过了 2 秒再试。
func TestMarketQuoteUnavailableIsCachedBriefly(t *testing.T) {
	now := cst("2026-11-06 10:00:00")
	calls := 0
	market := &Market{Label: "A 股", Now: func() time.Time { return now }, Quotes: func(context.Context, []string) (map[string]cnstock.Quote, error) {
		calls++
		return nil, cnstock.ErrTimeout
	}}
	keys := []exchange.SymbolKey{exchange.NewSymbolKey(exchange.MarketTypeStock, "600519.XSHG"), exchange.NewSymbolKey(exchange.MarketTypeStock, "000001.XSHE")}
	require.ErrorIs(t, market.PrefetchQuotes(context.Background(), keys), cnstock.ErrTimeout)
	for _, key := range keys {
		_, err := market.GetQuote(context.Background(), key)
		require.ErrorIs(t, err, cnstock.ErrTimeout)
		require.ErrorContains(t, err, "A 股行情不可用")
	}
	require.Equal(t, 1, calls)
	now = now.Add(3 * time.Second)
	_, err := market.GetQuote(context.Background(), keys[0])
	require.Error(t, err)
	require.Equal(t, 2, calls)
}

func TestAdapterDoesNotPlaceOrders(t *testing.T) {
	adapter := NewAdapter(&Market{Exchange: exchange.ExchangeHKEX})
	_, err := adapter.PlaceOrder(context.Background(), exchange.OrderRequest{})
	require.True(t, errors.Is(err, ErrNoOrders))
	require.Equal(t, exchange.ExchangeHKEX, adapter.Exchange(), "适配器的场所取自共用行情")
}

func TestUntilNextRefresh(t *testing.T) {
	require.Equal(t, 30*time.Minute, untilNextRefresh(cst("2026-11-09 08:00:00")))
	require.Equal(t, 23*time.Hour+30*time.Minute, untilNextRefresh(cst("2026-11-09 09:00:00")))
}

// 腾讯超时要等几秒才返回：缓存时间按请求返回之后算，失败缓存和成功结果都不会写进去就过期。
func TestMarketCacheStampsAfterRequestReturns(t *testing.T) {
	now := cst("2026-11-06 10:00:00")
	calls := 0
	fail := true
	market := &Market{Label: "A 股", Now: func() time.Time { return now }, Quotes: func(_ context.Context, ids []string) (map[string]cnstock.Quote, error) {
		calls++
		now = now.Add(5 * time.Second) // 请求期间时钟前进超过 2 秒
		if fail {
			return nil, cnstock.ErrTimeout
		}
		return map[string]cnstock.Quote{ids[0]: {InstrumentID: ids[0], Last: "10", Time: now}}, nil
	}}
	key := exchange.NewSymbolKey(exchange.MarketTypeStock, "600519.XSHG")
	_, err := market.GetQuote(context.Background(), key)
	require.ErrorIs(t, err, cnstock.ErrTimeout)
	_, err = market.GetQuote(context.Background(), key)
	require.ErrorIs(t, err, cnstock.ErrTimeout)
	require.Equal(t, 1, calls, "失败缓存仍然生效")

	now = now.Add(3 * time.Second)
	fail = false
	_, err = market.GetQuote(context.Background(), key)
	require.NoError(t, err)
	_, err = market.GetQuote(context.Background(), key)
	require.NoError(t, err)
	require.Equal(t, 2, calls, "成功结果也不会写进去就过期")
}

// 格式错误不是网络故障：不记失败缓存，下次照常重试；缺了的那只按“取不到报价”处理。
func TestMarketProtocolErrorIsNotCached(t *testing.T) {
	calls := 0
	market := &Market{Label: "A 股", Quotes: func(context.Context, []string) (map[string]cnstock.Quote, error) {
		calls++
		return nil, cnstock.ErrProtocol
	}}
	key := exchange.NewSymbolKey(exchange.MarketTypeStock, "600519.XSHG")
	for range 2 {
		_, err := market.GetQuote(context.Background(), key)
		require.ErrorIs(t, err, cnstock.ErrProtocol)
	}
	require.Equal(t, 2, calls)

	missing := &Market{Label: "A 股", Quotes: func(context.Context, []string) (map[string]cnstock.Quote, error) {
		return map[string]cnstock.Quote{}, nil
	}}
	_, err := missing.GetQuote(context.Background(), key)
	require.ErrorContains(t, err, "没有标的")
}

// 只读缓存的上下文：不取新快照，只用已有的；没有或比 maxAge 旧时报“不在缓存里”。
func TestMarketQuoteCachedOnlyNeverFetches(t *testing.T) {
	now := cst("2026-11-07 10:00:00")
	calls := 0
	market := &Market{Label: "A 股", Now: func() time.Time { return now }, Quotes: func(_ context.Context, ids []string) (map[string]cnstock.Quote, error) {
		calls++
		return map[string]cnstock.Quote{ids[0]: {InstrumentID: ids[0], Last: "10.00", PrevClose: "10.00", Time: now}}, nil
	}}
	key := exchange.SymbolKey{Market: exchange.MarketTypeStock, Symbol: "600519.XSHG"}
	cached := exchange.WithCachedQuotes(context.Background(), time.Minute)
	if _, err := market.GetQuote(cached, key); !errors.Is(err, exchange.ErrQuoteNotCached) || calls != 0 {
		t.Fatalf("没有快照时应报不在缓存里且不发请求：err = %v, calls = %d", err, calls)
	}
	if _, err := market.GetQuote(context.Background(), key); err != nil || calls != 1 {
		t.Fatalf("普通取价：err = %v, calls = %d", err, calls)
	}
	// 过了 2 秒的复用时间，普通取价会重新取，只读缓存的照用旧快照。
	now = now.Add(30 * time.Second)
	if quote, err := market.GetQuote(cached, key); err != nil || quote.Last.String() != "10" || calls != 1 {
		t.Fatalf("应读到已有快照：quote = %+v, err = %v, calls = %d", quote, err, calls)
	}
	now = now.Add(time.Minute)
	if _, err := market.GetQuote(cached, key); !errors.Is(err, exchange.ErrQuoteNotCached) || calls != 1 {
		t.Fatalf("比 maxAge 旧的快照不用：err = %v, calls = %d", err, calls)
	}
}

// 港股开了 FillEmptyBook：快照里没有买一、卖一时用现价补上，不当作没有对手价；停牌照旧清空买一卖一、按昨收计。
// 没开的场所（A 股）没有卖一就是涨停，保持为空。
func TestMarketFillsEmptyBookWithLast(t *testing.T) {
	now := cst("2026-11-17 10:00:00")
	quotes := func(_ context.Context, ids []string) (map[string]cnstock.Quote, error) {
		result := map[string]cnstock.Quote{}
		for _, id := range ids {
			result[id] = cnstock.Quote{InstrumentID: id, Last: "376.000", PrevClose: "390.400", Time: now, Halted: id == "00204.XHKG", LotSize: 100}
		}
		return result, nil
	}
	key := exchange.NewSymbolKey(exchange.MarketTypeStock, "00700.XHKG")
	hk := &Market{Exchange: exchange.ExchangeHKEX, Label: "港股", FillEmptyBook: true, Now: func() time.Time { return now }, Quotes: quotes}
	quote, err := hk.GetQuote(context.Background(), key)
	require.NoError(t, err)
	require.Equal(t, "376", quote.Bid.String())
	require.Equal(t, "376", quote.Ask.String())
	require.True(t, quote.LimitUp.IsZero() && quote.LimitDown.IsZero(), "港股没有涨跌停价")
	halted, err := hk.GetQuote(context.Background(), exchange.NewSymbolKey(exchange.MarketTypeStock, "00204.XHKG"))
	require.NoError(t, err)
	require.True(t, halted.Halted && halted.Bid.IsZero() && halted.Ask.IsZero())
	require.Equal(t, "390.4", halted.Last.String(), "停牌按昨收计")

	plain := &Market{Label: "A 股", Now: func() time.Time { return now }, Quotes: quotes}
	quote, err = plain.GetQuote(context.Background(), key)
	require.NoError(t, err)
	require.True(t, quote.Bid.IsZero() && quote.Ask.IsZero(), "没开时不补盘口")
}
