package test

import (
	"context"
	"testing"
	"time"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/eventconsumer"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/stretchr/testify/require"
)

// 新目标到达时，旧目标挂着的市价单不撤，等它自己结束再按新目标收敛。
func TestNewTargetWaitsForOldTargetOpenOrderWithoutCancel(t *testing.T) {
	f := newFixture(t, exchange.MarketTypeSpot, newFakeExchange(exchange.MarketTypeSpot))
	seedLogicalAccount(t, f.store)
	seedTargetPaperHoldings(t, f)
	now := time.Now().UTC()
	f.fake.reference = exchange.ReferencePrice{Price: shared.MustDecimal("50000"), UpdatedAt: now}
	f.orders.Now = func() time.Time { return now }
	f.orders.Validator.Now = f.orders.Now
	opts := eventconsumer.TargetOptions{Store: f.store, Now: f.orders.Now, WeightResolver: testTargetWeightResolver{}}
	executor := &targetapp.Executor{Store: f.store, Orders: f.orders, Now: f.orders.Now,
		Prices: targetapp.ExchangePriceSource{Adapters: adapterSource{adapter: f.adapter}}}
	ctx := context.Background()
	accepted := eventconsumer.HandleTarget(ctx, targetDelivery(t, now, "old-target", 1, "0.03"), opts)
	require.Equal(t, jetstream.ACK, accepted.Decision, accepted.Err)
	result, err := executor.Converge(ctx, testSpace, testLogicalAccount)
	require.NoError(t, err)
	require.Equal(t, "place", result.Action)
	accepted = eventconsumer.HandleTarget(ctx, targetDelivery(t, now, "new-target", 2, "0.04"), opts)
	require.Equal(t, jetstream.ACK, accepted.Decision, accepted.Err)

	for range 3 {
		result, err = executor.Converge(ctx, testSpace, testLogicalAccount)
		require.NoError(t, err)
		require.Equal(t, targetapp.StatusConverging, result.Status)
	}
	f.fake.mu.Lock()
	defer f.fake.mu.Unlock()
	for _, order := range f.fake.orders {
		require.NotEqual(t, exchange.OrderStatusCanceled, order.Status, "旧目标的市价单不能被撤")
	}
	require.Equal(t, 1, f.fake.placeCalls, "旧单结束前不能为新目标下单")
}
