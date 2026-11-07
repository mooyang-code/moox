package test

import (
	"context"
	"testing"
	"time"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/eventconsumer"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/stretchr/testify/require"
)

// defaultSettingsFixture 建模拟盘组合；下单设置是默认值（PSEUDO_MARKET、配置的最小下单额），不是 legacyOrderSettings。
func defaultSettingsFixture(t *testing.T, market exchange.MarketType) *fixture {
	t.Helper()
	f := newProductionPaperFixture(t, market)
	seedLogicalAccount(t, f.store)
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.SetLogicalAccountOrderSettings(testSpace, testLogicalAccount, "{}")
	}))
	return f
}

// underQuotedPrices 把执行器看到的价格压低，让伪市价的首单限价低于模拟盘的可成交价，必然零成交。
type underQuotedPrices struct {
	inner targetapp.ExchangePriceSource
	// discount 是压低的比例（0.05 表示压低 5%）。
	discount shared.Decimal
}

func (p underQuotedPrices) LatestPrice(ctx context.Context, account string, key exchange.SymbolKey) (targetapp.Quote, error) {
	quote, err := p.inner.LatestPrice(ctx, account, key)
	if err != nil {
		return quote, err
	}
	quote.Price = quote.Price.Mul(shared.MustDecimal("1").Sub(p.discount))
	return quote, nil
}

// 默认设置（伪市价单 + 配置的最小下单额）在真实的执行器、订单服务和模拟撮合里收敛到 CONVERGED，
// 包括首单限价压不到可成交价、零成交后按递增滑点重试的轮次。
func TestDefaultOrderSettingsConvergeThroughPseudoMarketRetries(t *testing.T) {
	for _, market := range []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap} {
		t.Run(string(market), func(t *testing.T) {
			ctx := context.Background()
			f := defaultSettingsFixture(t, market)
			executor := &targetapp.Executor{
				Store: f.store, Orders: f.orders, Now: func() time.Time { return testNow },
				Prices: underQuotedPrices{inner: targetapp.ExchangePriceSource{Adapters: adapterSource{adapter: f.adapter}}, discount: shared.MustDecimal("0.05")},
			}
			options := eventconsumer.TargetOptions{Store: f.store, Now: func() time.Time { return testNow }, WeightResolver: testTargetWeightResolver{}}
			accepted := eventconsumer.HandleTarget(ctx, targetDeliveryForMarket(t, testNow, "default-target", 1, "0.03", market), options)
			require.Equal(t, jetstream.ACK, accepted.Decision, accepted.Err)

			var attempts []int
			var final targetapp.Result
			for round := 0; round < 12; round++ {
				result, err := executor.Converge(ctx, testSpace, testLogicalAccount)
				require.NoError(t, err)
				final = result
				if result.Status == targetapp.StatusConverged {
					break
				}
				productionPaperMatch(t, f)
				orders, _, listErr := f.store.ListOrders(ctx, testSpace, store.OrderQuery{LogicalAccountID: testLogicalAccount, Limit: 100})
				require.NoError(t, listErr)
				attempts = attempts[:0]
				for _, order := range orders {
					require.Equal(t, "LIMIT", order.OrderType, "默认下单方式是伪市价单：限价 + IOC")
					require.Equal(t, "IOC", order.TimeInForce)
					attempts = append(attempts, order.Attempt)
				}
			}
			require.Equal(t, targetapp.StatusConverged, final.Status, "最终收敛：%+v", final)
			require.ElementsMatch(t, []int{0, 1, 2, 3}, attempts, "首单限价 -5%% × 1.015 压不到可成交价，滑点每次加一份，第 4 次（6%%）才成交")

			orders, _, err := f.store.ListOrders(ctx, testSpace, store.OrderQuery{LogicalAccountID: testLogicalAccount, Limit: 100})
			require.NoError(t, err)
			require.Len(t, orders, 4)
			filled := 0
			for _, order := range orders {
				if order.State == string(orderdomain.Filled) {
					filled++
				}
			}
			require.Equal(t, 1, filled, "前三次零成交结束，第四次成交")
		})
	}
}

// 系统单（BNB 补买、稳定币调平）挂在簿上时，目标子单照常提交：它不是外部单，不会触发“存在外部活动订单”而暂停组合。
func TestOpenSystemOrderDoesNotBlockTargetSubmissionOnTheRealOrderService(t *testing.T) {
	ctx := context.Background()
	f := defaultSettingsFixture(t, exchange.MarketTypeSpot)

	system := mustPlace(t, f, orderdomain.OrderSpec{
		ClientOrderSpec: orderdomain.ClientOrderSpec{
			TradingAccountID: testAccount, ClientOrderID: "sys-resting", InstrumentID: testInstrumentID(exchange.MarketTypeSpot),
			Type: exchange.OrderTypeLimit, FillPolicy: exchange.FillPolicyGTC, Side: exchange.SideBuy,
			Quantity: shared.MustDecimal("0.01"), LimitPrice: ptrDecimal("40000"),
		},
		ReferencePrice: shared.MustDecimal("50000"), ReferencePriceAt: testNow,
		Owner: orderdomain.OrderOwner{Type: orderdomain.OwnerSystem, OwnerID: "stable-rebalance", LogicalAccountID: testLogicalAccount},
	})
	_, err := f.orders.Submit(ctx, testSpace, string(system.ID))
	require.NoError(t, err)
	productionPaperMatch(t, f)
	system, err = f.orders.Get(ctx, testSpace, string(system.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Open, system.State, "系统单挂在簿上")

	executor := &targetapp.Executor{
		Store: f.store, Orders: f.orders, Now: func() time.Time { return testNow },
		Prices: targetapp.ExchangePriceSource{Adapters: adapterSource{adapter: f.adapter}},
	}
	options := eventconsumer.TargetOptions{Store: f.store, Now: func() time.Time { return testNow }, WeightResolver: testTargetWeightResolver{}}
	accepted := eventconsumer.HandleTarget(ctx, targetDelivery(t, testNow, "with-system-order", 1, "0.03"), options)
	require.Equal(t, jetstream.ACK, accepted.Decision, accepted.Err)
	result, err := executor.Converge(ctx, testSpace, testLogicalAccount)
	require.NoError(t, err)
	require.Equal(t, "place", result.Action, "目标子单照常下，没有因系统单暂停")
	productionPaperMatch(t, f)
	result, err = executor.Converge(ctx, testSpace, testLogicalAccount)
	require.NoError(t, err)
	require.Equal(t, targetapp.StatusConverged, result.Status)
	logical, err := f.store.GetLogicalAccount(ctx, testSpace, testLogicalAccount)
	require.NoError(t, err)
	require.Equal(t, "ACTIVE", logical.AutomationState)
	system, err = f.orders.Get(ctx, testSpace, string(system.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Open, system.State, "系统单没被当成目标子单撤掉")
}

// 系统单的成交不让资产变成受管：补买的 BNB、调平换来的稳定币不会被当成策略持仓卖掉。
func TestSystemOrderFillDoesNotMakeTheAssetManaged(t *testing.T) {
	ctx := context.Background()
	f := defaultSettingsFixture(t, exchange.MarketTypeSpot)
	system := mustPlace(t, f, orderdomain.OrderSpec{
		ClientOrderSpec: orderdomain.ClientOrderSpec{
			TradingAccountID: testAccount, ClientOrderID: "sys-fill", InstrumentID: testInstrumentID(exchange.MarketTypeSpot),
			Type: exchange.OrderTypeMarket, Side: exchange.SideBuy, Quantity: shared.MustDecimal("0.01"),
		},
		ReferencePrice: shared.MustDecimal("50000"), ReferencePriceAt: testNow,
		Owner: orderdomain.OrderOwner{Type: orderdomain.OwnerSystem, OwnerID: "bnb-reserve", LogicalAccountID: testLogicalAccount},
	})
	_, err := f.orders.Submit(ctx, testSpace, string(system.ID))
	require.NoError(t, err)
	productionPaperMatch(t, f)
	system, err = f.orders.Get(ctx, testSpace, string(system.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Filled, system.State)

	managed, err := f.store.ManagedAssets(ctx, testSpace, testAccount)
	require.NoError(t, err)
	require.NotContains(t, managed, "BTC", "系统单成交过的标的不让基础资产变成受管")
	require.Contains(t, managed, "USDT")
}

func ptrDecimal(raw string) *shared.Decimal {
	value := shared.MustDecimal(raw)
	return &value
}

// 默认设置里现货最小下单额是 10：低于它的目标不下单（写进 blocked_targets），高于它的照常下。
// 把交易所最小数量放小，让配置的门槛真正起作用（夹具里的最小数量 0.001 个 BTC 本身就是 50 USDT）。
func TestDefaultOrderSettingsEnforceTheConfiguredMinimumNotional(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		quantity string
		placed   bool
	}{{"0.0001", false}, {"0.0003", true}} { // 50000 × 0.0001 = 5 < 10；50000 × 0.0003 = 15 ≥ 10
		f := defaultSettingsFixture(t, exchange.MarketTypeSpot)
		instruments, err := f.store.ListInstrumentsForAccount(ctx, testAccount)
		require.NoError(t, err)
		require.NoError(t, f.store.Transaction(ctx, func(tx *store.Tx) error {
			for _, instrument := range instruments {
				instrument.ExchangeQuantityStep, instrument.MinExchangeQuantity, instrument.MinNotional = "0.00001", "0.00001", "1"
				if err := tx.UpsertInstrument(instrument); err != nil {
					return err
				}
			}
			return nil
		}))
		executor := &targetapp.Executor{
			Store: f.store, Orders: f.orders, Now: func() time.Time { return testNow },
			Prices: targetapp.ExchangePriceSource{Adapters: adapterSource{adapter: f.adapter}},
		}
		options := eventconsumer.TargetOptions{Store: f.store, Now: func() time.Time { return testNow }, WeightResolver: testTargetWeightResolver{}}
		accepted := eventconsumer.HandleTarget(ctx, targetDelivery(t, testNow, "min-notional", 1, c.quantity), options)
		require.Equal(t, jetstream.ACK, accepted.Decision, accepted.Err)
		result, err := executor.Converge(ctx, testSpace, testLogicalAccount)
		require.NoError(t, err)
		orders, _, err := f.store.ListOrders(ctx, testSpace, store.OrderQuery{LogicalAccountID: testLogicalAccount, Limit: 10})
		require.NoError(t, err)
		if c.placed {
			require.Len(t, orders, 1, "数量 %s", c.quantity)
			continue
		}
		require.Empty(t, orders, "数量 %s：金额 5 低于默认最小下单额 10", c.quantity)
		require.Equal(t, targetapp.StatusBlocked, result.Status)
		target, err := f.store.GetLogicalAccountTarget(ctx, testSpace, testLogicalAccount)
		require.NoError(t, err)
		require.Contains(t, target.BlockedTargets[0].Reason, "最小下单额")
	}
}
