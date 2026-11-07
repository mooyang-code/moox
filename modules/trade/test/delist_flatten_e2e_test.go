package test

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/operator"
	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/eventconsumer"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/stretchr/testify/require"
)

// 下架清仓走真实的订单服务和模拟盘撮合：子单（OPERATOR 归属、只减仓、限价 + IOC）能通过校验、成交、按（市场、代码）找回；
// 清仓之后执行器不会按策略目标把仓位买回来（名单里的标的只减不增），组合也没有被暂停。
func TestDelistFlattenThroughTheRealOrderPath(t *testing.T) {
	ctx := context.Background()
	now := testNow
	f := newPaperFixture(t, exchange.MarketTypeSwap)
	seedSwapLogicalAccount(t, f.store)
	delivery := targetDeliveryForMarket(t, now, "target-delist", 1, "0.002", exchange.MarketTypeSwap)
	handled := eventconsumer.HandleTarget(ctx, delivery, eventconsumer.TargetOptions{
		Store: f.store, Now: func() time.Time { return now }, WeightResolver: testTargetWeightResolver{},
	})
	require.Equal(t, jetstream.ACK, handled.Decision, handled.Err)

	prices := logicalAccountE2EPriceSource{at: now}
	executor := &targetapp.Executor{Store: f.store, Orders: f.orders, Prices: prices, Now: func() time.Time { return now }}
	converged, err := executor.Converge(ctx, testSpace, testLogicalAccount)
	require.NoError(t, err)
	for attempts := 0; attempts < 10 && converged.Status != targetapp.StatusConverged; attempts++ {
		converged, err = executor.Converge(ctx, testSpace, testLogicalAccount)
		require.NoError(t, err)
	}
	require.Equal(t, targetapp.StatusConverged, converged.Status)
	f.fake.mu.Lock()
	opened := len(f.fake.requests)
	f.fake.mu.Unlock()
	require.Positive(t, opened)

	// 标的进入下架名单后下架清仓。
	_, err = f.store.BlockInstrument(ctx, blockedRecord("BINANCE", "SWAP", testSymbol))
	require.NoError(t, err)
	operatorService := &operator.Service{
		Store: f.store, Orders: f.orders, Syncer: syncBridge{service: f.sync}, Prices: prices,
		Now: func() time.Time { return now }, FlattenMaxAttempts: 2, FlattenRetryInterval: time.Millisecond,
	}
	result, err := operatorService.FlattenLogicalAccount(ctx, operator.FlattenCommand{
		SpaceID: testSpace, ActionID: "delist-e2e", LogicalAccountID: testLogicalAccount, Reason: "标的即将下架",
		InstrumentID: testInstrumentID(exchange.MarketTypeSwap), Exchange: "BINANCE",
	})
	require.NoError(t, err)
	require.Equal(t, "COMPLETED", result.Action.Status, "accounts=%+v", result.Accounts)
	require.Empty(t, result.Accounts[0].Remaining)

	f.fake.mu.Lock()
	closing := f.fake.requests[len(f.fake.requests)-1]
	f.fake.mu.Unlock()
	require.Equal(t, exchange.SideSell, closing.Side)
	require.Equal(t, exchange.OrderTypeLimit, closing.OrderType)
	require.Equal(t, exchange.FillPolicyIOC, closing.FillPolicy)
	require.True(t, closing.ReduceOnly)
	require.NotNil(t, closing.LimitPrice)

	// 子单按（市场、代码）找得回，归属是 OPERATOR。
	orders, err := f.store.ListOrdersForLane(ctx, testSpace, testAccount, exchange.NewSymbolKey(exchange.MarketTypeSwap, testSymbol))
	require.NoError(t, err)
	var child bool
	for _, order := range orders {
		if order.OwnerType == "OPERATOR" && order.OwnerID == "delist-e2e" {
			child = true
			require.Equal(t, "LIMIT", order.OrderType)
			require.Equal(t, "IOC", order.TimeInForce)
		}
	}
	require.True(t, child)

	// 不暂停组合；执行器不会按目标把仓位买回来。
	logical, err := f.store.GetLogicalAccount(ctx, testSpace, testLogicalAccount)
	require.NoError(t, err)
	require.Equal(t, "ACTIVE", logical.AutomationState)
	afterFlatten := len(f.fake.requests)
	for attempts := 0; attempts < 3; attempts++ {
		_, err = executor.Converge(ctx, testSpace, testLogicalAccount)
		require.NoError(t, err)
	}
	f.fake.mu.Lock()
	defer f.fake.mu.Unlock()
	require.Len(t, f.fake.requests, afterFlatten, "名单里的标的只减不增：清仓后不再买入")
}

func blockedRecord(exchangeName, market, symbol string) store.BlockedInstrumentRecord {
	return store.BlockedInstrumentRecord{Exchange: exchangeName, MarketType: market, ExchangeSymbol: symbol, Reason: "测试下架", BlockedAt: 1}
}
