package service

import (
	"context"
	"errors"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	_ "github.com/mooyang-code/moox/modules/trade/internal/exchange/all"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestOrderService_NewAdapter_KnownExchange_ShouldReturnAdapter(t *testing.T) {
	svc := New("trade", WithExchangeFactory(exchange.New))
	adapter, err := svc.Order.NewAdapter("binance")
	require.NoError(t, err)
	assert.Equal(t, "binance", adapter.Name())
}

func TestOrderService_NewAdapter_UnknownExchange_ShouldError(t *testing.T) {
	svc := New("trade", WithExchangeFactory(func(name string) (exchange.ExchangeAdapter, error) {
		return nil, errors.New("unknown")
	}))
	_, err := svc.Order.NewAdapter("missing")
	assert.Error(t, err)
}

func TestOrderService_TestChannel_Reachable_ShouldReturnLatency(t *testing.T) {
	adapter := &fakeExecAdapter{}
	svc, _ := newExecOrderService(t, adapter)
	ok, latency, err := svc.TestChannel(context.Background(), "crypto", "ch_1")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, int64(12), latency)
}

func TestOrderService_TestChannel_MissingChannel_ShouldFail(t *testing.T) {
	adapter := &fakeExecAdapter{}
	svc, _ := newExecOrderService(t, adapter)
	ok, _, err := svc.TestChannel(context.Background(), "crypto", "missing")
	assert.Error(t, err)
	assert.False(t, ok)
}

func TestOrderService_CancelAllOrders_ValidChannel_ShouldDelegate(t *testing.T) {
	adapter := &fakeExecAdapter{}
	svc, _ := newExecOrderService(t, adapter)
	n, err := svc.CancelAllOrders(context.Background(), "crypto", "ch_1", "BTCUSDT")
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

func TestOrderService_SetLeverage_ValidInput_ShouldSucceed(t *testing.T) {
	adapter := &fakeExecAdapter{}
	svc, _ := newExecOrderService(t, adapter)
	err := svc.SetLeverage(context.Background(), "crypto", "ch_1", "BTCUSDT", "10")
	assert.NoError(t, err)
}

func TestOrderService_ListInstruments_ShouldReturnRules(t *testing.T) {
	adapter := &fakeExecAdapter{instruments: []exchange.Instrument{{Symbol: "ETHUSDT"}}}
	svc, _ := newExecOrderService(t, adapter)
	got, err := svc.ListInstruments(context.Background(), "crypto", "ch_1", exchange.MarketSpot)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "ETHUSDT", got[0].Symbol)
}

func TestOrderService_CreateChannel_ValidInput_ShouldReturnID(t *testing.T) {
	store := &memoryAccountStore{}
	svc := &OrderService{store: store, exNew: nil}
	ctx := context.Background()
	id, err := svc.CreateChannel(ctx, "crypto", &TradeChannel{
		ChannelName: "binance", Exchange: "binance", AccountID: "acc-1",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, id)
	got, err := store.GetChannel(ctx, "crypto", id)
	require.NoError(t, err)
	assert.Equal(t, "binance", got.ChannelName)
}

func TestOrderService_CreateChannel_InvalidInput_ShouldReject(t *testing.T) {
	svc := &OrderService{store: &memoryAccountStore{}}
	_, err := svc.CreateChannel(context.Background(), "crypto", &TradeChannel{Exchange: "binance"})
	assert.ErrorIs(t, err, ErrInvalidParam)
}

func TestOrderService_DeleteChannel_EmptyID_ShouldReject(t *testing.T) {
	svc := &OrderService{store: &memoryAccountStore{}}
	err := svc.DeleteChannel(context.Background(), "crypto", "")
	assert.ErrorIs(t, err, ErrInvalidParam)
}

func TestService_Health_ShouldReturnModuleName(t *testing.T) {
	svc := New("trade-test")
	got := svc.Health()
	assert.Equal(t, "trade-test", got.Module)
	assert.True(t, got.Ready)
}

func TestService_New_EmptyModule_ShouldDefaultTrade(t *testing.T) {
	svc := New("")
	assert.Equal(t, "trade", svc.module)
}

type syncPositionStore struct {
	Store
	account   *Account
	channel   *TradeChannel
	apiKey    *APIKey
	replaced  []*Position
	listAfter []*Position
	orders    []*Order
	trades    []*Trade
}

func (s *syncPositionStore) GetAccount(ctx context.Context, spaceID, accountID string) (*Account, error) {
	if s.account == nil || s.account.AccountID != accountID {
		return nil, ErrNotFound
	}
	return s.account, nil
}

func (s *syncPositionStore) GetChannel(ctx context.Context, spaceID, channelID string) (*TradeChannel, error) {
	if s.channel == nil || s.channel.ChannelID != channelID {
		return nil, ErrNotFound
	}
	return s.channel, nil
}

func (s *syncPositionStore) GetAPIKey(ctx context.Context, spaceID, apiKeyID string) (*APIKey, error) {
	if s.apiKey == nil || s.apiKey.APIKeyID != apiKeyID {
		return nil, ErrNotFound
	}
	return s.apiKey, nil
}

func (s *syncPositionStore) ReplacePositions(ctx context.Context, spaceID, accountID, symbol string, positions []*Position) error {
	s.replaced = positions
	s.listAfter = positions
	return nil
}

func (s *syncPositionStore) ListPositions(ctx context.Context, spaceID, accountID, symbol string) ([]*Position, error) {
	return s.listAfter, nil
}

func (s *syncPositionStore) UpsertOrders(ctx context.Context, spaceID string, orders []*Order) error {
	for _, o := range orders {
		cp := *o
		cp.SpaceID = spaceID
		s.orders = append(s.orders, &cp)
	}
	return nil
}

func (s *syncPositionStore) ListOrders(ctx context.Context, spaceID string, f OrderFilter, page Page) ([]*Order, int, error) {
	out := make([]*Order, 0, len(s.orders))
	for _, o := range s.orders {
		if f.AccountID != "" && o.AccountID != f.AccountID {
			continue
		}
		if f.Symbol != "" && o.Symbol != f.Symbol {
			continue
		}
		out = append(out, o)
	}
	return out, len(out), nil
}

func (s *syncPositionStore) AppendTrades(ctx context.Context, spaceID string, trades []*Trade) error {
	for _, t := range trades {
		cp := *t
		cp.SpaceID = spaceID
		s.trades = append(s.trades, &cp)
	}
	return nil
}

func (s *syncPositionStore) ListTrades(ctx context.Context, spaceID string, f TradeFilter, page Page) ([]*Trade, int, error) {
	out := make([]*Trade, 0, len(s.trades))
	for _, tr := range s.trades {
		if f.AccountID != "" && tr.AccountID != f.AccountID {
			continue
		}
		if f.Symbol != "" && tr.Symbol != f.Symbol {
			continue
		}
		out = append(out, tr)
	}
	return out, len(out), nil
}

type fakePositionAdapter struct {
	exchange.ExchangeAdapter
	gotMarket             exchange.MarketType
	gotSymbol             string
	gotAssets             []string
	convertibleDustAssets []exchange.DustConvertibleAsset
	positions             []exchange.Position
	orders                []exchange.Order
	trades                []exchange.Trade
	dust                  *exchange.DustTransferResult
}

func (a *fakePositionAdapter) ListPositions(ctx context.Context, cred exchange.Credential, market exchange.MarketType, symbol string) ([]exchange.Position, error) {
	a.gotMarket = market
	a.gotSymbol = symbol
	return a.positions, nil
}

func (a *fakePositionAdapter) ListOrders(ctx context.Context, cred exchange.Credential, req *exchange.ListOrdersReq) ([]exchange.Order, error) {
	a.gotMarket = req.Market
	a.gotSymbol = req.Symbol
	return a.orders, nil
}

func (a *fakePositionAdapter) ListTrades(ctx context.Context, cred exchange.Credential, req *exchange.ListTradesReq) ([]exchange.Trade, error) {
	a.gotMarket = req.Market
	a.gotSymbol = req.Symbol
	return a.trades, nil
}

func (a *fakePositionAdapter) ConvertDust(ctx context.Context, cred exchange.Credential, req *exchange.DustTransferReq) (*exchange.DustTransferResult, error) {
	a.gotAssets = append([]string(nil), req.Assets...)
	return a.dust, nil
}

func (a *fakePositionAdapter) ListConvertibleDustAssets(ctx context.Context, cred exchange.Credential, req *exchange.DustConvertibleReq) ([]exchange.DustConvertibleAsset, error) {
	return a.convertibleDustAssets, nil
}

func TestSyncPositionsFetchesExchangeAndReplacesSnapshot(t *testing.T) {
	store := &syncPositionStore{
		account: &Account{AccountID: "acc_1", ChannelID: "ch_1"},
		channel: &TradeChannel{
			ChannelID:  "ch_1",
			Exchange:   "binance",
			MarketType: "swap",
			AccountID:  "acc_1",
			APIKeyID:   "ak_1",
		},
		apiKey: &APIKey{APIKeyID: "ak_1", APIKey: "key", APISecret: "secret"},
	}
	adapter := &fakePositionAdapter{
		positions: []exchange.Position{
			{
				Symbol: "BTCUSDT", PosSide: "long", Quantity: "0.01",
				AvgPrice: "60000", Leverage: "10", Margin: "60",
				LiqPrice: "50000", UnrealizedPnl: "12.5",
			},
		},
	}
	svc := New("trade", WithStore(store), WithExchangeFactory(func(name string) (exchange.ExchangeAdapter, error) {
		if name != "binance" {
			t.Fatalf("exchange name = %q, want binance", name)
		}
		return adapter, nil
	}))

	got, err := svc.Order.SyncPositions(context.Background(), "crypto", "acc_1", "BTCUSDT")
	if err != nil {
		t.Fatalf("SyncPositions returned error: %v", err)
	}
	if adapter.gotMarket != exchange.MarketSwap || adapter.gotSymbol != "BTCUSDT" {
		t.Fatalf("adapter called with market=%s symbol=%s", adapter.gotMarket, adapter.gotSymbol)
	}
	if len(got) != 1 || len(store.replaced) != 1 {
		t.Fatalf("positions len got=%d replaced=%d", len(got), len(store.replaced))
	}
	p := got[0]
	if p.AccountID != "acc_1" || p.ChannelID != "ch_1" || p.Exchange != "binance" {
		t.Fatalf("position ownership not filled: %+v", p)
	}
	if p.PositionID == "" {
		t.Fatalf("position_id must be stable and non-empty")
	}
}

func TestSyncOrdersFetchesExchangeAndUpsertsLocalOrders(t *testing.T) {
	store := &syncPositionStore{
		account: &Account{AccountID: "acc_1", ChannelID: "ch_1"},
		channel: &TradeChannel{
			ChannelID:  "ch_1",
			Exchange:   "binance",
			MarketType: "spot",
			AccountID:  "acc_1",
			APIKeyID:   "ak_1",
		},
		apiKey: &APIKey{APIKeyID: "ak_1", APIKey: "key", APISecret: "secret"},
	}
	adapter := &fakePositionAdapter{
		orders: []exchange.Order{{
			ExchangeOrderID: "1001", ClientOrderID: "cli-1", Symbol: "BTCUSDT",
			Market: exchange.MarketSpot, Side: exchange.SideBuy, Type: exchange.TypeLimit,
			Price: "60000", Quantity: "0.01", FilledQty: "0.01", FilledAmount: "600",
			Status: exchange.StatusFilled, CreatedAt: 1710000000000, UpdatedAt: 1710000001000,
		}},
	}
	svc := New("trade", WithStore(store), WithExchangeFactory(func(name string) (exchange.ExchangeAdapter, error) {
		return adapter, nil
	}))

	got, total, err := svc.Order.SyncOrders(context.Background(), "crypto", "acc_1", "BTCUSDT", false, 0, 0, Page{PageNo: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("SyncOrders returned error: %v", err)
	}
	if adapter.gotMarket != exchange.MarketSpot || adapter.gotSymbol != "BTCUSDT" {
		t.Fatalf("adapter called with market=%s symbol=%s", adapter.gotMarket, adapter.gotSymbol)
	}
	if total != 1 || len(got) != 1 || len(store.orders) != 1 {
		t.Fatalf("orders len total=%d got=%d stored=%d", total, len(got), len(store.orders))
	}
	o := got[0]
	if o.AccountID != "acc_1" || o.ChannelID != "ch_1" || o.Exchange != "binance" || o.OrderID == "" {
		t.Fatalf("order ownership not filled: %+v", o)
	}
}

func TestSyncTradesFetchesExchangeAndAppendsLocalTrades(t *testing.T) {
	store := &syncPositionStore{
		account: &Account{AccountID: "acc_1", ChannelID: "ch_1"},
		channel: &TradeChannel{
			ChannelID:  "ch_1",
			Exchange:   "binance",
			MarketType: "spot",
			AccountID:  "acc_1",
			APIKeyID:   "ak_1",
		},
		apiKey: &APIKey{APIKeyID: "ak_1", APIKey: "key", APISecret: "secret"},
	}
	adapter := &fakePositionAdapter{
		trades: []exchange.Trade{{
			ExchangeTradeID: "2001", OrderID: "1001", Symbol: "BTCUSDT",
			Side: exchange.SideBuy, Price: "60000", Quantity: "0.01", Amount: "600",
			Fee: "0.6", FeeCurrency: "USDT", Role: "taker", TradedAt: 1710000001000,
		}},
	}
	svc := New("trade", WithStore(store), WithExchangeFactory(func(name string) (exchange.ExchangeAdapter, error) {
		return adapter, nil
	}))

	got, total, err := svc.Order.SyncTrades(context.Background(), "crypto", "acc_1", "BTCUSDT", "", 0, 0, Page{PageNo: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("SyncTrades returned error: %v", err)
	}
	if adapter.gotMarket != exchange.MarketSpot || adapter.gotSymbol != "BTCUSDT" {
		t.Fatalf("adapter called with market=%s symbol=%s", adapter.gotMarket, adapter.gotSymbol)
	}
	if total != 1 || len(got) != 1 || len(store.trades) != 1 {
		t.Fatalf("trades len total=%d got=%d stored=%d", total, len(got), len(store.trades))
	}
	tr := got[0]
	if tr.AccountID != "acc_1" || tr.ChannelID != "ch_1" || tr.Exchange != "binance" || tr.TradeID == "" {
		t.Fatalf("trade ownership not filled: %+v", tr)
	}
}

func TestConvertDustUsesChannelAdapter(t *testing.T) {
	store := &syncPositionStore{
		channel: &TradeChannel{
			ChannelID:  "ch_1",
			Exchange:   "binance",
			MarketType: "spot",
			AccountID:  "acc_1",
			APIKeyID:   "ak_1",
		},
		apiKey: &APIKey{APIKeyID: "ak_1", APIKey: "key", APISecret: "secret"},
	}
	adapter := &fakePositionAdapter{
		convertibleDustAssets: []exchange.DustConvertibleAsset{{Asset: "GALA"}},
		dust: &exchange.DustTransferResult{
			TotalTransfered: "0.01",
			Results: []exchange.DustTransferItem{{
				Asset: "GALA", Amount: "1225.773", TransferedAmount: "0.01",
			}},
		},
	}
	svc := New("trade", WithStore(store), WithExchangeFactory(func(name string) (exchange.ExchangeAdapter, error) {
		return adapter, nil
	}))

	got, err := svc.Order.ConvertDust(context.Background(), "crypto", "ch_1", []string{"GALA"})
	if err != nil {
		t.Fatalf("ConvertDust returned error: %v", err)
	}
	if len(adapter.gotAssets) != 1 || adapter.gotAssets[0] != "GALA" {
		t.Fatalf("adapter assets = %#v, want GALA", adapter.gotAssets)
	}
	if got == nil || len(got.Results) != 1 || got.Results[0].Asset != "GALA" {
		t.Fatalf("ConvertDust result = %#v", got)
	}
}

func TestConvertDustFiltersAssetsUnavailableForDustTransfer(t *testing.T) {
	store := &syncPositionStore{
		channel: &TradeChannel{
			ChannelID:  "ch_1",
			Exchange:   "binance",
			MarketType: "spot",
			AccountID:  "acc_1",
			APIKeyID:   "ak_1",
		},
		apiKey: &APIKey{APIKeyID: "ak_1", APIKey: "key", APISecret: "secret"},
	}
	adapter := &fakePositionAdapter{
		convertibleDustAssets: []exchange.DustConvertibleAsset{{Asset: "GALA"}},
		dust: &exchange.DustTransferResult{
			Results: []exchange.DustTransferItem{{Asset: "GALA", Amount: "1225.773", TransferedAmount: "0.01"}},
		},
	}
	svc := New("trade", WithStore(store), WithExchangeFactory(func(name string) (exchange.ExchangeAdapter, error) {
		return adapter, nil
	}))

	got, err := svc.Order.ConvertDust(context.Background(), "crypto", "ch_1", []string{"GALA", "SHIB"})
	if err != nil {
		t.Fatalf("ConvertDust returned error: %v", err)
	}
	if len(adapter.gotAssets) != 1 || adapter.gotAssets[0] != "GALA" {
		t.Fatalf("adapter assets = %#v, want only GALA", adapter.gotAssets)
	}
	if got == nil || len(got.Skipped) != 1 || got.Skipped[0].Asset != "SHIB" {
		t.Fatalf("skipped assets = %#v, want SHIB", got)
	}
}
