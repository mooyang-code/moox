package target

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/hkex"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/stockfeed"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// hkNow 是 2026-11-17（周二）10:00 香港时间，在交易时段内。
var hkNow = time.Date(2026, 11, 17, 2, 0, 0, 0, time.UTC)

// hkInstrument 是一只港股：每手 lot 股，标的表里的价格步长是价位表最小的一档，没有价格笼子。
func hkInstrument(id, lot string) store.InstrumentRecord {
	return store.InstrumentRecord{
		Exchange: "HKEX", MarketType: "STOCK", ExchangeSymbol: id, InstrumentID: id,
		BaseAsset: id, QuoteAsset: "HKD", SettlementAsset: "HKD",
		ExchangeQuantityStep: lot, MinExchangeQuantity: lot, PriceTick: exchange.HKMinPriceTick,
		MaxBuyPriceRatio: "0", MinSellPriceRatio: "0", Status: "TRADING",
	}
}

// hkQuote 是港股报价：没有涨跌停价，买一卖一各差一个价位。
func hkQuote(price, bid, ask string, at time.Time) Quote {
	return Quote{Price: shared.MustDecimal(price), UpdatedAt: at, Bid: shared.MustDecimal(bid), Ask: shared.MustDecimal(ask)}
}

func newHKFixture(t *testing.T) *stockFixture {
	t.Helper()
	fixture := newStockExchangeFixture(t, exchange.ExchangeHKEX, "HKD", hkNow, hkInstrument("00700.XHKG", "100"), hkInstrument("00005.XHKG", "400"))
	fixture.quotes["00700.XHKG"] = hkQuote("376", "375.8", "376", hkNow)
	fixture.quotes["00005.XHKG"] = hkQuote("138.5", "138.4", "138.5", hkNow)
	return fixture
}

// at 把夹具的当前时刻和报价时间一起移到 now。
func hkAt(fixture *stockFixture, now time.Time) {
	fixture.now = now
	for id, quote := range fixture.quotes {
		quote.UpdatedAt = now
		fixture.quotes[id] = quote
	}
}

// 每手股数逐只不同：00005 每手 400 股，目标 1000 股向下取整到 800 股；00700 每手 100 股，250 股取整到 200 股。
func TestHKBuyFloorsToInstrumentLotSize(t *testing.T) {
	fixture := newHKFixture(t)
	// 价格取 60 港元，两手 4.8 万港元，不触发默认的单笔上限拆单。
	fixture.quotes["00005.XHKG"] = hkQuote("60", "59.95", "60", hkNow)
	fixture.target(t,
		store.InstrumentQuantity{InstrumentID: "00005.XHKG", Quantity: "1000"},
		store.InstrumentQuantity{InstrumentID: "00700.XHKG", Quantity: "250"})

	fixture.converge(t)

	quantities := map[string]string{}
	for _, spec := range fixture.orders.specs {
		quantities[spec.InstrumentID] = spec.Quantity.String()
	}
	require.Equal(t, map[string]string{"00005.XHKG": "800", "00700.XHKG": "200"}, quantities)
	// 不足一手的买入差额视为已满足。
	small := newHKFixture(t)
	small.target(t, store.InstrumentQuantity{InstrumentID: "00005.XHKG", Quantity: "399"})
	result, current := small.converge(t)
	require.Equal(t, StatusConverged, result.Status)
	require.Empty(t, small.orders.specs)
	require.Empty(t, current.BlockedTargets)
}

// 伪市价单的限价对齐到港交所价位表：376 × 1.03 = 387.28，200～500 港元一档 0.2，买入向上到 387.4；
// 卖出 376 × 0.97 = 364.72 向下到 364.6。港股没有价格笼子和涨跌停，不再截价。
func TestHKPseudoMarketPriceSnapsToTickTable(t *testing.T) {
	settings := `{"order_mode":"PSEUDO_MARKET","slippage_step":"0.03","slippage_max":"0.06","min_notional_spot":"0","min_notional_swap":"0"}`
	buy := newHKFixture(t)
	require.NoError(t, buy.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.SetLogicalAccountOrderSettings("space-1", "logical-1", settings)
	}))
	buy.target(t, store.InstrumentQuantity{InstrumentID: "00700.XHKG", Quantity: "200"})
	buy.converge(t)
	require.Len(t, buy.orders.specs, 1)
	require.Equal(t, exchange.OrderTypeLimit, buy.orders.specs[0].Type)
	require.Equal(t, exchange.FillPolicyIOC, buy.orders.specs[0].FillPolicy)
	require.Equal(t, "387.4", buy.orders.specs[0].LimitPrice.String())

	sell := newHKFixture(t)
	require.NoError(t, sell.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.SetLogicalAccountOrderSettings("space-1", "logical-1", settings)
	}))
	sell.holding(t, "00700.XHKG", "200", "200")
	sell.target(t, store.InstrumentQuantity{InstrumentID: "00700.XHKG", Quantity: "0"})
	sell.converge(t)
	require.Len(t, sell.orders.specs, 1)
	require.Equal(t, exchange.SideSell, sell.orders.specs[0].Side)
	require.Equal(t, "200", sell.orders.specs[0].Quantity.String())
	require.Equal(t, "364.6", sell.orders.specs[0].LimitPrice.String())
}

// 港股 T+0：没有“当日买入，次日可卖”的顺延。可卖数量为 0（例如被挂单占用）时照常记可卖量不足，不写成 T+1。
func TestHKHasNoT1Deferral(t *testing.T) {
	fixture := newHKFixture(t)
	fixture.holding(t, "00700.XHKG", "300", "0")
	fixture.target(t, store.InstrumentQuantity{InstrumentID: "00700.XHKG", Quantity: "0"})

	_, current := fixture.converge(t)

	require.Empty(t, fixture.orders.specs)
	for _, blocked := range current.BlockedTargets {
		require.NotEqual(t, reasonT1, blocked.Reason)
		require.False(t, blocked.Deferred, "港股卖不出不是 T+1 顺延")
	}
	require.NotEmpty(t, current.BlockedTargets)
}

// 停牌：顺延，不算阻塞（港股报价没有涨跌停价，只有停牌会顺延）。
func TestHKHaltedIsDeferred(t *testing.T) {
	fixture := newHKFixture(t)
	halted := hkQuote("376", "0", "0", hkNow)
	halted.Halted = true
	fixture.quotes["00700.XHKG"] = halted
	fixture.target(t, store.InstrumentQuantity{InstrumentID: "00700.XHKG", Quantity: "200"})

	result, current := fixture.converge(t)

	require.Equal(t, StatusConverged, result.Status)
	require.Empty(t, fixture.orders.specs)
	requireDeferred(t, current, "00700.XHKG", reasonHalted)
}

// 时段外的目标留到下一次开盘（与 A 股一致）：午休、收市后、半日市下午都不下单，原因写“当前不在交易时段”；
// 15:58 仍在连续竞价时段内，照常下单（A 股 14:57 之后不发新单，港股没有这个截止）；日历不覆盖写“交易日历已过期”。
func TestHKOutOfSessionTargetsWait(t *testing.T) {
	hk := time.FixedZone("HKT", 8*3600)
	for name, at := range map[string]time.Time{
		"午休":    time.Date(2026, 11, 17, 12, 30, 0, 0, hk),
		"收市后":   time.Date(2026, 11, 17, 16, 0, 0, 0, hk),
		"半日市下午": time.Date(2026, 12, 24, 13, 30, 0, 0, hk),
		"周六":    time.Date(2026, 11, 21, 10, 0, 0, 0, hk),
	} {
		fixture := newHKFixture(t)
		hkAt(fixture, at)
		fixture.target(t, store.InstrumentQuantity{InstrumentID: "00700.XHKG", Quantity: "200"})
		result, current := fixture.converge(t)
		require.Equal(t, StatusPaused, result.Status, name)
		require.Equal(t, execution.ReasonOutOfSession, current.LastError, name)
		require.Empty(t, fixture.orders.specs, name)
	}

	late := newHKFixture(t)
	hkAt(late, time.Date(2026, 11, 17, 15, 58, 0, 0, hk))
	late.target(t, store.InstrumentQuantity{InstrumentID: "00700.XHKG", Quantity: "200"})
	late.converge(t)
	require.Len(t, late.orders.specs, 1, "收市前都能发单")

	expired := newHKFixture(t)
	hkAt(expired, time.Date(2030, 1, 3, 10, 0, 0, 0, hk))
	expired.target(t, store.InstrumentQuantity{InstrumentID: "00700.XHKG", Quantity: "200"})
	_, current := expired.converge(t)
	require.Equal(t, stockfeed.ReasonCalendarExpired, current.LastError)
}

// 权重换算时标的不在任何成员的标的表里（取不到每手股数的标的不收录）：原因里写明港股模拟盘的标的范围。
func TestHKUnknownInstrumentNamesExchangeScope(t *testing.T) {
	fixture := newHKFixture(t)
	session, err := hkex.NewSession()
	require.NoError(t, err)
	resolver := &WeightResolver{Store: fixture.store, Now: func() time.Time { return fixture.now },
		Exchanges: stockCatalog{exchange.ExchangeHKEX: {Exchange: exchange.ExchangeHKEX, Session: session, InstrumentScope: hkex.InstrumentScope}}}
	members, err := fixture.store.ListLogicalAccountMembers(context.Background(), "space-1", "logical-1", false)
	require.NoError(t, err)

	_, _, err = resolver.instrumentForMembers(context.Background(), "space-1", members, "09988.XHKG")
	require.ErrorContains(t, err, "没有就绪的组合成员支持标的 09988.XHKG")
	require.ErrorContains(t, err, "港股模拟盘只收录恒生指数成分里取得到每手股数的标的")

	instrument, accountID, err := resolver.instrumentForMembers(context.Background(), "space-1", members, "00005.XHKG")
	require.NoError(t, err)
	require.Equal(t, "stock-a", accountID)
	require.Equal(t, "400", instrument.ExchangeQuantityStep)
}
