package ashare

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/stockfeed"
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

func TestLoadInstrumentsUsesSeedUntilListLoads(t *testing.T) {
	seedCalls := 0
	market := newMarket(nil, func(context.Context) ([]cnstock.Stock, error) {
		return []cnstock.Stock{{InstrumentID: "600519.XSHG"}, {InstrumentID: "688981.XSHG"}, {InstrumentID: "920045.XBSE"}}, nil
	}, func(context.Context) ([]exchange.Instrument, error) {
		seedCalls++
		return nil, nil
	})
	_, err := market.LoadInstruments(context.Background())
	require.ErrorIs(t, err, stockfeed.ErrListPending, "库里也没有时报错，会话稍后重试")
	require.ErrorContains(t, err, "A 股标的列表还没有加载完")
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
