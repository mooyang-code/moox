package ashare

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
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

func TestSessionCanOrderStopsAt1457AndLunch(t *testing.T) {
	session, err := NewSession()
	require.NoError(t, err)
	cases := map[string]bool{
		"2026-11-06 09:29:59": false,
		"2026-11-06 09:30:00": true,
		"2026-11-06 11:29:59": true,
		"2026-11-06 11:30:00": false,
		"2026-11-06 13:00:00": true,
		"2026-11-06 14:56:59": true,
		"2026-11-06 14:57:00": false, // 收盘集合竞价不发新单
		"2026-11-06 15:00:00": false,
		"2026-11-07 10:00:00": false, // 周六
		"2026-10-05 10:00:00": false, // 国庆
	}
	for at, want := range cases {
		got, reason := session.CanOrder(cst(at))
		require.Equal(t, want, got, at)
		if !want {
			require.Equal(t, execution.ReasonOutOfSession, reason, at)
		}
	}
	// 14:57-15:00 仍在市场时段内：报价新鲜度、撮合照常。
	require.True(t, session.InSession(cst("2026-11-06 14:58:00")))
	// 交易日历过期按休市处理，原因单独写。
	ok, reason := session.CanOrder(cst("2028-01-04 10:00:00"))
	require.False(t, ok)
	require.Equal(t, ReasonCalendarExpired, reason)
	require.False(t, session.InSession(cst("2028-01-04 10:00:00")))
	require.True(t, session.LastClose(cst("2028-01-04 10:00:00")).IsZero())
}

type catalog map[exchange.Exchange]execution.ExchangeDescriptor

func (c catalog) Descriptor(name exchange.Exchange) (execution.ExchangeDescriptor, bool) {
	descriptor, ok := c[name]
	return descriptor, ok
}

// 时段外只要求报价时间不早于最近一次休市减 5 分钟：夜里、午休、周末用收盘价，账户保持就绪。
func TestQuoteFreshOutsideSessionAcceptsCloseQuotes(t *testing.T) {
	session, err := NewSession()
	require.NoError(t, err)
	exchanges := catalog{exchange.ExchangeAShare: {Exchange: exchange.ExchangeAShare, Session: session}}
	fresh := func(quote, now string) bool {
		return execution.QuoteFresh(exchanges, string(exchange.ExchangeAShare), cst(quote), cst(now), 10*time.Second)
	}
	require.True(t, fresh("2026-11-06 15:00:01", "2026-11-07 10:00:00"), "周六用周五收盘价")
	require.True(t, fresh("2026-11-06 14:55:00", "2026-11-09 09:00:00"), "冷门股收盘快照早于 15:00 也算")
	require.False(t, fresh("2026-11-06 14:54:59", "2026-11-07 10:00:00"))
	require.True(t, fresh("2026-11-06 11:29:58", "2026-11-06 12:00:00"), "午休用上午收盘价")
	require.False(t, fresh("2026-11-06 11:29:58", "2026-11-06 13:00:30"), "下午开盘后照原窗口")
	require.True(t, fresh("2026-11-06 13:00:25", "2026-11-06 13:00:30"))
	// 加密货币全天在时段内，行为不变。
	require.False(t, execution.QuoteFresh(exchanges, "BINANCE", cst("2026-11-06 15:00:01"), cst("2026-11-07 10:00:00"), 10*time.Second))
	require.True(t, execution.QuoteFresh(exchanges, "BINANCE", cst("2026-11-07 09:59:55"), cst("2026-11-07 10:00:00"), 10*time.Second))
}

func TestMarketQuoteFromSnapshot(t *testing.T) {
	now := cst("2026-11-07 10:00:00")
	calls := 0
	market := &Market{Now: func() time.Time { return now }, Quotes: func(_ context.Context, ids []string) (map[string]cnstock.Quote, error) {
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
	market := &Market{Now: func() time.Time { return now }, Quotes: func(context.Context, []string) (map[string]cnstock.Quote, error) {
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

func TestLoadInstrumentsUsesSeedUntilListLoads(t *testing.T) {
	seedCalls := 0
	market := &Market{Seed: func(context.Context) ([]exchange.Instrument, error) {
		seedCalls++
		return nil, nil
	}, List: func(context.Context) ([]cnstock.Stock, error) {
		return []cnstock.Stock{{InstrumentID: "600519.XSHG"}, {InstrumentID: "688981.XSHG"}, {InstrumentID: "920045.XBSE"}}, nil
	}}
	_, err := market.LoadInstruments(context.Background())
	require.ErrorIs(t, err, errListPending, "库里也没有时报错，会话稍后重试")
	require.Equal(t, 1, seedCalls)
	require.NoError(t, market.RefreshList(context.Background()))
	instruments, err := market.LoadInstruments(context.Background())
	require.NoError(t, err)
	require.Len(t, instruments, 3)
	byID := map[string]exchange.Instrument{}
	for _, instrument := range instruments {
		byID[instrument.InstrumentID] = instrument
	}
	main := byID["600519.XSHG"]
	require.Equal(t, exchange.ExchangeAShare, main.Exchange)
	require.Equal(t, exchange.MarketTypeStock, main.MarketType)
	require.Equal(t, "600519.XSHG", main.BaseAsset)
	require.Equal(t, "CNY", main.QuoteAsset)
	require.Equal(t, "100", main.ExchangeQuantityStep.String())
	require.Equal(t, "100", main.MinExchangeQuantity.String())
	require.Equal(t, "0.01", main.PriceTick.String())
	require.Equal(t, "1.02", main.MaxBuyPriceRatio.String())
	require.Equal(t, "TRADING", main.Status)
	require.Equal(t, "1", byID["688981.XSHG"].ExchangeQuantityStep.String())
	require.Equal(t, "200", byID["688981.XSHG"].MinExchangeQuantity.String())
	require.Equal(t, "100", byID["920045.XBSE"].MinExchangeQuantity.String())
	require.Equal(t, 1, seedCalls, "列表加载后不再读库")
}

func TestAdapterDoesNotPlaceOrders(t *testing.T) {
	adapter := NewAdapter(&Market{})
	_, err := adapter.PlaceOrder(context.Background(), exchange.OrderRequest{})
	require.True(t, errors.Is(err, ErrNoOrders))
	require.Equal(t, exchange.ExchangeAShare, adapter.Exchange())
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
	market := &Market{Now: func() time.Time { return now }, Quotes: func(_ context.Context, ids []string) (map[string]cnstock.Quote, error) {
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
	market := &Market{Quotes: func(context.Context, []string) (map[string]cnstock.Quote, error) {
		calls++
		return nil, cnstock.ErrProtocol
	}}
	key := exchange.NewSymbolKey(exchange.MarketTypeStock, "600519.XSHG")
	for range 2 {
		_, err := market.GetQuote(context.Background(), key)
		require.ErrorIs(t, err, cnstock.ErrProtocol)
	}
	require.Equal(t, 2, calls)

	missing := &Market{Quotes: func(context.Context, []string) (map[string]cnstock.Quote, error) {
		return map[string]cnstock.Quote{}, nil
	}}
	_, err := missing.GetQuote(context.Background(), key)
	require.ErrorContains(t, err, "没有标的")
}
