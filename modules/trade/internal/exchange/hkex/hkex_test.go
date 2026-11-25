package hkex

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/stockfeed"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/packages/cnstock"
	"github.com/stretchr/testify/require"
)

// hkt 把香港时间写成 UTC 时刻。
func hkt(value string) time.Time {
	parsed, err := time.Parse("2006-01-02 15:04:05", value)
	if err != nil {
		panic(err)
	}
	return parsed.Add(-8 * time.Hour).UTC()
}

// 港股时段：09:30-12:00、13:00-16:00，连续竞价时段内都能发单（没有 A 股那样的 14:57 截止）；半日市只有上午。
func TestSessionFollowsHKHoursAndHalfDays(t *testing.T) {
	session, err := NewSession()
	require.NoError(t, err)
	cases := map[string]bool{
		"2026-11-17 09:29:59": false,
		"2026-11-17 09:30:00": true,
		"2026-11-17 11:45:00": true, // A 股此时已午休，港股上午到 12:00
		"2026-11-17 11:59:59": true,
		"2026-11-17 12:00:00": false,
		"2026-11-17 12:59:59": false,
		"2026-11-17 13:00:00": true,
		"2026-11-17 15:59:59": true,  // 到收市前都能发单
		"2026-11-17 16:00:00": false, // 收市竞价不参与
		"2026-11-21 10:00:00": false, // 周六
		"2026-12-24 11:59:59": true,  // 半日市上午
		"2026-12-24 13:30:00": false, // 半日市下午休市
		"2026-12-25 10:00:00": false, // 圣诞节
	}
	for at, want := range cases {
		got, reason := session.CanOrder(hkt(at))
		require.Equal(t, want, got, at)
		require.Equal(t, want, session.InSession(hkt(at)), at)
		if !want {
			require.Equal(t, execution.ReasonOutOfSession, reason, at)
		}
	}
	require.Equal(t, hkt("2026-11-17 12:00:00"), session.LastBreak(hkt("2026-11-17 12:30:00")), "午间休市从 12:00 起")
	require.Equal(t, hkt("2026-11-17 16:00:00"), session.LastClose(hkt("2026-11-17 20:00:00")))
	require.Equal(t, hkt("2026-12-24 12:00:00"), session.LastClose(hkt("2026-12-24 15:00:00")), "半日市 12:00 就是日收盘")
	ok, reason := session.CanOrder(hkt("2030-01-03 10:00:00"))
	require.False(t, ok)
	require.Equal(t, stockfeed.ReasonCalendarExpired, reason)
}

// 时段外的报价新鲜度：收市后、午休用最近一次休市前后的报价（腾讯收市后的行情时间约在 16:08）。
func TestQuoteFreshOutsideHKSession(t *testing.T) {
	session, err := NewSession()
	require.NoError(t, err)
	exchanges := catalog{exchange.ExchangeHKEX: {Exchange: exchange.ExchangeHKEX, Session: session}}
	fresh := func(quote, now string) bool {
		return execution.QuoteFresh(exchanges, "hkex", hkt(quote), hkt(now), 10*time.Second)
	}
	require.True(t, fresh("2026-11-17 16:08:19", "2026-11-17 20:00:00"), "收市后用收市价")
	require.True(t, fresh("2026-11-17 11:59:58", "2026-11-17 12:30:00"), "午休用上午收市价")
	require.False(t, fresh("2026-11-17 11:59:58", "2026-11-17 13:00:30"), "下午开盘后照原窗口")
	require.True(t, fresh("2026-12-24 12:00:05", "2026-12-24 15:00:00"), "半日市下午用 12:00 的收市价")
	require.False(t, fresh("2026-12-24 11:30:00", "2026-12-24 15:00:00"))
}

type catalog map[exchange.Exchange]execution.ExchangeDescriptor

func (c catalog) Descriptor(name exchange.Exchange) (execution.ExchangeDescriptor, bool) {
	descriptor, ok := c[name]
	return descriptor, ok
}

// 标的表：恒生指数成分加库里已有的标的；每手股数取自快照、逐只不同；取不到每手股数的新标的不收录，库里已有的沿用原规则。
func TestListInstrumentsUsesLotSizeFromSnapshot(t *testing.T) {
	now := hkt("2026-11-17 08:30:00")
	var asked []string
	quotes := func(_ context.Context, ids []string) (map[string]cnstock.Quote, error) {
		asked = ids
		return map[string]cnstock.Quote{
			"00700.XHKG": {InstrumentID: "00700.XHKG", Last: "376.000", LotSize: 100},
			"00005.XHKG": {InstrumentID: "00005.XHKG", Last: "138.500", LotSize: 400},
			"09988.XHKG": {InstrumentID: "09988.XHKG", Last: "85.450"}, // 没有每手股数
			"00011.XHKG": {InstrumentID: "00011.XHKG", Last: "120.000"},
		}, nil
	}
	constituents := func(context.Context) ([]string, error) {
		return []string{"00700.XHKG", "00005.XHKG", "09988.XHKG", "01810.XHKG"}, nil
	}
	old := stockInstrument("00011.XHKG", 200, hkt("2026-11-01 08:30:00"))
	seed := func(context.Context) ([]exchange.Instrument, error) {
		return []exchange.Instrument{old, stockInstrument("00700.XHKG", 50, hkt("2026-11-01 08:30:00"))}, nil
	}
	market := NewMarketFrom(quotes, constituents, seed)
	market.Now = func() time.Time { return now }
	require.NoError(t, market.RefreshList(context.Background()))
	require.Equal(t, []string{"00700.XHKG", "00005.XHKG", "09988.XHKG", "01810.XHKG", "00011.XHKG"}, asked, "成分与库里已有的标的都要取快照，重复的只取一次")
	instruments, err := market.LoadInstruments(context.Background())
	require.NoError(t, err)
	byID := map[string]exchange.Instrument{}
	for _, instrument := range instruments {
		byID[instrument.InstrumentID] = instrument
	}
	require.Len(t, byID, 3, "09988 取不到每手股数、01810 没有快照：都不收录")
	tencent := byID["00700.XHKG"]
	require.Equal(t, exchange.ExchangeHKEX, tencent.Exchange)
	require.Equal(t, exchange.MarketTypeEquity, tencent.MarketType)
	require.Equal(t, "00700.XHKG", tencent.ExchangeSymbol)
	require.Equal(t, "00700.XHKG", tencent.BaseAsset)
	require.Equal(t, "HKD", tencent.QuoteAsset)
	require.Equal(t, "HKD", tencent.SettlementAsset)
	require.Equal(t, "100", tencent.ExchangeQuantityStep.String(), "快照里的每手股数优先于库里的旧值")
	require.Equal(t, "100", tencent.MinExchangeQuantity.String())
	require.Equal(t, "0.001", tencent.PriceTick.String())
	require.True(t, tencent.MaxBuyPriceRatio.IsZero() && tencent.MinSellPriceRatio.IsZero(), "港股没有价格笼子")
	require.Equal(t, now, tencent.ExchangeUpdatedAt)
	require.Equal(t, "400", byID["00005.XHKG"].ExchangeQuantityStep.String())
	require.Equal(t, old, byID["00011.XHKG"], "库里已有、这次取不到每手股数的沿用原规则")
	require.NotContains(t, byID, "09988.XHKG")
	require.NotContains(t, byID, "01810.XHKG")
}

// 一只都取不到每手股数时刷新失败，沿用已有列表；成分接口失败同样不替换。
func TestListInstrumentsFailsWithoutAnyLotSize(t *testing.T) {
	market := NewMarketFrom(
		func(context.Context, []string) (map[string]cnstock.Quote, error) {
			return map[string]cnstock.Quote{}, nil
		},
		func(context.Context) ([]string, error) { return []string{"00700.XHKG"}, nil }, nil)
	require.ErrorContains(t, market.RefreshList(context.Background()), "没有一只标的取得到每手股数")
	_, err := market.LoadInstruments(context.Background())
	require.ErrorIs(t, err, stockfeed.ErrListPending)
	require.ErrorContains(t, err, "港股标的列表还没有加载完")

	failing := NewMarketFrom(nil, func(context.Context) ([]string, error) { return nil, cnstock.ErrTimeout }, nil)
	require.True(t, errors.Is(failing.RefreshList(context.Background()), cnstock.ErrTimeout))
}

func TestMarketIsHKEXWithFilledBook(t *testing.T) {
	market := NewMarket(nil, nil)
	require.Equal(t, exchange.ExchangeHKEX, market.Exchange)
	require.True(t, market.FillEmptyBook)
	require.True(t, market.QuoteTimeIsLastTrade)
	require.Equal(t, exchange.ExchangeHKEX, stockfeed.NewAdapter(market).Exchange())
}

// 腾讯 r_hk 第 30 字段是最后一笔成交的时间（2026-11-18 盘中实测：00027 相隔 22 秒两次都是 09:42:43）。
// 报价时间按取到快照的时刻算：成交稀疏的标的盘中不判过期，午休、收市后也照样新鲜；停牌照旧。
func TestQuoteTimeIsFetchTimeNotLastTrade(t *testing.T) {
	session, err := NewSession()
	require.NoError(t, err)
	exchanges := catalog{exchange.ExchangeHKEX: {Exchange: exchange.ExchangeHKEX, Session: session}}
	var now time.Time
	quotes := func(_ context.Context, ids []string) (map[string]cnstock.Quote, error) {
		result := map[string]cnstock.Quote{}
		for _, id := range ids {
			switch id {
			case "00027.XHKG":
				// 最后一笔成交在 5 分钟前，盘口字段等于现价。
				result[id] = cnstock.Quote{InstrumentID: id, Last: "33.660", PrevClose: "33.440", Bid: "33.660", Ask: "33.660",
					Time: now.Add(-5 * time.Minute)}
			case "00204.XHKG":
				result[id] = cnstock.Quote{InstrumentID: id, Last: "0.320", PrevClose: "0.320", Time: now.Add(-48 * time.Hour), Halted: true}
			}
		}
		return result, nil
	}
	market := NewMarketFrom(quotes, nil, nil)
	market.Now = func() time.Time { return now }
	thin := exchange.NewSymbolKey(exchange.MarketTypeEquity, "00027.XHKG")
	halted := exchange.NewSymbolKey(exchange.MarketTypeEquity, "00204.XHKG")
	for _, at := range []string{"2026-11-18 09:47:43", "2026-11-18 12:30:00", "2026-11-18 13:00:30", "2026-11-18 16:30:00"} {
		now = hkt(at)
		quote, err := market.GetQuote(context.Background(), thin)
		require.NoError(t, err)
		require.Equal(t, now, quote.SourceTime, at)
		require.True(t, execution.QuoteFresh(exchanges, "hkex", quote.SourceTime, now, 10*time.Second), at)
		require.Equal(t, "33.66", quote.Last.String())
		stopped, err := market.GetQuote(context.Background(), halted)
		require.NoError(t, err)
		require.True(t, stopped.Halted)
		require.Equal(t, now, stopped.SourceTime, at)
	}
}
