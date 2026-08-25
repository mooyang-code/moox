package test

import (
	"context"
	"testing"
	"time"

	equityapp "github.com/mooyang-code/moox/modules/trade/internal/application/equity"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// TestLiveAndPaperProduceEquivalentSpotFacts drives both modes through the
// same OrderService and reducer boundary. The fake exchange supplies the Live
// transport; the paper fixture uses the production SQLite-backed adapter.
func TestLiveAndPaperProduceEquivalentSpotFacts(t *testing.T) {
	ctx := context.Background()

	liveFake := newFakeExchange(exchange.MarketTypeSpot)
	live := newFixture(t, exchange.MarketTypeSpot, liveFake)
	setFixtureLive(t, live)
	liveOrder := mustPlace(t, live, marketSpec("parity-live", exchange.SideBuy, "0.01"))
	_, err := live.orders.Submit(ctx, testSpace, string(liveOrder.ID))
	require.NoError(t, err)
	applyFakeFill(t, live, "parity-live", "parity-live-fill", "0.01", "50000", "0")
	sampleEquity(t, live)

	paper := newPaperFixture(t, exchange.MarketTypeSpot)
	paperOrder := mustPlace(t, paper, marketSpec("parity-paper", exchange.SideBuy, "0.01"))
	paperOrder, err = paper.orders.Submit(ctx, testSpace, string(paperOrder.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Filled, paperOrder.State)
	sampleEquity(t, paper)

	assertCommonSpotFacts(t, live, string(liveOrder.ID))
	assertCommonSpotFacts(t, paper, string(paperOrder.ID))
}

// Swap follows the same order/fill boundary but asserts the position projection
// instead of the Spot holding projection.
func TestLiveAndPaperProduceEquivalentSwapFacts(t *testing.T) {
	ctx := context.Background()

	liveFake := newFakeExchange(exchange.MarketTypeSwap)
	live := newFixture(t, exchange.MarketTypeSwap, liveFake)
	setFixtureLive(t, live)
	liveOrder := mustPlace(t, live, swapSpec("parity-swap-live", exchange.SideBuy, "0.01", false))
	_, err := live.orders.Submit(ctx, testSpace, string(liveOrder.ID))
	require.NoError(t, err)
	applyFakeFill(t, live, "parity-swap-live", "parity-swap-live-fill", "0.01", "50000", "0")

	paper := newPaperFixture(t, exchange.MarketTypeSwap)
	paperOrder := mustPlace(t, paper, swapSpec("parity-swap-paper", exchange.SideBuy, "0.01", false))
	paperOrder, err = paper.orders.Submit(ctx, testSpace, string(paperOrder.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Filled, paperOrder.State)

	assertCommonSwapFacts(t, live, string(liveOrder.ID))
	assertCommonSwapFacts(t, paper, string(paperOrder.ID))
}

func setFixtureLive(t *testing.T, f *fixture) {
	t.Helper()
	err := f.store.DBForTest().Exec(`
		UPDATE t_trading_accounts
		SET c_execution_mode = 'LIVE', c_live_environment = 'TESTNET',
		    c_credential_secret_id = 'e2e-secret'
		WHERE c_space_id = ? AND c_trading_account_id = ?
	`, testSpace, testAccount).Error
	require.NoError(t, err)
}

func assertCommonSpotFacts(t *testing.T, f *fixture, orderID string) {
	t.Helper()
	ctx := context.Background()
	orders, total, err := f.store.ListOrders(ctx, testSpace, store.OrderQuery{
		TradingAccountID: testAccount, Limit: 10,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, orders, 1)
	require.Equal(t, "FILLED", orders[0].State)
	require.Equal(t, "BINANCE", orders[0].Exchange)
	require.Equal(t, "SPOT", orders[0].MarketType)
	require.Equal(t, "BTCUSDT", orders[0].ExchangeSymbol)
	require.Equal(t, "0.01", orders[0].FilledQuantity)
	require.Equal(t, orderID, orders[0].OrderID)

	fills, fillTotal, err := f.store.ListFills(ctx, testSpace, store.FillQuery{
		TradingAccountID: testAccount, Limit: 10,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), fillTotal)
	require.Len(t, fills, 1)
	require.Equal(t, "BTCUSDT", fills[0].ExchangeSymbol)
	require.Equal(t, "BUY", fills[0].Side)
	require.Equal(t, "0.01", fills[0].Quantity)
	require.Equal(t, "50000", fills[0].Price)
	points, err := f.store.ListAccountEquityPoints(ctx, testSpace, testAccount, 0, 0)
	require.NoError(t, err)
	require.Len(t, points, 1)
}

func assertCommonSwapFacts(t *testing.T, f *fixture, orderID string) {
	t.Helper()
	ctx := context.Background()
	orders, total, err := f.store.ListOrders(ctx, testSpace, store.OrderQuery{TradingAccountID: testAccount, Limit: 10})
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, orders, 1)
	require.Equal(t, orderID, orders[0].OrderID)
	require.Equal(t, "FILLED", orders[0].State)
	require.Equal(t, "SWAP", orders[0].MarketType)

	fills, fillTotal, err := f.store.ListFills(ctx, testSpace, store.FillQuery{TradingAccountID: testAccount, Limit: 10})
	require.NoError(t, err)
	require.Equal(t, int64(1), fillTotal)
	require.Len(t, fills, 1)
	require.Equal(t, "SWAP", fills[0].MarketType)
	require.Equal(t, "0.01", fills[0].Quantity)

	positions, err := f.store.ListPositions(ctx, testSpace, testAccount, testSymbol)
	require.NoError(t, err)
	require.Len(t, positions, 1)
	require.Equal(t, "0.01", positions[0].SignedQuantity)
}

func sampleEquity(t *testing.T, f *fixture) {
	t.Helper()
	service := &equityapp.Service{
		Store: f.store, Adapters: adapterSource{adapter: f.adapter},
		Now: func() time.Time { return testNow }, SourceMaxAge: time.Minute,
	}
	require.NoError(t, service.SampleAccount(context.Background(), testAccount))
}
