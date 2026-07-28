package accountsync

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

type syncAdapterSource struct {
	adapter exchange.Adapter
}

type readySessionState bool

func (s readySessionState) Ready(string) bool { return bool(s) }

func (s syncAdapterSource) Adapter(string) (exchange.Adapter, error) {
	return s.adapter, nil
}

type syncAdapter struct {
	order       exchange.Order
	fill        exchange.Fill
	fillStarted chan struct{}
	fillRelease chan struct{}
	lookupErr   error
}

func (*syncAdapter) Exchange() exchange.Exchange { return exchange.ExchangeBinance }
func (*syncAdapter) LoadInstruments(context.Context) ([]exchange.Instrument, error) {
	return nil, nil
}
func (*syncAdapter) GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error) {
	return exchange.AccountSnapshot{
		AvailableFunds:    shared.MustDecimal("900"),
		Equity:            shared.MustDecimal("1000"),
		ExchangeUpdatedAt: time.UnixMilli(2_000),
	}, nil
}
func (*syncAdapter) ListPositionSnapshots(context.Context) ([]exchange.Position, error) {
	return []exchange.Position{{
		Symbol: "BTC-USDT", PositionSide: exchange.PositionSideNet,
		SignedQuantity: shared.MustDecimal("0.5"),
		EntryPrice:     shared.MustDecimal("100"), MarkPrice: shared.MustDecimal("101"),
		Leverage: shared.MustDecimal("5"), MarginMode: exchange.MarginModeCross,
		ExchangeUpdatedAt: time.UnixMilli(2_000),
	}}, nil
}
func (a *syncAdapter) ListOpenOrders(context.Context) ([]exchange.Order, error) {
	return []exchange.Order{a.order}, nil
}
func (a *syncAdapter) ListRecentFills(
	context.Context,
	string,
	string,
) ([]exchange.Fill, string, error) {
	if a.fillStarted != nil {
		close(a.fillStarted)
		<-a.fillRelease
	}
	return []exchange.Fill{a.fill}, "11", nil
}
func (a *syncAdapter) GetOrder(context.Context, string, string) (exchange.Order, error) {
	return a.order, nil
}
func (a *syncAdapter) GetOrderByExchangeID(
	_ context.Context,
	symbol string,
	exchangeOrderID string,
) (exchange.Order, error) {
	if a.lookupErr != nil {
		return exchange.Order{}, a.lookupErr
	}
	current := a.order
	current.Symbol = symbol
	current.ExchangeOrderID = exchangeOrderID
	return current, nil
}
func (*syncAdapter) PlaceOrder(context.Context, exchange.OrderRequest) (exchange.Order, error) {
	return exchange.Order{}, nil
}
func (*syncAdapter) CancelOrder(context.Context, string, string) (exchange.Order, error) {
	return exchange.Order{}, nil
}
func (*syncAdapter) SetLeverage(context.Context, string, shared.Decimal) error {
	return nil
}
func (*syncAdapter) SetMarginMode(context.Context, string, exchange.MarginMode) error {
	return nil
}
func (*syncAdapter) SubscribePrivate(context.Context, exchange.EventHandler) error {
	return nil
}

func TestServiceSyncAccountImportsExternalOrderAndAppliesFacts(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	adapter := &syncAdapter{
		order: exchange.Order{
			ExchangeOrderID: "exchange-1", ClientOrderID: "manual-1",
			Symbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
			Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
			Quantity: shared.MustDecimal("1"), FilledQuantity: shared.MustDecimal("0.5"),
			AveragePrice: shared.MustDecimal("100"), Status: exchange.OrderStatusPartiallyFilled,
			CreatedAt: time.UnixMilli(1_000), UpdatedAt: time.UnixMilli(2_000),
		},
		fill: exchange.Fill{
			ExchangeTradeID: "trade-1", ExchangeOrderID: "exchange-1",
			ClientOrderID: "manual-1", Symbol: "BTC-USDT", Side: exchange.SideBuy,
			PositionSide: exchange.PositionSideNet,
			Quantity:     shared.MustDecimal("0.5"), Price: shared.MustDecimal("100"),
			SettlementAsset: "USDT", TradedAt: time.UnixMilli(1_500),
		},
	}
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: adapter},
		SessionState: readySessionState(true),
		Fills:        &consumer.Reducer{Store: tradeStore},
		Now:          func() time.Time { return time.UnixMilli(3_000) },
	}

	result, err := service.SyncAccount(context.Background(), "account-1")
	require.NoError(t, err)
	require.Equal(t, 1, result.FillsIngested)
	require.Equal(t, 1, result.OrdersUpdated)
	require.Equal(t, 1, result.PositionsUpdated)
	require.True(t, result.AccountSnapshotUpdated)
	require.True(t, result.Ready)

	orderRecord, err := tradeStore.GetOrderByClientID(
		context.Background(),
		"space-1",
		"account-1",
		"manual-1",
	)
	require.NoError(t, err)
	require.Equal(t, "EXTERNAL", orderRecord.Source)
	require.Equal(t, "PARTIALLY_FILLED", orderRecord.State)
	require.Equal(t, "0.5", orderRecord.FilledQuantity)

	account, err := tradeStore.GetExchangeAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.True(t, account.Ready)
	require.Equal(t, "11", account.FillCursors["BTC-USDT"])
	require.Equal(t, int64(3_000), account.LastSyncAt)
	require.Equal(t, int64(2_000), account.SnapshotSourceTime)

	result, err = service.SyncAccount(context.Background(), "account-1")
	require.NoError(t, err)
	require.Zero(t, result.FillsIngested, "replayed REST Fill must be idempotent")
}

func TestApplyPartialPositionUsesConfiguredLeverage(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: &syncAdapter{}},
		Fills: &consumer.Reducer{Store: tradeStore},
	}
	err := service.ApplyPosition(context.Background(), "account-1", exchange.Position{
		ExchangeAccountID: "account-1",
		Symbol:            "BTC-USDT",
		PositionSide:      exchange.PositionSideNet,
		SignedQuantity:    shared.MustDecimal("1"),
		EntryPrice:        shared.MustDecimal("100"),
		MarginMode:        exchange.MarginModeCross,
		UnrealizedPnL:     shared.MustDecimal("2"),
		ExchangeUpdatedAt: time.UnixMilli(2_000),
		Present: exchange.PositionPresence{
			SignedQuantity: true,
			EntryPrice:     true,
			MarginMode:     true,
			UnrealizedPnL:  true,
		},
		RequiresSync: true,
	})
	require.NoError(t, err)
	var position store.PositionRecord
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		var found bool
		var getErr error
		position, found, getErr = tx.GetPosition(
			"space-1",
			"account-1",
			"BTC-USDT",
			"NET",
		)
		require.True(t, found)
		return getErr
	}))
	require.Equal(t, "5", position.Leverage)
	require.Equal(t, "1", position.SignedQuantity)
}

func TestApplyUnknownPartialPositionDefersToFullSync(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpdateExchangeAccountSync(
			"space-1",
			"account-1",
			store.ExchangeAccountSyncState{LastSyncAt: 1},
		)
	}))
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: &syncAdapter{}},
		Fills: &consumer.Reducer{Store: tradeStore},
	}
	require.NoError(t, service.ApplyPosition(context.Background(), "account-1", exchange.Position{
		Symbol: "ETH-USDT", PositionSide: exchange.PositionSideNet,
		SignedQuantity: shared.MustDecimal("1"),
		EntryPrice:     shared.MustDecimal("100"), MarginMode: exchange.MarginModeCross,
		ExchangeUpdatedAt: time.UnixMilli(2_000),
		Present: exchange.PositionPresence{
			SignedQuantity: true, EntryPrice: true, MarginMode: true,
		},
		RequiresSync: true,
	}))
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		_, found, err := tx.GetPosition("space-1", "account-1", "ETH-USDT", "NET")
		require.False(t, found)
		return err
	}))
}

func TestApplyFillImportsEmptyClientIDPerSymbol(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.UpdateExchangeAccountSync(
			"space-1",
			"account-1",
			store.ExchangeAccountSyncState{
				LeverageSettings: store.LeverageSettings{
					"BTC-USDT": "5",
					"ETH-USDT": "5",
				},
				LastSyncAt: 1,
			},
		); err != nil {
			return err
		}
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "BINANCE", MarketType: "SWAP", Symbol: "ETH-USDT",
			InstrumentID: "ETHUSDT", BaseAsset: "ETH", QuoteAsset: "USDT",
			SettlementAsset: "USDT", Linear: true, ContractValue: "0.1",
			ContractValueAsset: "ETH", ExchangeQuantityStep: "1",
			MinExchangeQuantity: "1", PriceTick: "0.1", Status: "TRADING",
		})
	}))
	adapter := &syncAdapter{order: exchange.Order{
		OrderType: exchange.OrderTypeMarket,
		Side:      exchange.SideBuy, PositionSide: exchange.PositionSideNet,
		Quantity: shared.MustDecimal("1"), Status: exchange.OrderStatusOpen,
		CreatedAt: time.UnixMilli(1_000), UpdatedAt: time.UnixMilli(1_000),
	}}
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: adapter},
		Fills: &consumer.Reducer{Store: tradeStore},
	}
	for index, symbol := range []string{"BTC-USDT", "ETH-USDT"} {
		applied, err := service.ApplyFill(context.Background(), "account-1", exchange.Fill{
			ExchangeTradeID: fmt.Sprintf("trade-%d", index),
			ExchangeOrderID: "shared-exchange-id",
			Symbol:          symbol,
			Side:            exchange.SideBuy,
			PositionSide:    exchange.PositionSideNet,
			Quantity:        shared.MustDecimal("0.1"),
			Price:           shared.MustDecimal("100"),
			SettlementAsset: "USDT",
			TradedAt:        time.UnixMilli(2_000 + int64(index)),
		})
		require.NoError(t, err)
		require.True(t, applied)
	}
	first, err := tradeStore.GetOrderByExchangeID(
		context.Background(), "space-1", "account-1", "BTC-USDT", "shared-exchange-id",
	)
	require.NoError(t, err)
	second, err := tradeStore.GetOrderByExchangeID(
		context.Background(), "space-1", "account-1", "ETH-USDT", "shared-exchange-id",
	)
	require.NoError(t, err)
	require.NotEqual(t, first.OrderID, second.OrderID)
	require.NotEqual(t, first.ClientOrderID, second.ClientOrderID)
}

func TestApplySnapshotAggregatesSyntheticExternalOrderFills(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	adapter := &syncAdapter{lookupErr: &exchange.Error{
		Kind: exchange.ErrorOrderNotFound,
	}}
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: adapter},
		Fills: &consumer.Reducer{Store: tradeStore},
	}
	fills := []exchange.Fill{
		{
			ExchangeTradeID: "trade-1", ExchangeOrderID: "archived-order",
			Symbol: "BTC-USDT", Side: exchange.SideBuy,
			PositionSide: exchange.PositionSideNet,
			Quantity:     shared.MustDecimal("0.2"), Price: shared.MustDecimal("100"),
			SettlementAsset: "USDT", TradedAt: time.UnixMilli(1_000),
		},
		{
			ExchangeTradeID: "trade-2", ExchangeOrderID: "archived-order",
			Symbol: "BTC-USDT", Side: exchange.SideBuy,
			PositionSide: exchange.PositionSideNet,
			Quantity:     shared.MustDecimal("0.3"), Price: shared.MustDecimal("110"),
			SettlementAsset: "USDT", TradedAt: time.UnixMilli(1_100),
		},
	}
	result, err := service.ApplySnapshot(context.Background(), "account-1", Snapshot{
		Fills: fills,
		Account: exchange.AccountSnapshot{
			ExchangeUpdatedAt: time.UnixMilli(2_000),
		},
	})
	require.NoError(t, err)
	require.Equal(t, 2, result.FillsIngested)
	order, err := tradeStore.GetOrderByExchangeID(
		context.Background(),
		"space-1",
		"account-1",
		"BTC-USDT",
		"archived-order",
	)
	require.NoError(t, err)
	require.Equal(t, "0.5", order.Quantity)
	require.Equal(t, "0.5", order.FilledQuantity)
	require.Equal(t, "FILLED", order.State)
}

func TestMergePrivateSnapshotUsesPresenceAndPreservesUnmentionedBalances(t *testing.T) {
	current := store.ExchangeAccountSnapshot{
		Balances: []store.AssetBalance{
			{Asset: "BTC", Available: "1", Total: "1"},
			{Asset: "USDT", Available: "100", Total: "100"},
		},
		AvailableFunds:    "100",
		ExchangeUpdatedAt: 1_000,
	}
	merged := mergePrivateSnapshot(current, exchange.AccountSnapshot{
		Balances: []exchange.AssetBalance{{
			Asset: "BTC", Available: shared.Zero(), Total: shared.Zero(),
		}},
		AvailableFunds:    shared.Zero(),
		ExchangeUpdatedAt: time.UnixMilli(2_000),
		Present:           exchange.AccountSnapshotPresence{Balances: true},
	})
	require.Equal(t, "100", merged.AvailableFunds)
	require.Len(t, merged.Balances, 2)
	require.Equal(t, "0", merged.Balances[0].Available)
	require.Equal(t, "USDT", merged.Balances[1].Asset)

	merged = mergePrivateSnapshot(merged, exchange.AccountSnapshot{
		AvailableFunds:    shared.Zero(),
		ExchangeUpdatedAt: time.UnixMilli(3_000),
		Present: exchange.AccountSnapshotPresence{
			AvailableFunds: true,
		},
	})
	require.Equal(t, "0", merged.AvailableFunds)
}

func TestApplyAccountSnapshotMergesDisjointBalancesAtSameTimestamp(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	service := Service{
		Store:    tradeStore,
		Adapters: syncAdapterSource{adapter: &syncAdapter{}},
		Fills:    &consumer.Reducer{Store: tradeStore},
	}
	updatedAt := time.UnixMilli(2_000)

	require.NoError(t, service.ApplyAccountSnapshot(
		context.Background(),
		"account-1",
		exchange.AccountSnapshot{
			Balances: []exchange.AssetBalance{{
				Asset: "BTC", Available: shared.MustDecimal("1"),
				Total: shared.MustDecimal("1"),
			}},
			ExchangeUpdatedAt: updatedAt,
			Present:           exchange.AccountSnapshotPresence{Balances: true},
		},
	))
	require.NoError(t, service.ApplyAccountSnapshot(
		context.Background(),
		"account-1",
		exchange.AccountSnapshot{
			Balances: []exchange.AssetBalance{{
				Asset: "USDT", Available: shared.MustDecimal("100"),
				Total: shared.MustDecimal("100"),
			}},
			ExchangeUpdatedAt: updatedAt,
			Present:           exchange.AccountSnapshotPresence{Balances: true},
		},
	))

	account, err := tradeStore.GetExchangeAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.Equal(t, int64(2_000), account.Snapshot.ExchangeUpdatedAt)
	require.Len(t, account.Snapshot.Balances, 2)
	require.Equal(t, "BTC", account.Snapshot.Balances[0].Asset)
	require.Equal(t, "USDT", account.Snapshot.Balances[1].Asset)
}

func TestPrivateSnapshotDoesNotAdvanceFullSnapshotWatermark(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpdateExchangeAccountFacts(
			"space-1",
			"account-1",
			nil,
			store.ExchangeAccountSnapshot{
				AvailableFunds: "900", ExchangeUpdatedAt: 2_000,
			},
			2_000,
			3_000,
		)
	}))
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: &syncAdapter{}},
		Fills: &consumer.Reducer{Store: tradeStore},
	}
	require.NoError(t, service.ApplyAccountSnapshot(
		context.Background(),
		"account-1",
		exchange.AccountSnapshot{
			Balances: []exchange.AssetBalance{{
				Asset: "USDT", Available: shared.MustDecimal("800"),
				Total: shared.MustDecimal("800"),
			}},
			ExchangeUpdatedAt: time.UnixMilli(2_500),
			Present:           exchange.AccountSnapshotPresence{Balances: true},
			RequiresSync:      true,
		},
	))
	account, err := tradeStore.GetExchangeAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.Equal(t, int64(3_000), account.LastSyncAt)
	require.Equal(t, int64(2_000), account.SnapshotSourceTime)
	require.Equal(t, int64(2_500), account.Snapshot.ExchangeUpdatedAt)
}

func TestDisconnectReadinessWinsAgainstConcurrentManualSync(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	adapter := &syncAdapter{
		order: exchange.Order{
			ExchangeOrderID: "exchange-1", ClientOrderID: "manual-1",
			Symbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
			Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
			Quantity: shared.MustDecimal("1"), Status: exchange.OrderStatusOpen,
			CreatedAt: time.UnixMilli(1_000), UpdatedAt: time.UnixMilli(2_000),
		},
		fill: exchange.Fill{
			ExchangeTradeID: "trade-1", ExchangeOrderID: "exchange-1",
			ClientOrderID: "manual-1", Symbol: "BTC-USDT", Side: exchange.SideBuy,
			PositionSide: exchange.PositionSideNet,
			Quantity:     shared.MustDecimal("0.1"), Price: shared.MustDecimal("100"),
			SettlementAsset: "USDT", TradedAt: time.UnixMilli(1_500),
		},
		fillStarted: make(chan struct{}),
		fillRelease: make(chan struct{}),
	}
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: adapter},
		SessionState: readySessionState(true),
		Fills:        &consumer.Reducer{Store: tradeStore},
		Now:          func() time.Time { return time.UnixMilli(3_000) },
	}
	syncDone := make(chan error, 1)
	go func() {
		_, err := service.SyncAccount(context.Background(), "account-1")
		syncDone <- err
	}()
	<-adapter.fillStarted
	disconnectDone := make(chan error, 1)
	go func() {
		disconnectDone <- service.SetReady(
			context.Background(),
			"account-1",
			false,
			errors.New("disconnected"),
		)
	}()
	select {
	case err := <-disconnectDone:
		t.Fatalf("disconnect update bypassed account synchronization: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(adapter.fillRelease)
	require.NoError(t, <-syncDone)
	require.NoError(t, <-disconnectDone)
	account, err := tradeStore.GetExchangeAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.False(t, account.Ready)
	require.Equal(t, "disconnected", account.LastError)
}

func TestSyncSymbolsRetainsConfiguredSpotSymbolAfterSellToZero(t *testing.T) {
	symbols := syncSymbols(
		store.ExchangeAccountRecord{
			MarketType:  "SPOT",
			SyncSymbols: []string{"BTC-USDT"},
		},
		nil,
		nil,
		nil,
		[]store.InstrumentRecord{{
			Symbol: "BTC-USDT", BaseAsset: "BTC", SettlementAsset: "USDT",
			Status: "TRADING",
		}},
		exchange.AccountSnapshot{Balances: []exchange.AssetBalance{{
			Asset: "BTC", Total: shared.Zero(),
		}}},
	)
	require.Equal(t, []string{"BTC-USDT"}, symbols)

	symbols = syncSymbols(
		store.ExchangeAccountRecord{MarketType: "SWAP"},
		nil,
		nil,
		[]exchange.Position{{Symbol: "ETH-USDT"}},
		nil,
		exchange.AccountSnapshot{},
	)
	require.Equal(t, []string{"ETH-USDT"}, symbols)
}

func openSyncStore(t *testing.T) *store.Store {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tradeStore.Close()) })
	return tradeStore
}

func seedSyncAccount(t *testing.T, tradeStore *store.Store) {
	t.Helper()
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateExchangeAccount(store.ExchangeAccountRecord{
			SpaceID: "space-1", ExchangeAccountID: "account-1", Name: "main",
			Exchange: "BINANCE", MarketType: "SWAP", ExecutionMode: "LIVE",
			CredentialSecretID: "secret-1", SettlementAsset: "USDT",
			MarginMode: "CROSS", Status: "ENABLED",
			LeverageSettings: store.LeverageSettings{"BTC-USDT": "5"},
		}); err != nil {
			return err
		}
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "BINANCE", MarketType: "SWAP", Symbol: "BTC-USDT",
			InstrumentID: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT",
			SettlementAsset: "USDT", Linear: true, ContractValue: "0.1",
			ContractValueAsset: "BTC", ExchangeQuantityStep: "1",
			MinExchangeQuantity: "1", PriceTick: "0.1", Status: "TRADING",
		})
	}))
}
