package fundmaint

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

var testNow = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

// fakeAdapter 是同时支持 BNB 抵扣开关和小额兑换的币安适配器替身。
type fakeAdapter struct {
	execution.ExecutionAdapter
	clock    func() time.Time
	prices   map[string]string // 交易所代码 → 参考价
	bids     map[string]string
	discount bool
	getErr   error
	setCalls []bool
	dust     []exchange.DustAsset
	converts [][]string
}

func (a *fakeAdapter) GetReferencePrice(_ context.Context, key exchange.SymbolKey) (exchange.ReferencePrice, error) {
	price, ok := a.prices[key.Symbol]
	if !ok {
		return exchange.ReferencePrice{}, errors.New("no price")
	}
	return exchange.ReferencePrice{Price: shared.MustDecimal(price), UpdatedAt: a.clock()}, nil
}

func (a *fakeAdapter) GetQuote(_ context.Context, key exchange.SymbolKey) (execution.MarketQuote, error) {
	bid, ok := a.bids[key.Symbol]
	if !ok {
		bid = a.prices[key.Symbol]
	}
	return execution.MarketQuote{Bid: shared.MustDecimal(bid), Ask: shared.MustDecimal(bid), Last: shared.MustDecimal(bid), SourceTime: a.clock()}, nil
}

func (a *fakeAdapter) GetBNBFeeDiscount(context.Context) (bool, error) { return a.discount, a.getErr }
func (a *fakeAdapter) SetBNBFeeDiscount(_ context.Context, enabled bool) error {
	a.setCalls = append(a.setCalls, enabled)
	a.discount = enabled
	return nil
}
func (a *fakeAdapter) ListDust(context.Context) ([]exchange.DustAsset, error) { return a.dust, nil }
func (a *fakeAdapter) ConvertDust(_ context.Context, assets []string) (exchange.DustConversion, error) {
	a.converts = append(a.converts, assets)
	return exchange.DustConversion{Converted: assets, BNBReceived: shared.MustDecimal("0.01")}, nil
}

type adapters struct{ adapter *fakeAdapter }

func (a adapters) Adapter(string) (execution.ExecutionAdapter, error) { return a.adapter, nil }

// fakeOrders 记录下的系统单；Cancel 把单标成已撤。
type fakeOrders struct {
	specs     []orderdomain.OrderSpec
	submitted []string
	canceled  []string
	placeErr  error
}

func (o *fakeOrders) Place(_ context.Context, _ string, spec orderdomain.OrderSpec) (orderdomain.Order, error) {
	if o.placeErr != nil {
		return orderdomain.Order{}, o.placeErr
	}
	o.specs = append(o.specs, spec)
	return orderdomain.Order{ID: shared.OrderID("order-" + spec.ClientOrderID), Spec: spec}, nil
}
func (o *fakeOrders) Submit(_ context.Context, _ string, orderID string) (orderdomain.Order, error) {
	o.submitted = append(o.submitted, orderID)
	return orderdomain.Order{ID: shared.OrderID(orderID)}, nil
}
func (o *fakeOrders) Cancel(_ context.Context, _ string, orderID string) (orderdomain.Order, error) {
	o.canceled = append(o.canceled, orderID)
	return orderdomain.Order{ID: shared.OrderID(orderID)}, nil
}

type fixture struct {
	now     time.Time
	t       *testing.T
	store   *store.Store
	adapter *fakeAdapter
	orders  *fakeOrders
	service *Service
}

// newFixture 建一个币安统一账户（现货），USDT 结算；balances 是账户快照里的余额。
func newFixture(t *testing.T, fundSettings string, balances []store.AssetBalance) *fixture {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tradeStore.Close()) })
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "account-a", Name: "a", Exchange: "BINANCE", Markets: []string{"SPOT"},
			ExecutionMode: "LIVE", AccountMode: "BINANCE_PORTFOLIO_MARGIN", Environment: "PRODUCTION", CredentialSecretID: "secret-1",
			SettlementAsset: "USDT", Status: "ENABLED", Ready: true,
			Snapshot:   store.TradingAccountSnapshot{Balances: balances, ExchangeUpdatedAt: testNow.UnixMilli()},
			LastSyncAt: testNow.UnixMilli(), SnapshotSourceTime: testNow.UnixMilli(),
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical", ExecutionMode: "LIVE", SettlementAsset: "USDT",
			OwnerInstanceID: "i1", OwnerSessionID: "s1", FundSettingsJSON: fundSettings, AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-a", Enabled: true, Priority: 1,
		}); err != nil {
			return err
		}
		for _, base := range []string{"BNB", "BTC", "USDC", "FDUSD", "DOGE"} {
			if err := tx.UpsertInstrument(store.InstrumentRecord{
				Exchange: "BINANCE", Environment: "PRODUCTION", MarketType: "SPOT", ExchangeSymbol: base + "USDT", InstrumentID: base + "-USDT-SPOT",
				BaseAsset: base, QuoteAsset: "USDT", SettlementAsset: "USDT", ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001",
				MinNotional: "5", PriceTick: "0.0001", Status: "TRADING",
			}); err != nil {
				return err
			}
		}
		return tx.SetLogicalAccountAutomation("space-1", "logical-1", "ACTIVE", "")
	}))
	adapter := &fakeAdapter{
		prices: map[string]string{"BNBUSDT": "600", "BTCUSDT": "50000", "USDCUSDT": "1", "FDUSDUSDT": "1"},
		bids:   map[string]string{"USDCUSDT": "0.9995", "FDUSDUSDT": "0.9995"},
	}
	orders := &fakeOrders{}
	f := &fixture{t: t, now: testNow, store: tradeStore, adapter: adapter, orders: orders}
	adapter.clock = func() time.Time { return f.now }
	f.service = &Service{Store: tradeStore, Adapters: adapters{adapter}, Orders: orders, Now: func() time.Time { return f.now }}
	return f
}

func (f *fixture) run() error {
	logical, err := f.store.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(f.t, err)
	return f.service.RunLogicalAccount(context.Background(), logical)
}

func balance(asset, total string) store.AssetBalance {
	return store.AssetBalance{Wallet: "MARGIN", Asset: asset, Available: total, Total: total}
}

// ---- T23：BNB 抵扣开关与保留补买 ----

func TestBNBDiscountIsEnabledOnceAndRevalidatedAfterSettingsChange(t *testing.T) {
	f := newFixture(t, `{"bnb_discount":true}`, []store.AssetBalance{balance("USDT", "1000")})
	require.NoError(t, f.run())
	require.Equal(t, []bool{true}, f.adapter.setCalls)
	require.NoError(t, f.run())
	require.Len(t, f.adapter.setCalls, 1, "已确认开启，不重复设置")

	// 设置变更后重新确认交易所上的开关：交易所上已经是开着的就不再设。
	f.service.Invalidate(context.Background(), "space-1", "logical-1")
	require.NoError(t, f.run())
	require.Len(t, f.adapter.setCalls, 1)
	f.adapter.discount = false
	f.service.Invalidate(context.Background(), "space-1", "logical-1")
	require.NoError(t, f.run())
	require.Equal(t, []bool{true, true}, f.adapter.setCalls)
}

func TestBNBDiscountNeverTurnsTheExchangeSwitchOff(t *testing.T) {
	f := newFixture(t, `{"bnb_reserve_amount":"200"}`, []store.AssetBalance{balance("USDT", "1000"), balance("BNB", "1")})
	f.adapter.discount = true
	require.NoError(t, f.run())
	require.Empty(t, f.adapter.setCalls, "设置里没开 BNB 抵扣就不碰交易所的开关")
}

func TestBNBDiscountFailureIsReportedAndRetried(t *testing.T) {
	f := newFixture(t, `{"bnb_discount":true}`, []store.AssetBalance{balance("USDT", "1000")})
	f.adapter.getErr = errors.New("交易所暂时不可用")
	require.Error(t, f.run())
	f.adapter.getErr = nil
	require.NoError(t, f.run())
	require.Equal(t, []bool{true}, f.adapter.setCalls)
}

func TestBNBReserveBuysTheShortfallAsASystemOrder(t *testing.T) {
	// 保留 200，现有 0.1 个 BNB = 60，缺口 140 → 买 0.233 个（步长 0.001）。
	f := newFixture(t, `{"bnb_reserve_amount":"200"}`, []store.AssetBalance{balance("USDT", "1000"), balance("BNB", "0.1")})
	require.NoError(t, f.run())
	require.Len(t, f.orders.specs, 1)
	spec := f.orders.specs[0]
	require.Equal(t, orderdomain.OwnerSystem, spec.Owner.Type)
	require.Equal(t, "logical-1", spec.Owner.LogicalAccountID)
	require.Nil(t, spec.Owner.RunnerID)
	require.Equal(t, "BNB-USDT-SPOT", spec.InstrumentID)
	require.Equal(t, exchange.SideBuy, spec.Side)
	require.Equal(t, exchange.OrderTypeMarket, spec.Type)
	require.Equal(t, "0.233", spec.Quantity.String())
	require.Len(t, f.orders.submitted, 1)
}

func TestBNBReserveThresholdIsReserveMinusTen(t *testing.T) {
	// 保留 200：市值 190 恰好等于阈值，不补买；市值 189.6（0.316 个）低于阈值，补买。
	enough := newFixture(t, `{"bnb_reserve_amount":"200"}`, []store.AssetBalance{balance("USDT", "1000"), {Wallet: "MARGIN", Asset: "BNB", Available: "0.3166666666", Total: "0.31666667"}})
	require.NoError(t, enough.run())
	require.Empty(t, enough.orders.specs)
	short := newFixture(t, `{"bnb_reserve_amount":"200"}`, []store.AssetBalance{balance("USDT", "1000"), balance("BNB", "0.316")})
	require.NoError(t, short.run())
	require.Len(t, short.orders.specs, 1)
}

func TestBNBReserveCountsAllWalletsAndWaitsForTheOpenOrder(t *testing.T) {
	// 现货、合约钱包里的 BNB 都算：0.2 + 0.2 = 0.4 个 = 240，高于 200，不补买。
	f := newFixture(t, `{"bnb_reserve_amount":"200"}`, []store.AssetBalance{
		balance("USDT", "1000"), {Wallet: "SPOT", Asset: "BNB", Available: "0.2", Total: "0.2"}, {Wallet: "FUTURES", Asset: "BNB", Available: "0.2", Total: "0.2"},
	})
	require.NoError(t, f.run())
	require.Empty(t, f.orders.specs)

	// 上一次的补买单还没结束：不再重复下。
	g := newFixture(t, `{"bnb_reserve_amount":"200"}`, []store.AssetBalance{balance("USDT", "1000")})
	require.NoError(t, g.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateOrder(store.OrderRecord{
			SpaceID: "space-1", OrderID: "open-bnb", TradingAccountID: "account-a", LogicalAccountID: "logical-1", ClientOrderID: "open-bnb",
			Exchange: "BINANCE", MarketType: "SPOT", InstrumentID: "BNB-USDT-SPOT", ExchangeSymbol: "BNBUSDT",
			OrderType: "MARKET", Side: "BUY", Quantity: "0.3", ReferencePrice: "600", OwnerType: "SYSTEM", OwnerID: "bnb-reserve",
			State: "SUBMITTING", SubmittedAt: 1_000, Version: 1,
		})
	}))
	require.NoError(t, g.run())
	require.Empty(t, g.orders.specs)
}

func TestSystemTasksSkipPausedAndFlatteningAccounts(t *testing.T) {
	f := newFixture(t, `{"bnb_reserve_amount":"200"}`, []store.AssetBalance{balance("USDT", "1000")})
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.SetLogicalAccountAutomation("space-1", "logical-1", "PAUSED", "手动暂停")
	}))
	require.NoError(t, f.run())
	require.Empty(t, f.orders.specs, "暂停的组合不补买")

	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.SetLogicalAccountAutomation("space-1", "logical-1", "ACTIVE", ""); err != nil {
			return err
		}
		_, err := tx.CreateOperatorAction(store.OperatorActionRecord{
			SpaceID: "space-1", ActionID: "action-1", LogicalAccountID: "logical-1", ActionType: "REDUCE", Reason: "保证金率",
			RequestJSON: "{}", Status: "RUNNING",
		})
		return err
	}))
	require.NoError(t, f.run())
	require.Empty(t, f.orders.specs, "正在减仓的组合不补买")
}

func TestReserveWithoutSpotMarketWarnsAndSkips(t *testing.T) {
	f := newFixture(t, `{"bnb_reserve_amount":"200"}`, []store.AssetBalance{balance("USDT", "1000")})
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_trading_accounts SET c_markets = 'SWAP'").Error)
	require.NoError(t, f.run())
	require.Empty(t, f.orders.specs)
}

// ---- T24：小额兑 BNB ----

func dustFixture(t *testing.T, settings string, dust []exchange.DustAsset) *fixture {
	f := newFixture(t, settings, []store.AssetBalance{balance("USDT", "1000")})
	f.adapter.dust = dust
	return f
}

func TestDustConvertsOnlyNonTargetNonExcludedBelowFiveUSDT(t *testing.T) {
	btcValue := func(usdt string) shared.Decimal { return shared.MustDecimal(usdt).Div(shared.MustDecimal("50000")) }
	f := dustFixture(t, `{"dust_convert_enabled":true,"margin_coins":{"ETH":"100"},"stablecoin_margin":{"USDT":"0.5","USDC":"0.5"}}`, []exchange.DustAsset{
		{Asset: "DOGE", Quantity: shared.MustDecimal("10"), ValueBTC: btcValue("2")},     // 低于 5U，非目标：兑
		{Asset: "SHIB", Quantity: shared.MustDecimal("10"), ValueBTC: btcValue("4.99")},  // 兑
		{Asset: "LTC", Quantity: shared.MustDecimal("1"), ValueBTC: btcValue("5")},       // 恰好 5U：不兑
		{Asset: "XRP", Quantity: shared.MustDecimal("1"), ValueBTC: btcValue("30")},      // 太大：不兑
		{Asset: "BTC", Quantity: shared.MustDecimal("0.00001"), ValueBTC: btcValue("1")}, // 在目标里：不兑
		{Asset: "ETH", Quantity: shared.MustDecimal("0.001"), ValueBTC: btcValue("2")},   // 保证金币（排除）：不兑
		{Asset: "USDC", Quantity: shared.MustDecimal("2"), ValueBTC: btcValue("2")},      // 稳定币保证金（排除）：不兑
		{Asset: "BNB", Quantity: shared.MustDecimal("0.001"), ValueBTC: btcValue("1")},   // BNB 自己：不兑
	})
	_, accepted, err := f.store.AcceptLogicalAccountTarget(context.Background(), store.LogicalAccountTargetRecord{
		SpaceID: "space-1", LogicalAccountID: "logical-1", TargetID: "t1", InstanceID: "i1", SessionID: "s1", StrategyID: "st",
		BarEndTime: 2_000, EffectiveAt: 2_000, ValidUntil: time.Now().Add(time.Hour).UnixMilli(),
		Targets: []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SPOT", Quantity: "1"}}, Status: "PENDING", AcceptedAt: 2_000,
	})
	require.NoError(t, err)
	require.True(t, accepted)

	require.NoError(t, f.run())
	require.Equal(t, [][]string{{"DOGE", "SHIB"}}, f.adapter.converts)
	account, err := f.store.GetTradingAccountByID(context.Background(), "account-a")
	require.NoError(t, err)
	require.Equal(t, testNow.UnixMilli(), account.LastDustConvertAt, "兑换时间记在账户的 c_last_dust_convert_at 列")
}

func TestDustConvertWaitsForTheConfiguredInterval(t *testing.T) {
	f := dustFixture(t, `{"dust_convert_enabled":true,"dust_convert_interval_days":7}`, []exchange.DustAsset{
		{Asset: "DOGE", Quantity: shared.MustDecimal("10"), ValueBTC: shared.MustDecimal("0.00001")},
	})
	require.NoError(t, f.run())
	require.Len(t, f.adapter.converts, 1)
	f.now = testNow.Add(6 * 24 * time.Hour)
	require.NoError(t, f.run())
	require.Len(t, f.adapter.converts, 1, "没到 7 天")
	f.now = testNow.Add(7*24*time.Hour + time.Minute)
	require.NoError(t, f.run())
	require.Len(t, f.adapter.converts, 2)
}

func TestDustCheckWithNothingToConvertStillCountsAsACheck(t *testing.T) {
	f := dustFixture(t, `{"dust_convert_enabled":true}`, nil)
	require.NoError(t, f.run())
	require.Empty(t, f.adapter.converts)
	account, err := f.store.GetTradingAccountByID(context.Background(), "account-a")
	require.NoError(t, err)
	require.NotZero(t, account.LastDustConvertAt)
}

// ---- T25：多稳定币调平 ----

const stableSettings = `{"stablecoin_margin":{"USDT":"0.6","USDC":"0.4"}}`

func TestStablecoinWithinFivePointsIsLeftAlone(t *testing.T) {
	// 实际比例 USDT 64%、USDC 36%：都在目标的 5 个百分点以内，不动。
	f := newFixture(t, stableSettings, []store.AssetBalance{balance("USDT", "640"), balance("USDC", "360")})
	require.NoError(t, f.run())
	require.Empty(t, f.orders.specs)
	// 恰好 5 个百分点（65% / 35%）也不动：超过才调。
	g := newFixture(t, stableSettings, []store.AssetBalance{balance("USDT", "650"), balance("USDC", "350")})
	require.NoError(t, g.run())
	require.Empty(t, g.orders.specs)
}

func TestStablecoinBuysTheShortfallWithABidLimitOrder(t *testing.T) {
	// USDT 800、USDC 200：USDC 目标 40% = 400，缺 200 → 买 200 个（价格 1，步长 0.001），挂买一价 0.9995。
	f := newFixture(t, stableSettings, []store.AssetBalance{balance("USDT", "800"), balance("USDC", "200")})
	require.NoError(t, f.run())
	require.Len(t, f.orders.specs, 1)
	spec := f.orders.specs[0]
	require.Equal(t, orderdomain.OwnerSystem, spec.Owner.Type)
	require.Equal(t, "stable-rebalance", spec.Owner.OwnerID)
	require.Equal(t, "USDC-USDT-SPOT", spec.InstrumentID)
	require.Equal(t, exchange.SideBuy, spec.Side)
	require.Equal(t, exchange.OrderTypeLimit, spec.Type)
	require.Equal(t, exchange.FillPolicyGTC, spec.FillPolicy)
	require.Equal(t, "0.9995", spec.LimitPrice.String())
	require.Equal(t, "200", spec.Quantity.String())
}

func TestStablecoinSellsTheExcessWithAnIOCOrder(t *testing.T) {
	// USDT 400、USDC 600：USDC 目标 40% = 400，多 200 → 卖 200 个，IOC。
	f := newFixture(t, stableSettings, []store.AssetBalance{balance("USDT", "400"), balance("USDC", "600")})
	require.NoError(t, f.run())
	require.Len(t, f.orders.specs, 1)
	require.Equal(t, exchange.SideSell, f.orders.specs[0].Side)
	require.Equal(t, exchange.FillPolicyIOC, f.orders.specs[0].FillPolicy)
	require.Equal(t, "200", f.orders.specs[0].Quantity.String())
}

func TestStablecoinSellsBeforeItBuys(t *testing.T) {
	// 三种稳定币：USDT 目标 40%、USDC 30%、FDUSD 30%。USDC 持有过多、FDUSD 不足。
	f := newFixture(t, `{"stablecoin_margin":{"USDT":"0.4","USDC":"0.3","FDUSD":"0.3"}}`, []store.AssetBalance{balance("USDT", "400"), balance("USDC", "500"), balance("FDUSD", "100")})
	require.NoError(t, f.run())
	require.Len(t, f.orders.specs, 2)
	require.Equal(t, "USDC-USDT-SPOT", f.orders.specs[0].InstrumentID)
	require.Equal(t, exchange.SideSell, f.orders.specs[0].Side, "先卖多出来的")
	require.Equal(t, "FDUSD-USDT-SPOT", f.orders.specs[1].InstrumentID)
	require.Equal(t, exchange.SideBuy, f.orders.specs[1].Side)
}

func TestStablecoinCancelsLastRoundsUnfilledBuyBeforeRecalculating(t *testing.T) {
	f := newFixture(t, stableSettings, []store.AssetBalance{balance("USDT", "800"), balance("USDC", "200")})
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateOrder(store.OrderRecord{
			SpaceID: "space-1", OrderID: "rest-1", TradingAccountID: "account-a", LogicalAccountID: "logical-1", ClientOrderID: "rest-1",
			Exchange: "BINANCE", MarketType: "SPOT", InstrumentID: "USDC-USDT-SPOT", ExchangeSymbol: "USDCUSDT",
			OrderType: "LIMIT", TimeInForce: "GTC", Side: "BUY", Quantity: "200", LimitPrice: strPtr("0.9995"), ReferencePrice: "1",
			OwnerType: "SYSTEM", OwnerID: "stable-rebalance", State: "OPEN", SubmittedAt: 1_000, Version: 1,
		})
	}))
	require.NoError(t, f.run())
	require.Equal(t, []string{"rest-1"}, f.orders.canceled, "到下一次检查还没成交的买单撤掉")
	require.Empty(t, f.orders.specs, "撤单结束前不下新单")
}

// 更早的唤醒（设置变更、会话就绪）不撤刚挂不久的买单，也不下新单，等下一次定时检查。
func TestStablecoinKeepsARecentBuyUntilTheNextScheduledCheck(t *testing.T) {
	f := newFixture(t, stableSettings, []store.AssetBalance{balance("USDT", "800"), balance("USDC", "200")})
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateOrder(store.OrderRecord{
			SpaceID: "space-1", OrderID: "rest-1", TradingAccountID: "account-a", LogicalAccountID: "logical-1", ClientOrderID: "rest-1",
			Exchange: "BINANCE", MarketType: "SPOT", InstrumentID: "USDC-USDT-SPOT", ExchangeSymbol: "USDCUSDT",
			OrderType: "LIMIT", TimeInForce: "GTC", Side: "BUY", Quantity: "200", LimitPrice: strPtr("0.9995"), ReferencePrice: "1",
			OwnerType: "SYSTEM", OwnerID: "stable-rebalance", State: "OPEN", SubmittedAt: testNow.Add(-10 * time.Minute).UnixMilli(), Version: 1,
		})
	}))
	require.NoError(t, f.run())
	require.Empty(t, f.orders.canceled, "挂了 10 分钟，不撤")
	require.Empty(t, f.orders.specs)
	f.now = testNow.Add(46 * time.Minute) // 已经挂了 56 分钟
	require.NoError(t, f.run())
	require.Equal(t, []string{"rest-1"}, f.orders.canceled)
}

func TestStablecoinRebalanceIsUnifiedAccountOnly(t *testing.T) {
	f := newFixture(t, stableSettings, []store.AssetBalance{balance("USDT", "800"), balance("USDC", "200")})
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_trading_accounts SET c_account_mode = 'BINANCE_STANDARD'").Error)
	require.NoError(t, f.run())
	require.Empty(t, f.orders.specs)
}

func TestStablecoinWithoutAPairSkipsTheWholeRound(t *testing.T) {
	f := newFixture(t, `{"stablecoin_margin":{"USDT":"0.5","TUSD":"0.5"}}`, []store.AssetBalance{balance("USDT", "800"), balance("TUSD", "200")})
	require.NoError(t, f.run())
	require.Empty(t, f.orders.specs)
}

func strPtr(value string) *string { return &value }
