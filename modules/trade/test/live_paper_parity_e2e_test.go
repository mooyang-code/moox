package test

import (
	"context"
	"testing"
	"time"

	equityapp "github.com/mooyang-code/moox/modules/trade/internal/application/equity"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	paperexec "github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
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
	setSpotSnapshot(t, live, store.TradingAccountSnapshot{
		Balances:          []store.AssetBalance{{Asset: "USDT", Available: "100000", Total: "100000"}},
		Equity:            "100000",
		AvailableFunds:    "100000",
		ExchangeUpdatedAt: testNow.UnixMilli(),
	})
	setFixtureLive(t, live)
	liveOrder := mustPlace(t, live, marketSpec("parity-live", exchange.SideBuy, "0.01"))
	_, err := live.orders.Submit(ctx, testSpace, string(liveOrder.ID))
	require.NoError(t, err)
	applyFakeFill(t, live, "parity-live", "parity-live-fill", "0.01", "50000", "0")
	liveFake.mu.Lock()
	liveFake.account = exchange.AccountSnapshot{
		Balances: []exchange.AssetBalance{
			{Asset: "USDT", Available: shared.MustDecimal("99500"), Total: shared.MustDecimal("99500")},
			{Asset: "BTC", Available: shared.MustDecimal("0.01"), Total: shared.MustDecimal("0.01")},
		},
		Equity: shared.MustDecimal("100000"), AvailableFunds: shared.MustDecimal("99500"),
		ExchangeUpdatedAt: testNow,
		Present:           exchange.AccountSnapshotPresence{Balances: true, Equity: true, AvailableFunds: true},
	}
	liveFake.mu.Unlock()
	_, err = live.sync.SyncAccount(ctx, testAccount)
	require.NoError(t, err)
	sampleEquity(t, live)

	paper := newProductionPaperFixture(t, exchange.MarketTypeSpot)
	paperOrder := mustPlace(t, paper, marketSpec("parity-paper", exchange.SideBuy, "0.01"))
	_, err = paper.orders.Submit(ctx, testSpace, string(paperOrder.ID))
	require.NoError(t, err)
	productionPaperMatch(t, paper)
	paperOrder, err = paper.orders.Get(ctx, testSpace, string(paperOrder.ID))
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

	paper := newProductionPaperFixture(t, exchange.MarketTypeSwap)
	paperOrder := mustPlace(t, paper, swapSpec("parity-swap-paper", exchange.SideBuy, "0.01", false))
	_, err = paper.orders.Submit(ctx, testSpace, string(paperOrder.ID))
	require.NoError(t, err)
	productionPaperMatch(t, paper)
	paperOrder, err = paper.orders.Get(ctx, testSpace, string(paperOrder.ID))
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

func newProductionPaperFixture(t *testing.T, market exchange.MarketType) *fixture {
	t.Helper()
	path := filepathForTest(t)
	tradeStore, err := store.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tradeStore.Close() })
	fake := newFakeExchange(market)
	seedFixture(t, tradeStore, market, fake.instrument)
	account, err := tradeStore.GetTradingAccountByID(context.Background(), testAccount)
	require.NoError(t, err)
	base := &paperexec.Adapter{Account: account, Store: tradeStore, MarketData: fake, Now: func() time.Time { return testNow }}
	_, err = base.LoadInstruments(context.Background())
	require.NoError(t, err)
	return buildFixture(tradeStore, path, fake, recordingAdapter{ExecutionAdapter: base, recorder: fake})
}

// productionPaperMatch runs the same persistent PaperAdapter -> Matcher ->
// Reducer path used by bootstrap. The decision uses the persisted MARKET
// execution price, so the test also guards the reservation/price contract.
func productionPaperMatch(t *testing.T, f *fixture) {
	t.Helper()
	matcher := &paperexec.Matcher{
		Store: f.store, Reducer: f.reducer,
		Refresh: func(ctx context.Context, accountID string) error {
			adapter := f.adapter.(recordingAdapter).ExecutionAdapter
			snapshot, err := adapter.(interface {
				GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error)
			}).GetAccountSnapshot(ctx)
			if err != nil {
				return err
			}
			account, err := f.store.GetTradingAccountByID(ctx, accountID)
			if err != nil {
				return err
			}
			at := snapshot.ExchangeUpdatedAt.UnixMilli()
			return f.store.Transaction(ctx, func(tx *store.Tx) error {
				return tx.UpdateTradingAccountFacts(
					account.SpaceID, accountID, account.FillCursors,
					paperSnapshotRecordForTest(snapshot), at, at,
				)
			})
		},
		DecideContext: func(ctx context.Context, candidate store.OrderRecord) (paperexec.Decision, error) {
			price := shared.Zero()
			if candidate.PaperExecutionPrice != nil {
				price = shared.MustDecimal(*candidate.PaperExecutionPrice)
			} else {
				price = shared.MustDecimal(candidate.ReferencePrice)
			}
			config, err := f.store.GetPaperAccountConfig(ctx, candidate.SpaceID, candidate.TradingAccountID)
			if err != nil {
				return paperexec.Decision{}, err
			}
			feeRate := shared.Zero()
			if config.TakerFeeRate != "" {
				feeRate = shared.MustDecimal(config.TakerFeeRate)
			}
			fee := price.Mul(shared.MustDecimal(candidate.Quantity)).Mul(feeRate)
			return paperexec.Decision{Fill: exchange.Fill{
				ExchangeTradeID: candidate.TradingAccountID + ":" + candidate.ClientOrderID,
				ExchangeOrderID: candidate.ExchangeOrderID, ClientOrderID: candidate.ClientOrderID,
				ExchangeSymbol: candidate.ExchangeSymbol, Symbol: candidate.ExchangeSymbol,
				Side: exchange.Side(candidate.Side), PositionSide: exchange.PositionSide(candidate.PositionSide),
				Quantity: shared.MustDecimal(candidate.Quantity), Price: price, Fee: fee,
				FeeAsset: candidate.ReservedAsset, SettlementAsset: candidate.ReservedAsset,
				LiquidityRole: "TAKER", TradedAt: testNow,
			}}, nil
		},
	}
	require.NoError(t, matcher.Scan(context.Background()))
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
	require.Equal(t, "100000", points[0].Equity)
	require.Equal(t, "99500", points[0].AvailableFunds)
	require.Equal(t, testNow.UnixMilli(), points[0].SourceTime)
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
	require.Equal(t, "50", positions[0].UsedMargin)
	require.Equal(t, "0", positions[0].UnrealizedPnL)
}

func sampleEquity(t *testing.T, f *fixture) {
	t.Helper()
	service := &equityapp.Service{
		Store: f.store, Adapters: adapterSource{adapter: f.adapter},
		Now: func() time.Time { return testNow }, SourceMaxAge: time.Minute,
	}
	require.NoError(t, service.SampleAccount(context.Background(), testAccount))
}

func setSpotSnapshot(t *testing.T, f *fixture, snapshot store.TradingAccountSnapshot) {
	t.Helper()
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpdateTradingAccountSnapshot(testSpace, testAccount, snapshot)
	}))
}

func paperSnapshotRecordForTest(snapshot exchange.AccountSnapshot) store.TradingAccountSnapshot {
	balances := make([]store.AssetBalance, 0, len(snapshot.Balances))
	for _, balance := range snapshot.Balances {
		balances = append(balances, store.AssetBalance{
			Asset: balance.Asset, Available: balance.Available.String(),
			Locked: balance.Locked.String(), Total: balance.Total.String(),
		})
	}
	return store.TradingAccountSnapshot{
		Balances: balances, Equity: snapshot.Equity.String(),
		AvailableFunds: snapshot.AvailableFunds.String(), UsedMargin: snapshot.UsedMargin.String(),
		MaintenanceMargin: snapshot.MaintenanceMargin.String(), UnrealizedPnL: snapshot.UnrealizedPnL.String(),
		ExchangeUpdatedAt: snapshot.ExchangeUpdatedAt.UnixMilli(),
	}
}
