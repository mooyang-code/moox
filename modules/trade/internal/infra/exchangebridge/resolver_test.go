package exchangebridge

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	legacy "github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubStore struct {
	channel *service.TradeChannel
	apiKey  *service.APIKey
}

func (s stubStore) GetChannel(context.Context, string, string) (*service.TradeChannel, error) {
	if s.channel == nil {
		return nil, errors.New("channel not found")
	}
	return s.channel, nil
}
func (s stubStore) GetAPIKey(context.Context, string, string) (*service.APIKey, error) {
	if s.apiKey == nil {
		return nil, errors.New("api key not found")
	}
	return s.apiKey, nil
}

func (s stubStore) CreateAccount(context.Context, string, *service.Account) error { return nil }
func (s stubStore) UpdateAccount(context.Context, string, *service.Account) error { return nil }
func (s stubStore) DeleteAccount(context.Context, string, string) error           { return nil }
func (s stubStore) GetAccount(context.Context, string, string) (*service.Account, error) {
	return nil, nil
}
func (s stubStore) ListAccounts(context.Context, string, service.AccountFilter, service.Page) ([]*service.Account, int, error) {
	return nil, 0, nil
}
func (s stubStore) GetBalances(context.Context, string, string, []string) ([]*service.Balance, error) {
	return nil, nil
}
func (s stubStore) UpsertBalances(context.Context, string, []*service.Balance) error { return nil }
func (s stubStore) AdjustFrozen(context.Context, string, string, string, string) error {
	return nil
}
func (s stubStore) ListFundFlows(context.Context, string, service.FundFlowFilter, service.Page) ([]*service.FundFlow, int, error) {
	return nil, 0, nil
}
func (s stubStore) AppendFundFlows(context.Context, string, []*service.FundFlow) error { return nil }
func (s stubStore) CreateAPIKey(context.Context, string, *service.APIKey) error        { return nil }
func (s stubStore) DeleteAPIKey(context.Context, string, string) error                 { return nil }
func (s stubStore) ListAPIKeys(context.Context, string, string) ([]*service.APIKey, error) {
	return nil, nil
}
func (s stubStore) CreateChannel(context.Context, string, *service.TradeChannel) error { return nil }
func (s stubStore) UpdateChannel(context.Context, string, *service.TradeChannel) error {
	return nil
}
func (s stubStore) DeleteChannel(context.Context, string, string) error { return nil }
func (s stubStore) ListChannels(context.Context, string, service.ChannelFilter, service.Page) ([]*service.TradeChannel, int, error) {
	return nil, 0, nil
}
func (s stubStore) SaveOrder(context.Context, string, *service.Order) error { return nil }
func (s stubStore) UpsertOrders(context.Context, string, []*service.Order) error {
	return nil
}
func (s stubStore) UpdateOrder(context.Context, string, *service.Order) error { return nil }
func (s stubStore) GetOrder(context.Context, string, string, string) (*service.Order, error) {
	return nil, nil
}
func (s stubStore) ListOrders(context.Context, string, service.OrderFilter, service.Page) ([]*service.Order, int, error) {
	return nil, 0, nil
}
func (s stubStore) AppendTrades(context.Context, string, []*service.Trade) error { return nil }
func (s stubStore) ListTrades(context.Context, string, service.TradeFilter, service.Page) ([]*service.Trade, int, error) {
	return nil, 0, nil
}
func (s stubStore) UpsertPositions(context.Context, string, []*service.Position) error { return nil }
func (s stubStore) ReplacePositions(context.Context, string, string, string, []*service.Position) error {
	return nil
}
func (s stubStore) ListPositions(context.Context, string, string, string) ([]*service.Position, error) {
	return nil, nil
}
func (s stubStore) AppendOrderOperation(context.Context, string, *service.OrderOperation) error {
	return nil
}
func (s stubStore) UpdateOrderOperation(context.Context, string, *service.OrderOperation) error {
	return nil
}
func (s stubStore) GetSyncCursor(context.Context, string, string, service.SyncType, string) (*service.SyncCursor, error) {
	return nil, nil
}
func (s stubStore) UpsertSyncCursor(context.Context, string, *service.SyncCursor) error { return nil }
func (s stubStore) ListSyncCursors(context.Context, string, string, service.SyncType) ([]*service.SyncCursor, error) {
	return nil, nil
}

type stubExchangeAdapter struct{}

func (stubExchangeAdapter) Name() string { return "stub" }
func (stubExchangeAdapter) Ping(context.Context, legacy.Credential) (int64, error) {
	return 0, nil
}
func (stubExchangeAdapter) GetInstruments(context.Context, legacy.MarketType) ([]legacy.Instrument, error) {
	return []legacy.Instrument{{
		Symbol: "BTC-USDT", BaseCcy: "BTC", QuoteCcy: "USDT",
		TickSize: "0.01", LotSize: "0.001", MinQty: "0.001", MinNotional: "5",
	}}, nil
}
func (stubExchangeAdapter) GetAccountInfo(context.Context, legacy.Credential, legacy.MarketType) (*legacy.AccountInfo, error) {
	return nil, nil
}
func (stubExchangeAdapter) GetBalances(context.Context, legacy.Credential, legacy.MarketType, []string) ([]legacy.Balance, error) {
	return nil, nil
}
func (stubExchangeAdapter) GetTradeFee(context.Context, legacy.Credential, legacy.MarketType, string) (*legacy.FeeRate, error) {
	return nil, nil
}
func (stubExchangeAdapter) ListFundFlows(context.Context, legacy.Credential, *legacy.FundFlowQuery) ([]legacy.FundFlow, error) {
	return nil, nil
}
func (stubExchangeAdapter) Transfer(context.Context, legacy.Credential, *legacy.TransferReq) (*legacy.TransferResult, error) {
	return nil, nil
}
func (stubExchangeAdapter) ListConvertibleDustAssets(context.Context, legacy.Credential, *legacy.DustConvertibleReq) ([]legacy.DustConvertibleAsset, error) {
	return nil, nil
}
func (stubExchangeAdapter) ConvertDust(context.Context, legacy.Credential, *legacy.DustTransferReq) (*legacy.DustTransferResult, error) {
	return nil, nil
}
func (stubExchangeAdapter) PlaceOrder(context.Context, legacy.Credential, *legacy.PlaceOrderReq) (*legacy.OrderResult, error) {
	return &legacy.OrderResult{OrderID: "client-1", ClientOrderID: "client-1", ExchangeOrderID: "ex-1", Status: legacy.StatusSubmitted}, nil
}
func (stubExchangeAdapter) CancelOrder(context.Context, legacy.Credential, *legacy.CancelOrderReq) (*legacy.OrderResult, error) {
	return &legacy.OrderResult{OrderID: "client-1", ClientOrderID: "client-1", ExchangeOrderID: "ex-1", Status: legacy.StatusCanceled}, nil
}
func (stubExchangeAdapter) CancelAllOrders(context.Context, legacy.Credential, legacy.MarketType, string) (int, error) {
	return 0, nil
}
func (stubExchangeAdapter) AmendOrder(context.Context, legacy.Credential, *legacy.AmendOrderReq) (*legacy.OrderResult, error) {
	return nil, nil
}
func (stubExchangeAdapter) SetLeverage(context.Context, legacy.Credential, legacy.MarketType, string, string) error {
	return nil
}
func (stubExchangeAdapter) ClosePosition(context.Context, legacy.Credential, legacy.MarketType, string, string) error {
	return nil
}
func (stubExchangeAdapter) GetOrder(context.Context, legacy.Credential, *legacy.GetOrderReq) (*legacy.Order, error) {
	return &legacy.Order{ClientOrderID: "client-1", ExchangeOrderID: "ex-1", Status: legacy.StatusPartiallyFilled, FilledQty: "0.4"}, nil
}
func (stubExchangeAdapter) ListOpenOrders(context.Context, legacy.Credential, *legacy.ListOrdersReq) ([]legacy.Order, error) {
	return nil, nil
}
func (stubExchangeAdapter) ListOrders(context.Context, legacy.Credential, *legacy.ListOrdersReq) ([]legacy.Order, error) {
	return nil, nil
}
func (stubExchangeAdapter) ListTrades(context.Context, legacy.Credential, *legacy.ListTradesReq) ([]legacy.Trade, error) {
	return []legacy.Trade{{
		TradeID: "trade-1", ExchangeTradeID: "trade-1", OrderID: "ex-1",
		ClientOrderID: "client-1", Symbol: "BTC-USDT", Side: legacy.SideBuy,
		Price: "100", Quantity: "0.4", Fee: "0.01", FeeCurrency: "USDT",
	}}, nil
}
func (stubExchangeAdapter) ListPositions(context.Context, legacy.Credential, legacy.MarketType, string) ([]legacy.Position, error) {
	return nil, nil
}
func (stubExchangeAdapter) SubscribePrivate(_ context.Context, _ legacy.Credential, _ legacy.MarketType, handler legacy.StreamHandler) error {
	handler.OnTrade(&legacy.TradeEvent{Trade: legacy.Trade{
		TradeID: "trade-1", ExchangeTradeID: "trade-1", OrderID: "ex-1",
		ClientOrderID: "client-1", Symbol: "BTC-USDT", Side: legacy.SideSell,
		Price: "101", Quantity: "0.2", Fee: "0.02", FeeCurrency: "USDT",
	}})
	return nil
}

func TestPrivateHandler_NoOpCallbacks_ShouldNotPanic(t *testing.T) {
	h := &privateHandler{}
	assert.NotPanics(t, func() {
		h.OnOrderUpdate(&legacy.OrderEvent{})
		h.OnPositionUpdate(&legacy.PositionEvent{})
		h.OnBalanceUpdate(&legacy.BalanceEvent{})
		h.OnError(errors.New("stream"))
	})
}

func TestResolver_Resolve_SimulatedChannel_ShouldReturnError(t *testing.T) {
	resolver := Resolver{Store: stubStore{channel: &service.TradeChannel{
		ChannelID:   "ch-1",
		IsSimulated: true,
	}}}
	_, err := resolver.Resolve(context.Background(), "crypto", "ch-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "simulated channel is not implemented")
}

func TestResolver_Resolve_ValidChannel_ShouldReturnBoundAdapter(t *testing.T) {
	resolver := Resolver{
		Store: stubStore{
			channel: &service.TradeChannel{
				ChannelID:  "ch-1",
				Exchange:   "stub-exchange",
				MarketType: "spot",
				APIKeyID:   "key-1",
			},
			apiKey: &service.APIKey{
				APIKeyID:  "key-1",
				APIKey:    "ak",
				APISecret: "sk",
			},
		},
		Factory: func(name string) (legacy.ExchangeAdapter, error) {
			assert.Equal(t, "stub-exchange", name)
			return stubExchangeAdapter{}, nil
		},
	}

	adapter, err := resolver.Resolve(context.Background(), "crypto", "ch-1")
	require.NoError(t, err)
	require.NotNil(t, adapter)

	boundAdapter, ok := adapter.(*bound)
	require.True(t, ok)
	assert.Equal(t, "stub", boundAdapter.ExchangeName())
	assert.Equal(t, "spot", boundAdapter.MarketType())
}

func TestStatus_KnownStatuses_ShouldMapToTradeStatus(t *testing.T) {
	assert.Equal(t, "FILLED", status(legacy.StatusFilled))
	assert.Equal(t, "CANCELED", status(legacy.StatusCanceled))
	assert.Equal(t, "OPEN", status(legacy.StatusSubmitted))
}

func TestClassify_TimeoutError_ShouldMarkTransportUncertain(t *testing.T) {
	err := classify(errors.New("request timeout"))
	require.Error(t, err)
	assert.True(t, legacy.IsCategory(err, legacy.ErrorTransportUncertain))
}

func TestBound_OrderMethods_ShouldMapLegacyAdapterResults(t *testing.T) {
	b := &bound{adapter: stubExchangeAdapter{}, credential: legacy.Credential{APIKey: "ak"}, market: legacy.MarketSpot}

	placed, err := b.Place(context.Background(), legacy.PlaceRequest{
		ClientOrderID: "client-1", Symbol: "BTC-USDT", Side: "BUY", Type: "LIMIT",
		Quantity: shared.MustDecimal("1"), Price: shared.MustDecimal("100"),
	})
	require.NoError(t, err)
	assert.Equal(t, "OPEN", placed.Status)
	assert.Equal(t, "ex-1", placed.ExchangeOrderID)

	canceled, err := b.Cancel(context.Background(), "BTC-USDT", "client-1")
	require.NoError(t, err)
	assert.Equal(t, "CANCELED", canceled.Status)

	queried, err := b.QueryByClientOrderID(context.Background(), "BTC-USDT", "client-1")
	require.NoError(t, err)
	assert.Equal(t, "PARTIALLY_FILLED", queried.Status)
	assert.Equal(t, "0.4", queried.FilledQuantity.String())
}

func TestBound_RulesAndListFills_ShouldAttachInstrumentAssets(t *testing.T) {
	b := &bound{adapter: stubExchangeAdapter{}, market: legacy.MarketSpot}

	rules, err := b.Rules(context.Background(), "BTC-USDT")
	require.NoError(t, err)
	assert.Equal(t, "BTC", rules.BaseAsset)
	assert.Equal(t, "USDT", rules.QuoteAsset)

	fills, err := b.ListFills(context.Background(), "BTC-USDT", "ex-1")
	require.NoError(t, err)
	require.Len(t, fills, 1)
	assert.Equal(t, "BTC", fills[0].BaseAsset)
	assert.Equal(t, "USDT", fills[0].QuoteAsset)
	assert.Equal(t, "BUY", fills[0].Side)
}

func TestBound_SubscribePrivate_ShouldForwardTradeEvents(t *testing.T) {
	b := &bound{adapter: stubExchangeAdapter{}, market: legacy.MarketSpot}
	var got legacy.FillEvent

	err := b.SubscribePrivate(context.Background(), func(_ context.Context, event legacy.FillEvent) error {
		got = event
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, "trade-1", got.ExchangeTradeID)
	assert.Equal(t, "SELL", got.Side)
	assert.Equal(t, "0.2", got.Quantity.String())
}

var _ service.Store = stubStore{}
