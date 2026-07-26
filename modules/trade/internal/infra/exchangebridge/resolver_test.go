package exchangebridge

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
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
func (stubExchangeAdapter) Ping(context.Context, exchange.Credential) (int64, error) {
	return 0, nil
}
func (stubExchangeAdapter) GetInstruments(context.Context, exchange.MarketType) ([]exchange.Instrument, error) {
	return []exchange.Instrument{{
		Symbol: "BTC-USDT", BaseCcy: "BTC", QuoteCcy: "USDT",
		TickSize: "0.01", LotSize: "0.001", MinQty: "0.001", MinNotional: "5", LastPrice: "10",
	}}, nil
}
func (stubExchangeAdapter) GetAccountInfo(context.Context, exchange.Credential, exchange.MarketType) (*exchange.AccountInfo, error) {
	return nil, nil
}
func (stubExchangeAdapter) GetBalances(context.Context, exchange.Credential, exchange.MarketType, []string) ([]exchange.Balance, error) {
	return nil, nil
}
func (stubExchangeAdapter) GetTradeFee(context.Context, exchange.Credential, exchange.MarketType, string) (*exchange.FeeRate, error) {
	return nil, nil
}
func (stubExchangeAdapter) ListFundFlows(context.Context, exchange.Credential, *exchange.FundFlowQuery) ([]exchange.FundFlow, error) {
	return nil, nil
}
func (stubExchangeAdapter) Transfer(context.Context, exchange.Credential, *exchange.TransferReq) (*exchange.TransferResult, error) {
	return nil, nil
}
func (stubExchangeAdapter) ListConvertibleDustAssets(context.Context, exchange.Credential, *exchange.DustConvertibleReq) ([]exchange.DustConvertibleAsset, error) {
	return nil, nil
}
func (stubExchangeAdapter) ConvertDust(context.Context, exchange.Credential, *exchange.DustTransferReq) (*exchange.DustTransferResult, error) {
	return nil, nil
}
func (stubExchangeAdapter) PlaceOrder(context.Context, exchange.Credential, *exchange.PlaceOrderReq) (*exchange.OrderResult, error) {
	return &exchange.OrderResult{OrderID: "client-1", ClientOrderID: "client-1", ExchangeOrderID: "ex-1", Status: exchange.StatusSubmitted}, nil
}
func (stubExchangeAdapter) CancelOrder(context.Context, exchange.Credential, *exchange.CancelOrderReq) (*exchange.OrderResult, error) {
	return &exchange.OrderResult{OrderID: "client-1", ClientOrderID: "client-1", ExchangeOrderID: "ex-1", Status: exchange.StatusCanceled}, nil
}
func (stubExchangeAdapter) CancelAllOrders(context.Context, exchange.Credential, exchange.MarketType, string) (int, error) {
	return 0, nil
}
func (stubExchangeAdapter) AmendOrder(context.Context, exchange.Credential, *exchange.AmendOrderReq) (*exchange.OrderResult, error) {
	return nil, nil
}
func (stubExchangeAdapter) SetLeverage(context.Context, exchange.Credential, exchange.MarketType, string, string) error {
	return nil
}
func (stubExchangeAdapter) ClosePosition(context.Context, exchange.Credential, exchange.MarketType, string, string) error {
	return nil
}
func (stubExchangeAdapter) GetOrder(context.Context, exchange.Credential, *exchange.GetOrderReq) (*exchange.Order, error) {
	return &exchange.Order{ClientOrderID: "client-1", ExchangeOrderID: "ex-1", Status: exchange.StatusPartiallyFilled, FilledQty: "0.4"}, nil
}
func (stubExchangeAdapter) ListOpenOrders(context.Context, exchange.Credential, *exchange.ListOrdersReq) ([]exchange.Order, error) {
	return nil, nil
}
func (stubExchangeAdapter) ListOrders(context.Context, exchange.Credential, *exchange.ListOrdersReq) ([]exchange.Order, error) {
	return nil, nil
}
func (stubExchangeAdapter) ListTrades(context.Context, exchange.Credential, *exchange.ListTradesReq) ([]exchange.Trade, error) {
	return []exchange.Trade{{
		TradeID: "trade-1", ExchangeTradeID: "trade-1", OrderID: "ex-1",
		ClientOrderID: "client-1", Symbol: "BTC-USDT", Side: exchange.SideBuy,
		Price: "100", Quantity: "0.4", Fee: "0.01", FeeCurrency: "USDT",
	}}, nil
}
func (stubExchangeAdapter) ListPositions(context.Context, exchange.Credential, exchange.MarketType, string) ([]exchange.Position, error) {
	return nil, nil
}
func (stubExchangeAdapter) SubscribePrivate(_ context.Context, _ exchange.Credential, _ exchange.MarketType, handler exchange.StreamHandler) error {
	handler.OnTrade(&exchange.TradeEvent{Trade: exchange.Trade{
		TradeID: "trade-1", ExchangeTradeID: "trade-1", OrderID: "ex-1",
		ClientOrderID: "client-1", Symbol: "BTC-USDT", Side: exchange.SideSell,
		Price: "101", Quantity: "0.2", Fee: "0.02", FeeCurrency: "USDT",
	}})
	return nil
}

func TestPrivateHandler_NoOpCallbacks_ShouldNotPanic(t *testing.T) {
	h := &privateHandler{}
	assert.NotPanics(t, func() {
		h.OnOrderUpdate(&exchange.OrderEvent{})
		h.OnPositionUpdate(&exchange.PositionEvent{})
		h.OnBalanceUpdate(&exchange.BalanceEvent{})
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
		Factory: func(name string) (exchange.ExchangeAdapter, error) {
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
	assert.Equal(t, "FILLED", status(exchange.StatusFilled))
	assert.Equal(t, "CANCELED", status(exchange.StatusCanceled))
	assert.Equal(t, "OPEN", status(exchange.StatusSubmitted))
}

func TestClassify_TimeoutError_ShouldMarkTransportUncertain(t *testing.T) {
	err := classify(errors.New("request timeout"))
	require.Error(t, err)
	assert.True(t, exchange.IsCategory(err, exchange.ErrorTransportUncertain))
}

func TestBound_OrderMethods_ShouldMapLegacyAdapterResults(t *testing.T) {
	b := &bound{adapter: stubExchangeAdapter{}, credential: exchange.Credential{APIKey: "ak"}, market: exchange.MarketSpot}

	placed, err := b.Place(context.Background(), exchange.PlaceRequest{
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
	b := &bound{adapter: stubExchangeAdapter{}, market: exchange.MarketSpot}

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
	b := &bound{adapter: stubExchangeAdapter{}, market: exchange.MarketSpot}
	var got exchange.FillEvent

	err := b.SubscribePrivate(context.Background(), func(_ context.Context, event exchange.FillEvent) error {
		got = event
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, "trade-1", got.ExchangeTradeID)
	assert.Equal(t, "SELL", got.Side)
	assert.Equal(t, "0.2", got.Quantity.String())
}

var _ service.Store = stubStore{}
