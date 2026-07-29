package paper

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

type publicExchangeStub struct{}
type fillStoreStub struct {
	fills  []store.FillRecord
	orders []store.OrderRecord
}

func (s fillStoreStub) ListFills(
	_ context.Context,
	_ string,
	query store.FillQuery,
) ([]store.FillRecord, int64, error) {
	if query.Offset >= len(s.fills) {
		return nil, int64(len(s.fills)), nil
	}
	end := query.Offset + query.Limit
	if end > len(s.fills) {
		end = len(s.fills)
	}
	return append([]store.FillRecord(nil), s.fills[query.Offset:end]...),
		int64(len(s.fills)), nil
}

func (s fillStoreStub) GetOrderByClientID(
	_ context.Context,
	_ string,
	_ string,
	clientOrderID string,
) (store.OrderRecord, error) {
	for _, order := range s.orders {
		if order.ClientOrderID == clientOrderID {
			return order, nil
		}
	}
	return store.OrderRecord{}, store.ErrInvalidRecord
}

func (s fillStoreStub) ListOrdersForAccount(
	context.Context,
	string,
	string,
	int64,
) ([]store.OrderRecord, error) {
	return append([]store.OrderRecord(nil), s.orders...), nil
}

func (publicExchangeStub) Exchange() exchange.Exchange { return exchange.ExchangeBinance }
func (publicExchangeStub) LoadInstruments(context.Context) ([]exchange.Instrument, error) {
	return []exchange.Instrument{{
		Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT",
		SettlementAsset: "USDT",
	}}, nil
}
func (publicExchangeStub) GetReferencePrice(context.Context, string) (exchange.ReferencePrice, error) {
	return exchange.ReferencePrice{Price: shared.MustDecimal("50000"), UpdatedAt: time.Now()}, nil
}
func (publicExchangeStub) GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error) {
	return exchange.AccountSnapshot{}, nil
}
func (publicExchangeStub) ListPositionSnapshots(context.Context) ([]exchange.Position, error) {
	return nil, nil
}
func (publicExchangeStub) ListOpenOrders(context.Context) ([]exchange.Order, error) {
	return nil, nil
}
func (publicExchangeStub) ListRecentFills(context.Context, string, string) ([]exchange.Fill, string, error) {
	return nil, "", nil
}
func (publicExchangeStub) GetOrder(context.Context, string, string) (exchange.Order, error) {
	return exchange.Order{}, nil
}
func (publicExchangeStub) PlaceOrder(context.Context, exchange.OrderRequest) (exchange.Order, error) {
	return exchange.Order{}, nil
}
func (publicExchangeStub) CancelOrder(context.Context, string, string) (exchange.Order, error) {
	return exchange.Order{}, nil
}
func (publicExchangeStub) SetLeverage(context.Context, string, shared.Decimal) error { return nil }
func (publicExchangeStub) SetMarginMode(context.Context, string, exchange.MarginMode) error {
	return nil
}
func (publicExchangeStub) SubscribePrivate(context.Context, exchange.EventHandler) error {
	return nil
}

func TestAdapterFillsOrderAndExposesSnapshots(t *testing.T) {
	adapter := New(
		publicExchangeStub{}, nil, "space-1", "account-1",
		exchange.MarketTypeSpot, "USDT", shared.MustDecimal("100000"),
		exchange.MarginModeUnspecified, nil,
	)
	reference, err := adapter.GetReferencePrice(context.Background(), "BTCUSDT")
	require.NoError(t, err)
	require.Equal(t, "50000", reference.Price.String())

	order, err := adapter.PlaceOrder(context.Background(), exchange.OrderRequest{
		ClientOrderID: "client-1", Symbol: "BTCUSDT",
		OrderType: exchange.OrderTypeMarket, Side: exchange.SideBuy,
		Quantity: shared.MustDecimal("0.01"), ReferencePrice: reference.Price,
	})
	require.NoError(t, err)
	require.Equal(t, exchange.OrderStatusFilled, order.Status)

	fills, cursor, err := adapter.ListRecentFills(context.Background(), "BTCUSDT", "")
	require.NoError(t, err)
	require.NotEmpty(t, cursor)
	require.Len(t, fills, 1)
	require.Equal(t, "0.01", fills[0].Quantity.String())
	require.Equal(t, exchange.PositionSideUnspecified, fills[0].PositionSide)

	positions, err := adapter.ListPositionSnapshots(context.Background())
	require.NoError(t, err)
	require.Empty(t, positions)

	snapshot, err := adapter.GetAccountSnapshot(context.Background())
	require.NoError(t, err)
	require.Equal(t, "99500", snapshot.AvailableFunds.String())
	require.Equal(t, "100000", snapshot.Equity.String())
	require.Equal(t, "0.01", balance(snapshot, "BTC").Available.String())

	_, err = adapter.PlaceOrder(context.Background(), exchange.OrderRequest{
		ClientOrderID: "client-2", Symbol: "BTCUSDT",
		OrderType: exchange.OrderTypeMarket, Side: exchange.SideSell,
		Quantity: shared.MustDecimal("0.01"), ReferencePrice: reference.Price,
	})
	require.NoError(t, err)
	snapshot, err = adapter.GetAccountSnapshot(context.Background())
	require.NoError(t, err)
	require.Equal(t, "100000", snapshot.AvailableFunds.String())
	require.Equal(t, "0", balance(snapshot, "BTC").Available.String())
}

func TestPaperAdapterRejectsLimitAsDefenseInDepth(t *testing.T) {
	adapter := New(
		publicExchangeStub{}, nil, "space-1", "account-1",
		exchange.MarketTypeSpot, "USDT", shared.MustDecimal("100000"),
		exchange.MarginModeUnspecified, nil,
	)
	price := shared.MustDecimal("100")

	_, err := adapter.PlaceOrder(context.Background(), exchange.OrderRequest{
		ClientOrderID: "paper-limit", Symbol: "BTC-USDT",
		OrderType: exchange.OrderTypeLimit, FillPolicy: exchange.FillPolicyGTC,
		Side: exchange.SideBuy, Quantity: shared.MustDecimal("1"),
		LimitPrice: &price, ReferencePrice: price,
	})

	require.Error(t, err)
	require.True(t, exchange.IsKind(err, exchange.ErrorRejected))
}

func TestAdapterBuildsSwapPositionAndMargin(t *testing.T) {
	adapter := New(
		publicExchangeStub{}, nil, "space-1", "account-1",
		exchange.MarketTypeSwap, "USDT", shared.MustDecimal("100000"),
		exchange.MarginModeCross, store.LeverageSettings{"BTCUSDT": "10"},
	)
	_, err := adapter.PlaceOrder(context.Background(), exchange.OrderRequest{
		ClientOrderID: "client-1", Symbol: "BTCUSDT",
		OrderType: exchange.OrderTypeMarket, Side: exchange.SideBuy,
		Quantity: shared.MustDecimal("0.01"), ReferencePrice: shared.MustDecimal("49000"),
	})
	require.NoError(t, err)

	positions, err := adapter.ListPositionSnapshots(context.Background())
	require.NoError(t, err)
	require.Len(t, positions, 1)
	require.Equal(t, exchange.PositionSideNet, positions[0].PositionSide)
	require.Equal(t, "10", positions[0].Leverage.String())
	require.Equal(t, "50000", positions[0].MarkPrice.String())
	require.Equal(t, "50", positions[0].UsedMargin.String())

	snapshot, err := adapter.GetAccountSnapshot(context.Background())
	require.NoError(t, err)
	require.Equal(t, "100010", snapshot.Equity.String())
	require.Equal(t, "99960", snapshot.AvailableFunds.String())

	_, err = adapter.PlaceOrder(context.Background(), exchange.OrderRequest{
		ClientOrderID: "client-2", Symbol: "BTCUSDT",
		OrderType: exchange.OrderTypeMarket, Side: exchange.SideSell,
		Quantity: shared.MustDecimal("0.01"), ReferencePrice: shared.MustDecimal("51000"),
		ReduceOnly: true,
	})
	require.NoError(t, err)
	positions, err = adapter.ListPositionSnapshots(context.Background())
	require.NoError(t, err)
	require.Empty(t, positions)
	snapshot, err = adapter.GetAccountSnapshot(context.Background())
	require.NoError(t, err)
	require.Equal(t, "100020", snapshot.Equity.String())
	require.Equal(t, "100020", snapshot.AvailableFunds.String())

	fills, _, err := adapter.ListRecentFills(context.Background(), "BTCUSDT", "")
	require.NoError(t, err)
	require.Len(t, fills, 2)
	require.Equal(t, "20", fills[1].RealizedPnL.String())
}

func TestAdapterRestoresPaperBalancesFromPersistedFills(t *testing.T) {
	tradedAt := time.Now().Add(-time.Minute).UTC()
	persisted := fillStoreStub{fills: []store.FillRecord{{
		ExchangeTradeID: "persisted-fill-1", ExchangeOrderID: "persisted-order-1",
		ExchangeAccountID: "account-1", Symbol: "BTCUSDT",
		Side: string(exchange.SideBuy), PositionSide: string(exchange.PositionSideNet),
		Price: "50000", Quantity: "0.01", Fee: "0", FeeAsset: "USDT",
		SettlementAsset: "USDT", RealizedPnL: "0", Role: "TAKER",
		TradedAt: tradedAt.UnixMilli(),
	}}}
	adapter := New(
		publicExchangeStub{}, persisted, "space-1", "account-1",
		exchange.MarketTypeSpot, "USDT", shared.MustDecimal("100000"),
		exchange.MarginModeUnspecified, nil,
	)
	_, err := adapter.LoadInstruments(context.Background())
	require.NoError(t, err)

	snapshot, err := adapter.GetAccountSnapshot(context.Background())
	require.NoError(t, err)
	require.Equal(t, "99500", snapshot.AvailableFunds.String())
	require.Equal(t, "100000", snapshot.Equity.String())
	require.Equal(t, "0.01", balance(snapshot, "BTC").Available.String())

	fills, cursor, err := adapter.ListRecentFills(
		context.Background(), "BTCUSDT", "persisted-fill-1",
	)
	require.NoError(t, err)
	require.Empty(t, fills)
	require.Equal(t, "persisted-fill-1", cursor)
}

func TestAdapterRecoversSubmittedOrderWhenFillWasNotYetPersisted(t *testing.T) {
	submittedAt := time.Now().Add(-time.Second).UTC().Truncate(time.Millisecond)
	history := fillStoreStub{orders: []store.OrderRecord{{
		SpaceID: "space-1", OrderID: "order-1",
		ExchangeAccountID: "account-1", ClientOrderID: "client-1",
		MarketType: string(exchange.MarketTypeSpot), Symbol: "BTCUSDT",
		OrderType: string(exchange.OrderTypeMarket), Side: string(exchange.SideBuy),
		Quantity: "0.01", ReferencePrice: "50000",
		State: "SUBMITTING", SubmittedAt: submittedAt.UnixMilli(),
	}}}
	adapter := New(
		publicExchangeStub{}, history, "space-1", "account-1",
		exchange.MarketTypeSpot, "USDT", shared.MustDecimal("100000"),
		exchange.MarginModeUnspecified, nil,
	)
	_, err := adapter.LoadInstruments(context.Background())
	require.NoError(t, err)

	order, err := adapter.GetOrder(context.Background(), "BTCUSDT", "client-1")
	require.NoError(t, err)
	require.Equal(t, exchange.OrderStatusFilled, order.Status)
	require.Equal(t, submittedAt, order.CreatedAt)

	fills, cursor, err := adapter.ListRecentFills(context.Background(), "BTCUSDT", "")
	require.NoError(t, err)
	require.Len(t, fills, 1)
	require.NotEmpty(t, cursor)
	require.Equal(t, order.ExchangeOrderID, fills[0].ExchangeOrderID)

	snapshot, err := adapter.GetAccountSnapshot(context.Background())
	require.NoError(t, err)
	require.Equal(t, "99500", snapshot.AvailableFunds.String())
	require.Equal(t, "100000", snapshot.Equity.String())
}

func TestAdapterExcludesCurrentSubmittingSwapOrderFromRealizedPnLHistory(t *testing.T) {
	start := time.Now().Add(-time.Minute).UTC().Truncate(time.Millisecond)
	history := fillStoreStub{orders: []store.OrderRecord{
		{
			SpaceID: "space-1", OrderID: "order-open",
			ExchangeAccountID: "account-1", ClientOrderID: "client-open",
			MarketType: string(exchange.MarketTypeSwap), Symbol: "BTCUSDT",
			OrderType: string(exchange.OrderTypeMarket), Side: string(exchange.SideBuy),
			PositionSide: string(exchange.PositionSideNet),
			Quantity:     "0.01", ReferencePrice: "49000",
			State: "FILLED", SubmittedAt: start.UnixMilli(),
		},
		{
			SpaceID: "space-1", OrderID: "order-close",
			ExchangeAccountID: "account-1", ClientOrderID: "client-close",
			MarketType: string(exchange.MarketTypeSwap), Symbol: "BTCUSDT",
			OrderType: string(exchange.OrderTypeMarket), Side: string(exchange.SideSell),
			PositionSide: string(exchange.PositionSideNet),
			Quantity:     "0.01", ReferencePrice: "51000", ReduceOnly: true,
			State: "SUBMITTING", SubmittedAt: start.Add(time.Second).UnixMilli(),
		},
	}}
	adapter := New(
		publicExchangeStub{}, history, "space-1", "account-1",
		exchange.MarketTypeSwap, "USDT", shared.MustDecimal("100000"),
		exchange.MarginModeCross, store.LeverageSettings{"BTCUSDT": "10"},
	)
	_, err := adapter.LoadInstruments(context.Background())
	require.NoError(t, err)

	_, err = adapter.PlaceOrder(context.Background(), exchange.OrderRequest{
		ClientOrderID: "client-close", Symbol: "BTCUSDT",
		OrderType: exchange.OrderTypeMarket, Side: exchange.SideSell,
		PositionSide: exchange.PositionSideNet,
		Quantity:     shared.MustDecimal("0.01"), ReferencePrice: shared.MustDecimal("51000"),
		ReduceOnly: true,
	})
	require.NoError(t, err)

	fills, _, err := adapter.ListRecentFills(context.Background(), "BTCUSDT", "")
	require.NoError(t, err)
	require.Len(t, fills, 2)
	require.Equal(t, "20", fills[1].RealizedPnL.String())
}

func balance(snapshot exchange.AccountSnapshot, asset string) exchange.AssetBalance {
	for _, current := range snapshot.Balances {
		if current.Asset == asset {
			return current
		}
	}
	return exchange.AssetBalance{}
}
