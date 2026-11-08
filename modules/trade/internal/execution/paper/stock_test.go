package paper

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/ashare"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

type stockCatalog map[exchange.Exchange]execution.ExchangeDescriptor

func (c stockCatalog) Descriptor(name exchange.Exchange) (execution.ExchangeDescriptor, bool) {
	descriptor, ok := c[name]
	return descriptor, ok
}

func ashareCatalog(t *testing.T) stockCatalog {
	t.Helper()
	session, err := ashare.NewSession()
	require.NoError(t, err)
	return stockCatalog{exchange.ExchangeAShare: {Exchange: exchange.ExchangeAShare, Session: session}}
}

// beijingTime 把北京时间写成 UTC 时刻。
func beijingTime(value string) time.Time {
	parsed, err := time.Parse("2006-01-02 15:04:05", value)
	if err != nil {
		panic(err)
	}
	return parsed.Add(-8 * time.Hour).UTC()
}

func stockDeciderFixture(t *testing.T) (*Decider, *deciderAdapter, store.OrderRecord, *store.Store) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "ASHARE", Environment: "PRODUCTION", MarketType: "STOCK", ExchangeSymbol: "600519.XSHG",
			InstrumentID: "600519.XSHG", BaseAsset: "600519.XSHG", QuoteAsset: "CNY", SettlementAsset: "CNY",
			ExchangeQuantityStep: "100", MinExchangeQuantity: "100", PriceTick: "0.01", Status: "TRADING",
		}); err != nil {
			return err
		}
		return tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space", TradingAccountID: "paper", Name: "A 股", Exchange: "ASHARE",
			Markets: []string{"STOCK"}, ExecutionMode: "PAPER", SettlementAsset: "CNY", Status: "ENABLED",
			PaperConfig: &store.PaperAccountConfigRecord{SpaceID: "space", TradingAccountID: "paper",
				InitialBalance: "50000000", MakerFeeRate: "0.00025", TakerFeeRate: "0.00025", SlippageBPS: "0"},
		})
	}))
	now := beijingTime("2026-11-06 10:00:00")
	adapter := &deciderAdapter{
		quote: execution.MarketQuote{Bid: shared.MustDecimal("9.99"), Ask: shared.MustDecimal("10"), Last: shared.MustDecimal("10"),
			LimitUp: shared.MustDecimal("11"), LimitDown: shared.MustDecimal("9"), SourceTime: now},
		snapshot: exchange.AccountSnapshot{Balances: []exchange.AssetBalance{
			{Wallet: exchange.WalletUnified, Asset: "CNY", Available: shared.MustDecimal("0")},
		}},
	}
	limit := "10.2"
	return &Decider{Store: db, Adapters: deciderAdapters{adapter}, Exchanges: ashareCatalog(t), Now: func() time.Time { return now }}, adapter, store.OrderRecord{
		SpaceID: "space", TradingAccountID: "paper", OrderID: "order", ClientOrderID: "client", ExchangeOrderID: "exchange-order",
		InstrumentID: "600519.XSHG", ExchangeSymbol: "600519.XSHG", MarketType: "STOCK", Side: "BUY",
		OrderType: "LIMIT", TimeInForce: "IOC", LimitPrice: &limit, FirstMatchPending: true,
		Quantity: "100", ReferencePrice: "10", ReservedAsset: "CNY", RemainingReservedQuantity: "1005.01",
	}, db
}

// 小额买单：佣金按最低 5 元收，加过户费；预留的 1005.01 正好够，余额不会变成负数。
func TestStockDeciderChargesMinimumCommissionAndTransferFee(t *testing.T) {
	d, _, order, _ := stockDeciderFixture(t)
	decision, err := d.Decide(context.Background(), order)
	require.NoError(t, err)
	require.False(t, decision.Cancel)
	require.Equal(t, "10", decision.Fill.Price.String())
	require.Equal(t, "5.01", decision.Fill.Fee.String(), "佣金 0.25 不足 5 元按 5 元，过户费 0.01")
	require.Equal(t, "CNY", decision.Fill.FeeAsset)
	require.Equal(t, order.RemainingReservedQuantity, exchange.StockBuyCost(shared.MustDecimal("1000"), shared.MustDecimal("0.00025")).String(),
		"下单预留和撮合扣费是同一个函数")
}

// 卖出加印花税；卖单不读快照。
func TestStockDeciderSellAddsStampDuty(t *testing.T) {
	d, adapter, order, _ := stockDeciderFixture(t)
	order.Side, order.ReservedAsset, order.RemainingReservedQuantity = "SELL", "600519.XSHG", "10000"
	limit := "9"
	order.LimitPrice, order.Quantity = &limit, "10000"
	adapter.snapshot = exchange.AccountSnapshot{}
	decision, err := d.Decide(context.Background(), order)
	require.NoError(t, err)
	// 按下单时的买一 10 元成交，成交额 100000：佣金 25，过户费 1，印花税 50。
	require.Equal(t, "10", decision.Fill.Price.String())
	require.Equal(t, "76", decision.Fill.Fee.String())
}

// 没有对手价：涨停没有卖一时 IOC 买单整笔撤掉；GTC 限价单继续挂着；停牌一样。
func TestStockDeciderNoCounterparty(t *testing.T) {
	d, adapter, order, _ := stockDeciderFixture(t)
	adapter.quote.Ask, adapter.quote.Last = shared.Zero(), shared.MustDecimal("11")
	decision, err := d.Decide(context.Background(), order)
	require.NoError(t, err)
	require.True(t, decision.Cancel)
	require.Equal(t, "没有卖一，成交不了", decision.Reason)

	order.TimeInForce = "GTC"
	decision, err = d.Decide(context.Background(), order)
	require.NoError(t, err)
	require.True(t, decision.Rest)

	order.TimeInForce = "IOC"
	adapter.quote = execution.MarketQuote{Last: shared.MustDecimal("10"), SourceTime: d.Now(), Halted: true}
	decision, err = d.Decide(context.Background(), order)
	require.NoError(t, err)
	require.True(t, decision.Cancel)
	require.Equal(t, "停牌", decision.Reason)
}

// 时段外不撮合：挂着的单留到开盘，还没撮合过的单整笔撤掉（按下单时的价格成交会用到旧价）。
func TestMatcherSkipsOutsideSession(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "matcher.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	limit := "10"
	require.NoError(t, s.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{SpaceID: "s", TradingAccountID: "a", Name: "a", Exchange: "ASHARE", Markets: []string{"STOCK"}, ExecutionMode: "PAPER", SettlementAsset: "CNY", Status: "ENABLED"}); err != nil {
			return err
		}
		for _, item := range []struct {
			id    string
			first bool
		}{{"fresh", true}, {"resting", false}} {
			if err := tx.CreateOrder(store.OrderRecord{SpaceID: "s", TradingAccountID: "a", OrderID: item.id, ClientOrderID: item.id, ExchangeOrderID: item.id,
				MarketType: "STOCK", ExchangeSymbol: "600519.XSHG", InstrumentID: "600519.XSHG", OrderType: "LIMIT", TimeInForce: "GTC", LimitPrice: &limit,
				Side: "BUY", Quantity: "100", ReferencePrice: "10", OwnerType: "EXTERNAL", OwnerID: item.id, State: "OPEN", FirstMatchPending: item.first}); err != nil {
				return err
			}
		}
		return nil
	}))
	decided := 0
	m := &Matcher{Store: s, Reducer: &consumer.Reducer{Store: s}, Exchanges: ashareCatalog(t),
		Now: func() time.Time { return beijingTime("2026-11-06 12:00:00") }}
	m.DecideContext = func(context.Context, store.OrderRecord) (Decision, error) {
		decided++
		return Decision{}, nil
	}
	require.NoError(t, m.Scan(context.Background()))
	require.Zero(t, decided, "时段外不检查盘口")
	fresh, err := s.GetOrder(context.Background(), "s", "fresh")
	require.NoError(t, err)
	require.Equal(t, "CANCELED", fresh.State)
	require.Equal(t, OutOfSessionReason, fresh.RejectReason)
	resting, err := s.GetOrder(context.Background(), "s", "resting")
	require.NoError(t, err)
	require.Equal(t, "OPEN", resting.State)
}

type stockPublicData struct {
	price exchange.ReferencePrice
}

func (d stockPublicData) LoadInstruments(context.Context) ([]exchange.Instrument, error) {
	return nil, nil
}
func (d stockPublicData) GetQuote(context.Context, exchange.SymbolKey) (execution.MarketQuote, error) {
	return execution.MarketQuote{Last: d.price.Price, SourceTime: d.price.UpdatedAt}, nil
}
func (d stockPublicData) GetReferencePrice(context.Context, exchange.SymbolKey) (exchange.ReferencePrice, error) {
	return d.price, nil
}

// 模拟盘估值：周六用周五收盘价，账户快照照常算出来（不触发 valuation_degraded）；时段内照原来的 10 秒。
func TestStockValuationQuoteUsesCloseOutsideSession(t *testing.T) {
	adapter := &Adapter{
		Account:    store.TradingAccountRecord{Exchange: "ASHARE"},
		MarketData: stockPublicData{price: exchange.ReferencePrice{Price: shared.MustDecimal("1453"), UpdatedAt: beijingTime("2026-11-06 16:14:58")}},
		Exchanges:  ashareCatalog(t),
		Now:        func() time.Time { return beijingTime("2026-11-07 10:00:00") },
	}
	key := exchange.NewSymbolKey(exchange.MarketTypeStock, "600519.XSHG")
	price, err := adapter.valuationQuote(context.Background(), key)
	require.NoError(t, err)
	require.Equal(t, "1453", price.String())

	adapter.Now = func() time.Time { return beijingTime("2026-11-09 09:31:00") }
	_, err = adapter.valuationQuote(context.Background(), key)
	require.ErrorContains(t, err, "stale")
}

// 降级用的最后已知价：时段外按收盘价规则判断，夜里不会过了 90 秒就失效。
func TestDegradedLastPriceFollowsSession(t *testing.T) {
	catalog := ashareCatalog(t)
	fresh := func(now time.Time) func(time.Time) bool {
		return func(at time.Time) bool {
			return execution.QuoteFresh(catalog, "ASHARE", at, now, execution.LastPriceMaxAge)
		}
	}
	last := execution.LastPrice{Price: shared.MustDecimal("10"), At: beijingTime("2026-11-06 15:00:03")}
	value, err := execution.DegradedPrice(context.Background(), execution.ValuationQuoteFailed, last,
		fresh(beijingTime("2026-11-07 03:00:00")), nil, context.DeadlineExceeded)
	require.NoError(t, err)
	require.Equal(t, "10", value.Price.String())
	_, err = execution.DegradedPrice(context.Background(), execution.ValuationQuoteFailed, last,
		fresh(beijingTime("2026-11-09 10:00:00")), nil, context.DeadlineExceeded)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

type t1Store struct {
	FactStore
	bought map[string]shared.Decimal
	since  int64
}

func (s *t1Store) PaperT1Bought(_ context.Context, _, _ string, since int64) (map[string]shared.Decimal, error) {
	s.since = since
	return s.bought, nil
}

// T+1：可卖 = 总额 − 冻结 − 当日（北京时间当天）买入；执行器的可卖数量取自这里。
func TestApplyT1SubtractsTodayBuys(t *testing.T) {
	source := &t1Store{bought: map[string]shared.Decimal{"600519.XSHG": shared.MustDecimal("200")}}
	adapter := &Adapter{Store: source, Now: func() time.Time { return beijingTime("2026-11-06 10:00:00") }}
	rows := paperBalances(map[string]shared.Decimal{"600519.XSHG": shared.MustDecimal("300"), "CNY": shared.MustDecimal("100")},
		map[string]shared.Decimal{"600519.XSHG": shared.MustDecimal("50")})
	require.NoError(t, adapter.applyT1(context.Background(), store.TradingAccountRecord{Markets: []string{"STOCK"}}, rows))
	byAsset := map[string]exchange.AssetBalance{}
	for _, row := range rows {
		byAsset[row.Asset] = row
	}
	require.Equal(t, "50", byAsset["600519.XSHG"].Available.String())
	require.Equal(t, "300", byAsset["600519.XSHG"].Total.String(), "持仓仍按总额算")
	require.Equal(t, "100", byAsset["CNY"].Available.String())
	require.Equal(t, beijingTime("2026-11-06 00:00:00").UnixMilli(), source.since)

	// 加密货币账户不查当日买入。
	source.since = 0
	require.NoError(t, adapter.applyT1(context.Background(), store.TradingAccountRecord{Markets: []string{"SPOT"}}, rows))
	require.Zero(t, source.since)
}
