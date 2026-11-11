package target

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/ashare"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store/storetest"
	"github.com/stretchr/testify/require"
)

// stockQuoteStub 按标的给报价；没登记的标的报 10 元、有买一卖一、涨跌停 ±10%。
type stockQuoteStub map[string]Quote

func (s stockQuoteStub) LatestPrice(_ context.Context, _ string, key exchange.SymbolKey) (Quote, error) {
	if quote, ok := s[key.Symbol]; ok {
		return quote, nil
	}
	return Quote{
		Price: shared.MustDecimal("10"), UpdatedAt: stockNow,
		LimitUp: shared.MustDecimal("11"), LimitDown: shared.MustDecimal("9"),
		Bid: shared.MustDecimal("9.99"), Ask: shared.MustDecimal("10"),
	}, nil
}

type stockCatalog map[exchange.Exchange]execution.ExchangeDescriptor

func (c stockCatalog) Descriptor(name exchange.Exchange) (execution.ExchangeDescriptor, bool) {
	descriptor, ok := c[name]
	return descriptor, ok
}

// stockNow 是 2026-11-06（周五）10:00 北京时间，在交易时段内。
var stockNow = time.Date(2026, 11, 6, 2, 0, 0, 0, time.UTC)

type stockFixture struct {
	store  *store.Store
	orders *targetOrderServiceStub
	quotes stockQuoteStub
	now    time.Time
}

func newStockFixture(t *testing.T) *stockFixture {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tradeStore.Close()) })
	fixture := &stockFixture{store: tradeStore, orders: &targetOrderServiceStub{}, quotes: stockQuoteStub{}, now: stockNow}
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "stock-a", Name: "stock-a",
			Exchange: "ASHARE", Markets: []string{"STOCK"}, ExecutionMode: "PAPER", Environment: "PAPER",
			SettlementAsset: "CNY", Status: "ENABLED", Ready: true,
			Snapshot: store.TradingAccountSnapshot{
				AvailableFunds: "50000000",
				Balances:       []store.AssetBalance{{Wallet: "UNIFIED", Asset: "CNY", Available: "50000000", Total: "50000000"}},
			},
			LastSyncAt: stockNow.UnixMilli(), SnapshotSourceTime: stockNow.UnixMilli(),
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical",
			OwnerInstanceID: "runner-1", OwnerSessionID: "session-1", ExecutionMode: "PAPER",
			SettlementAsset: "CNY", OrderSettingsJSON: legacyOrderSettings,
			AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "stock-a", Enabled: true, Priority: 1,
		}); err != nil {
			return err
		}
		for _, instrument := range []store.InstrumentRecord{
			stockInstrument("600519.XSHG", "100", "100"),
			stockInstrument("688981.XSHG", "1", "200"),
		} {
			if err := tx.UpsertInstrument(instrument); err != nil {
				return err
			}
		}
		return tx.SetLogicalAccountAutomation("space-1", "logical-1", "ACTIVE", "")
	}))
	return fixture
}

func stockInstrument(id, step, minimum string) store.InstrumentRecord {
	return store.InstrumentRecord{
		Exchange: "ASHARE", MarketType: "STOCK", ExchangeSymbol: id, InstrumentID: id,
		BaseAsset: id, QuoteAsset: "CNY", SettlementAsset: "CNY",
		ExchangeQuantityStep: step, MinExchangeQuantity: minimum, PriceTick: "0.01",
		MaxBuyPriceRatio: "1.02", MinSellPriceRatio: "0.98", Status: "TRADING",
	}
}

func (f *stockFixture) executor(t *testing.T) *Executor {
	session, err := ashare.NewSession()
	require.NoError(t, err)
	return &Executor{
		Store: f.store, Orders: f.orders, Prices: f.quotes,
		Exchanges: stockCatalog{exchange.ExchangeAShare: {Exchange: exchange.ExchangeAShare, Session: session}},
		Now:       func() time.Time { return f.now },
	}
}

func (f *stockFixture) target(t *testing.T, targets ...store.InstrumentTarget) store.LogicalAccountTargetRecord {
	t.Helper()
	// 接收目标时按真实时间检查有效期：有效期末取“目标时刻 + 1 小时”和“现在 + 1 小时”里更晚的。
	validUntil := f.now.Add(time.Hour)
	if real := time.Now().Add(time.Hour); real.After(validUntil) {
		validUntil = real
	}
	record := store.LogicalAccountTargetRecord{
		SpaceID: "space-1", LogicalAccountID: "logical-1",
		TargetID: "target-current", InstanceID: "runner-1", SessionID: "session-1", StrategyID: "strategy-1",
		BarEndTime: f.now.UnixMilli(), EffectiveAt: f.now.UnixMilli(), ValidUntil: validUntil.UnixMilli(),
		Targets: targets, Status: StatusPending, AcceptedAt: f.now.UnixMilli(),
	}
	_, accepted, err := f.store.AcceptLogicalAccountTarget(context.Background(), record)
	require.NoError(t, err)
	require.True(t, accepted)
	current, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	return current
}

// holding 写入一笔股票持仓：total 是总数，available 是可卖数量（T+1 当日买入的不在里面）。
func (f *stockFixture) holding(t *testing.T, asset, total, available string) {
	t.Helper()
	f.holdingLocked(t, asset, total, available, "0")
}

// holdingLocked 同 holding，另带挂单冻结的数量：当日买入 = 总数 − 冻结 − 可卖。
func (f *stockFixture) holdingLocked(t *testing.T, asset, total, available, locked string) {
	t.Helper()
	account, err := f.store.GetTradingAccountByID(context.Background(), "stock-a")
	require.NoError(t, err)
	account.Snapshot.Balances = append(account.Snapshot.Balances, store.AssetBalance{
		Wallet: "UNIFIED", Asset: asset, Available: available, Total: total, Locked: locked,
	})
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return storetest.SeedAccount(tx, account.SpaceID, account.TradingAccountID, storetest.AccountState{
			Ready: true, Snapshot: account.Snapshot, SnapshotSourceTime: account.SnapshotSourceTime, LastSyncAt: account.LastSyncAt,
		})
	}))
}

func (f *stockFixture) converge(t *testing.T) (Result, store.LogicalAccountTargetRecord) {
	t.Helper()
	result, err := f.executor(t).Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	current, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	return result, current
}

func (f *stockFixture) hasDifference(t *testing.T) bool {
	t.Helper()
	current, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	different, err := f.executor(t).HasDifference(context.Background(), current)
	require.NoError(t, err)
	return different
}

func requireDeferred(t *testing.T, current store.LogicalAccountTargetRecord, instrumentID, reason string) {
	t.Helper()
	require.Len(t, current.BlockedTargets, 1)
	require.Equal(t, instrumentID, current.BlockedTargets[0].InstrumentID)
	require.Equal(t, reason, current.BlockedTargets[0].Reason)
	require.True(t, current.BlockedTargets[0].Deferred, "顺延不算阻塞")
}

// 买入不足一手视为已满足：开新仓也一样，不记 BLOCKED，过期时也不算未到位。
func TestStockBuyBelowOneLotIsSatisfied(t *testing.T) {
	fixture := newStockFixture(t)
	fixture.target(t, store.InstrumentTarget{InstrumentID: "600519.XSHG", Quantity: "50"})

	result, current := fixture.converge(t)

	require.Equal(t, StatusConverged, result.Status)
	require.Empty(t, fixture.orders.specs)
	require.Empty(t, current.BlockedTargets)
	require.False(t, fixture.hasDifference(t))
}

// 买入向下取整到整手。
func TestStockBuyFloorsToLot(t *testing.T) {
	fixture := newStockFixture(t)
	fixture.target(t, store.InstrumentTarget{InstrumentID: "600519.XSHG", Quantity: "250"})

	result, _ := fixture.converge(t)

	require.Equal(t, StatusConverging, result.Status)
	require.Len(t, fixture.orders.specs, 1)
	require.Equal(t, "200", fixture.orders.specs[0].Quantity.String())
	require.Equal(t, exchange.SideBuy, fixture.orders.specs[0].Side)
}

// 目标为 0、持仓是零股：一次卖掉全部可卖零股（原来会直接判完成，零股永远卖不掉）。
func TestStockSellsOddLotWhenTargetIsZero(t *testing.T) {
	fixture := newStockFixture(t)
	fixture.holding(t, "600519.XSHG", "50", "50")
	fixture.target(t, store.InstrumentTarget{InstrumentID: "600519.XSHG", Quantity: "0"})
	require.True(t, fixture.hasDifference(t), "还有可卖零股，算差额")

	result, _ := fixture.converge(t)

	require.Equal(t, StatusConverging, result.Status)
	require.Len(t, fixture.orders.specs, 1)
	require.Equal(t, exchange.SideSell, fixture.orders.specs[0].Side)
	require.Equal(t, "50", fixture.orders.specs[0].Quantity.String())
}

// 要卖的不小于全部可卖数量时卖出全部可卖数量，允许零股；否则按整手取整。
func TestStockSellAllSellableIncludingOddLot(t *testing.T) {
	fixture := newStockFixture(t)
	fixture.holding(t, "600519.XSHG", "150", "150")
	fixture.target(t, store.InstrumentTarget{InstrumentID: "600519.XSHG", Quantity: "0"})

	fixture.converge(t)

	require.Len(t, fixture.orders.specs, 1)
	require.Equal(t, "150", fixture.orders.specs[0].Quantity.String())
}

// 只因为 T+1 卖不出：顺延，原因写“当日买入，次日可卖”，不算阻塞，过期时也不算未到位。
func TestStockT1SellIsDeferred(t *testing.T) {
	fixture := newStockFixture(t)
	fixture.holding(t, "600519.XSHG", "300", "0")
	fixture.target(t, store.InstrumentTarget{InstrumentID: "600519.XSHG", Quantity: "0"})

	result, current := fixture.converge(t)

	require.Equal(t, StatusConverged, result.Status)
	require.Empty(t, fixture.orders.specs)
	requireDeferred(t, current, "600519.XSHG", reasonT1)
	require.False(t, fixture.hasDifference(t))
}

// T+1 只锁当日买入的部分：昨天的 100 股照卖。
func TestStockT1SellsYesterdayShares(t *testing.T) {
	fixture := newStockFixture(t)
	fixture.holding(t, "600519.XSHG", "300", "100")
	fixture.target(t, store.InstrumentTarget{InstrumentID: "600519.XSHG", Quantity: "0"})

	fixture.converge(t)

	require.Len(t, fixture.orders.specs, 1)
	require.Equal(t, "100", fixture.orders.specs[0].Quantity.String())
}

func TestStockHaltedIsDeferred(t *testing.T) {
	fixture := newStockFixture(t)
	fixture.quotes["600519.XSHG"] = Quote{Price: shared.MustDecimal("10"), UpdatedAt: stockNow, Halted: true}
	fixture.target(t, store.InstrumentTarget{InstrumentID: "600519.XSHG", Quantity: "1000"})

	result, current := fixture.converge(t)

	require.Equal(t, StatusConverged, result.Status)
	require.Empty(t, fixture.orders.specs)
	requireDeferred(t, current, "600519.XSHG", reasonHalted)
	require.False(t, fixture.hasDifference(t))
}

// 涨停时买、跌停时卖：价格截在涨跌停价上也成交不了，顺延，不交给滑点递增（否则次数用满记成“未到位”）。
func TestStockLimitUpBuyAndLimitDownSellAreDeferred(t *testing.T) {
	t.Run("涨停", func(t *testing.T) {
		fixture := newStockFixture(t)
		fixture.quotes["600519.XSHG"] = Quote{
			Price: shared.MustDecimal("11"), UpdatedAt: stockNow, LimitUp: shared.MustDecimal("11"), LimitDown: shared.MustDecimal("9"),
			Bid: shared.MustDecimal("11"),
		}
		fixture.target(t, store.InstrumentTarget{InstrumentID: "600519.XSHG", Quantity: "1000"})
		_, current := fixture.converge(t)
		require.Empty(t, fixture.orders.specs)
		requireDeferred(t, current, "600519.XSHG", reasonLimitUp)
		require.False(t, fixture.hasDifference(t))
	})
	t.Run("跌停", func(t *testing.T) {
		fixture := newStockFixture(t)
		fixture.holding(t, "600519.XSHG", "1000", "1000")
		fixture.quotes["600519.XSHG"] = Quote{
			Price: shared.MustDecimal("9"), UpdatedAt: stockNow, LimitUp: shared.MustDecimal("11"), LimitDown: shared.MustDecimal("9"),
			Ask: shared.MustDecimal("9"),
		}
		fixture.target(t, store.InstrumentTarget{InstrumentID: "600519.XSHG", Quantity: "0"})
		_, current := fixture.converge(t)
		require.Empty(t, fixture.orders.specs)
		requireDeferred(t, current, "600519.XSHG", reasonLimitDown)
		require.False(t, fixture.hasDifference(t))
	})
	t.Run("涨停但有卖一照常买", func(t *testing.T) {
		fixture := newStockFixture(t)
		fixture.quotes["600519.XSHG"] = Quote{
			Price: shared.MustDecimal("11"), UpdatedAt: stockNow, LimitUp: shared.MustDecimal("11"), LimitDown: shared.MustDecimal("9"),
			Bid: shared.MustDecimal("10.99"), Ask: shared.MustDecimal("11"),
		}
		fixture.target(t, store.InstrumentTarget{InstrumentID: "600519.XSHG", Quantity: "1000"})
		fixture.converge(t)
		require.Len(t, fixture.orders.specs, 1)
	})
}

// 下单校验看到停牌（ErrInstrumentHalted）：顺延，不拖住整个目标。
func TestStockPlaceHaltedErrorIsDeferred(t *testing.T) {
	fixture := newStockFixture(t)
	fixture.orders.placeErrors = map[string]error{"stock-a": orderapp.ErrInstrumentHalted}
	fixture.target(t, store.InstrumentTarget{InstrumentID: "600519.XSHG", Quantity: "1000"})

	result, current := fixture.converge(t)

	require.Equal(t, StatusConverged, result.Status)
	requireDeferred(t, current, "600519.XSHG", reasonHalted)
}

// 伪市价单的价格先截到价格笼子，再截到涨跌停价之间（涨跌停是绝对价）。
func TestStockPseudoMarketPriceClampedToLimitUp(t *testing.T) {
	fixture := newStockFixture(t)
	require.NoError(t, fixture.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.SetLogicalAccountOrderSettings("space-1", "logical-1", `{"order_mode":"PSEUDO_MARKET","slippage_step":"0.03","slippage_max":"0.06","min_notional_spot":"0","min_notional_swap":"0"}`)
	}))
	fixture.quotes["600519.XSHG"] = Quote{
		Price: shared.MustDecimal("10"), UpdatedAt: stockNow, LimitUp: shared.MustDecimal("10.05"), LimitDown: shared.MustDecimal("9"),
		Bid: shared.MustDecimal("9.99"), Ask: shared.MustDecimal("10"),
	}
	fixture.target(t, store.InstrumentTarget{InstrumentID: "600519.XSHG", Quantity: "1000"})

	fixture.converge(t)

	require.Len(t, fixture.orders.specs, 1)
	require.NotNil(t, fixture.orders.specs[0].LimitPrice)
	require.Equal(t, "10.05", fixture.orders.specs[0].LimitPrice.String())
}

// 交易日历过期按休市处理，原因写“交易日历已过期”；14:57 之后不发新单。
func TestStockOutOfSessionReasons(t *testing.T) {
	fixture := newStockFixture(t)
	fixture.now = time.Date(2026, 11, 6, 6, 58, 0, 0, time.UTC) // 14:58
	fixture.target(t, store.InstrumentTarget{InstrumentID: "600519.XSHG", Quantity: "1000"})
	result, current := fixture.converge(t)
	require.Equal(t, StatusPaused, result.Status)
	require.Equal(t, execution.ReasonOutOfSession, current.LastError)
	require.Empty(t, fixture.orders.specs)

	expired := newStockFixture(t)
	expired.now = time.Date(2028, 1, 4, 2, 0, 0, 0, time.UTC)
	expired.target(t, store.InstrumentTarget{InstrumentID: "600519.XSHG", Quantity: "1000"})
	_, current = expired.converge(t)
	require.Equal(t, ashare.ReasonCalendarExpired, current.LastError)
}

// 时段检查挪到完整加载成员之前后，几个原因的先后不变：时段外成员未就绪仍写“未就绪”，资金设置的排除字段坏了仍写资金设置的错误。
func TestStockOutOfSessionKeepsReasonPrecedence(t *testing.T) {
	notReady := newStockFixture(t)
	notReady.now = time.Date(2026, 11, 6, 6, 58, 0, 0, time.UTC) // 14:58
	require.NoError(t, notReady.store.DBForTest().Exec(`UPDATE t_trading_accounts SET c_ready = 0 WHERE c_trading_account_id = 'stock-a'`).Error)
	notReady.store.InvalidateCaches()
	notReady.target(t, store.InstrumentTarget{InstrumentID: "600519.XSHG", Quantity: "1000"})
	result, current := notReady.converge(t)
	require.Equal(t, StatusPaused, result.Status)
	require.Equal(t, "成员 stock-a 未就绪", current.LastError)

	broken := newStockFixture(t)
	broken.now = time.Date(2026, 11, 6, 6, 58, 0, 0, time.UTC)
	require.NoError(t, broken.store.DBForTest().Exec(`UPDATE t_logical_accounts SET c_fund_settings_json = '{"margin_coins":["ETH"]}'`).Error)
	broken.target(t, store.InstrumentTarget{InstrumentID: "600519.XSHG", Quantity: "1000"})
	result, current = broken.converge(t)
	require.Equal(t, StatusPaused, result.Status)
	require.Contains(t, current.LastError, "资金设置的排除字段无法解析")
}

func TestClampToPriceBandUsesWiderOfRatioAndTicks(t *testing.T) {
	instrument := stockInstrument("600000.XSHG", "100", "100")
	// 低价股：2% 不到 10 个价位，按价位放宽（精确值，不留余量）。
	require.Equal(t, "1.1", ClampToPriceBand(shared.MustDecimal("1.2"), shared.MustDecimal("1"), exchange.SideBuy, instrument).String())
	require.Equal(t, "0.9", ClampToPriceBand(shared.MustDecimal("0.8"), shared.MustDecimal("1"), exchange.SideSell, instrument).String())
	// 高价股按比例：100 × 1.02。
	require.Equal(t, "102", ClampToPriceBand(shared.MustDecimal("110"), shared.MustDecimal("100"), exchange.SideBuy, instrument).String())
	require.Equal(t, "98", ClampToPriceBand(shared.MustDecimal("90"), shared.MustDecimal("100"), exchange.SideSell, instrument).String())
	// 基准价：买入看卖一、卖出看买一，没有时退回最新价。
	quote := Quote{Price: shared.MustDecimal("10"), Bid: shared.MustDecimal("9.98"), Ask: shared.MustDecimal("10.01")}
	require.Equal(t, "10.01", CageBasis(quote, exchange.SideBuy).String())
	require.Equal(t, "9.98", CageBasis(quote, exchange.SideSell).String())
	require.Equal(t, "10", CageBasis(Quote{Price: shared.MustDecimal("10")}, exchange.SideBuy).String())
	// 加密货币没有价位数，行为不变。
	crypto := store.InstrumentRecord{MarketType: "SPOT", PriceTick: "0.01", MaxBuyPriceRatio: "1.02", MinSellPriceRatio: "0.98"}
	require.Equal(t, "1.01", ClampToPriceBand(shared.MustDecimal("1.2"), shared.MustDecimal("1"), exchange.SideBuy, crypto).String())
}

type bookAdapter struct {
	execution.ExecutionAdapter
	quote execution.MarketQuote
}

func (a bookAdapter) Exchange() exchange.Exchange { return exchange.ExchangeAShare }
func (a bookAdapter) GetQuote(context.Context, exchange.SymbolKey) (execution.MarketQuote, error) {
	return a.quote, nil
}

type bookAdapters struct{ adapter execution.ExecutionAdapter }

func (a bookAdapters) Adapter(string) (execution.ExecutionAdapter, error) { return a.adapter, nil }

// 新鲜度检查点：换算用的参考价（30 秒）和盘口（10 秒）在时段外都按收盘价判断，加密货币不变。
func TestQuoteFreshnessCheckpointsFollowSession(t *testing.T) {
	session, err := ashare.NewSession()
	require.NoError(t, err)
	catalog := stockCatalog{exchange.ExchangeAShare: {Exchange: exchange.ExchangeAShare, Session: session}}
	saturday := time.Date(2026, 11, 7, 2, 0, 0, 0, time.UTC)
	closeQuote := time.Date(2026, 11, 6, 8, 14, 58, 0, time.UTC)

	resolver := &WeightResolver{Exchanges: catalog, Now: func() time.Time { return saturday }}
	require.True(t, resolver.priceFresh("ASHARE", closeQuote), "周六换算用周五收盘价")
	require.False(t, resolver.priceFresh("BINANCE", closeQuote))
	resolver.Now = func() time.Time { return stockNow }
	require.False(t, resolver.priceFresh("ASHARE", stockNow.Add(-31*time.Second)), "时段内照原来的 30 秒")

	source := ExchangePriceSource{
		Adapters:  bookAdapters{bookAdapter{quote: execution.MarketQuote{Bid: shared.MustDecimal("9"), Ask: shared.MustDecimal("10"), SourceTime: closeQuote}}},
		Exchanges: catalog, Now: func() time.Time { return saturday },
	}
	bid, ask, err := source.Book(context.Background(), "stock-a", exchange.NewSymbolKey(exchange.MarketTypeStock, "600519.XSHG"))
	require.NoError(t, err)
	require.Equal(t, "9", bid.String())
	require.Equal(t, "10", ask.String())
	source.Now = func() time.Time { return stockNow }
	_, _, err = source.Book(context.Background(), "stock-a", exchange.NewSymbolKey(exchange.MarketTypeStock, "600519.XSHG"))
	require.ErrorContains(t, err, "过期")
}

// 有涨跌停的市场一次取全报价：涨跌停价、买一卖一、停牌都从快照来。
func TestExchangePriceSourceCarriesLimitsForStock(t *testing.T) {
	source := ExchangePriceSource{Adapters: bookAdapters{bookAdapter{quote: execution.MarketQuote{
		Bid: shared.MustDecimal("9.99"), Ask: shared.MustDecimal("10"), Last: shared.MustDecimal("10"), SourceTime: stockNow,
		LimitUp: shared.MustDecimal("11"), LimitDown: shared.MustDecimal("9"), Halted: true,
	}}}}
	quote, err := source.LatestPrice(context.Background(), "stock-a", exchange.NewSymbolKey(exchange.MarketTypeStock, "600519.XSHG"))
	require.NoError(t, err)
	require.Equal(t, "10", quote.Price.String())
	require.Equal(t, "11", quote.LimitUp.String())
	require.Equal(t, "9", quote.LimitDown.String())
	require.True(t, quote.Halted)
	require.Equal(t, stockNow, quote.UpdatedAt)
}

// 高价股：一手 14.5 万元超过默认的单笔上限 10 万元，按一手下单，不能记成“单笔子单上限低于交易所最小数量”。
func TestStockHighPriceBuysOneLotAboveChildCap(t *testing.T) {
	fixture := newStockFixture(t)
	fixture.quotes["600519.XSHG"] = Quote{
		Price: shared.MustDecimal("1450"), UpdatedAt: stockNow, LimitUp: shared.MustDecimal("1595"), LimitDown: shared.MustDecimal("1305"),
		Bid: shared.MustDecimal("1449.9"), Ask: shared.MustDecimal("1450"),
	}
	fixture.target(t, store.InstrumentTarget{InstrumentID: "600519.XSHG", Quantity: "1000"})

	result, current := fixture.converge(t)

	require.Equal(t, StatusConverging, result.Status)
	require.Empty(t, current.BlockedTargets)
	require.Len(t, fixture.orders.specs, 1)
	require.Equal(t, "100", fixture.orders.specs[0].Quantity.String())
}

// 只剩顺延项的目标：有效期内 60 秒后再看一次（停牌复牌、涨跌停打开），不是 CONVERGED 之后就不管。
func TestStockDeferredTargetRetriesWithinValidity(t *testing.T) {
	fixture := newStockFixture(t)
	fixture.quotes["600519.XSHG"] = Quote{Price: shared.MustDecimal("10"), UpdatedAt: stockNow, Halted: true}
	fixture.target(t, store.InstrumentTarget{InstrumentID: "600519.XSHG", Quantity: "1000"})

	result, _ := fixture.converge(t)

	require.Equal(t, StatusConverged, result.Status)
	require.Equal(t, marketDeferralRetry, result.RetryAfter)
}

// 挂单冻结的部分不是 T+1：卖不出时照常记“可卖量不足”，不写成“当日买入，次日可卖”。
func TestStockLockedByOrdersIsNotT1(t *testing.T) {
	fixture := newStockFixture(t)
	fixture.holdingLocked(t, "600519.XSHG", "300", "0", "300")
	fixture.target(t, store.InstrumentTarget{InstrumentID: "600519.XSHG", Quantity: "0"})

	_, current := fixture.converge(t)

	require.Len(t, current.BlockedTargets, 1)
	require.False(t, current.BlockedTargets[0].Deferred)
	require.Contains(t, current.BlockedTargets[0].Reason, "可卖量不足")
}

// 时段外 TargetWorker 每秒都来：状态和原因没变时不再写库。
func TestStockOutOfSessionDoesNotRewritePendingTarget(t *testing.T) {
	fixture := newStockFixture(t)
	fixture.now = time.Date(2026, 11, 6, 4, 0, 0, 0, time.UTC) // 12:00 午休
	fixture.target(t, store.InstrumentTarget{InstrumentID: "600519.XSHG", Quantity: "1000"})
	_, first := fixture.converge(t)
	require.Equal(t, execution.ReasonOutOfSession, first.LastError)
	time.Sleep(1100 * time.Millisecond)
	_, second := fixture.converge(t)
	require.Equal(t, first.UpdatedAt, second.UpdatedAt)
}

// 休市时被模拟撮合撤掉、一股没成交的 IOC 不算一次伪市价尝试。
func TestPseudoMarketAttemptsSkipOutOfSessionCancel(t *testing.T) {
	children := []store.TargetChildOrder{
		{OrderType: "LIMIT", TimeInForce: "IOC", State: "CANCELED", Quantity: "100", FilledQuantity: "0", ExchangeOrderID: "x1"},
		{OrderType: "LIMIT", TimeInForce: "IOC", State: "CANCELED", Quantity: "100", FilledQuantity: "0", ExchangeOrderID: "x2", RejectReason: paper.OutOfSessionReason},
	}
	require.Equal(t, 1, pseudoMarketAttempts(children))
}
