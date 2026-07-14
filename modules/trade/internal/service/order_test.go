package service

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	_ "github.com/mooyang-code/moox/modules/trade/internal/exchange/all"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testServiceStore struct {
	Store
	account *Account
	channel *TradeChannel
	apiKey  *APIKey
}

func (s *testServiceStore) GetAccount(_ context.Context, _, accountID string) (*Account, error) {
	if s.account == nil || s.account.AccountID != accountID {
		return nil, ErrNotFound
	}
	return s.account, nil
}
func (s *testServiceStore) GetChannel(_ context.Context, _, channelID string) (*TradeChannel, error) {
	if s.channel == nil || s.channel.ChannelID != channelID {
		return nil, ErrNotFound
	}
	return s.channel, nil
}
func (s *testServiceStore) GetAPIKey(_ context.Context, _, apiKeyID string) (*APIKey, error) {
	if s.apiKey == nil || s.apiKey.APIKeyID != apiKeyID {
		return nil, ErrNotFound
	}
	return s.apiKey, nil
}

type testExchangeAdapter struct {
	exchange.ExchangeAdapter
	instruments []exchange.Instrument
	convertible []exchange.DustConvertibleAsset
	dust        *exchange.DustTransferResult
	gotAssets   []string
}

func (*testExchangeAdapter) Ping(context.Context, exchange.Credential) (int64, error) { return 12, nil }
func (a *testExchangeAdapter) GetInstruments(context.Context, exchange.MarketType) ([]exchange.Instrument, error) {
	return a.instruments, nil
}
func (a *testExchangeAdapter) SetLeverage(context.Context, exchange.Credential, exchange.MarketType, string, string) error {
	return nil
}
func (a *testExchangeAdapter) ListConvertibleDustAssets(context.Context, exchange.Credential, *exchange.DustConvertibleReq) ([]exchange.DustConvertibleAsset, error) {
	return a.convertible, nil
}
func (a *testExchangeAdapter) ConvertDust(_ context.Context, _ exchange.Credential, req *exchange.DustTransferReq) (*exchange.DustTransferResult, error) {
	a.gotAssets = append([]string(nil), req.Assets...)
	return a.dust, nil
}

func newTestOrderService(adapter exchange.ExchangeAdapter) *OrderService {
	store := &testServiceStore{
		account: &Account{AccountID: "acc_1", ChannelID: "ch_1"},
		channel: &TradeChannel{ChannelID: "ch_1", Exchange: "binance", MarketType: "spot", AccountID: "acc_1", APIKeyID: "ak_1"},
		apiKey:  &APIKey{APIKeyID: "ak_1", APIKey: "key", APISecret: "secret"},
	}
	return New("trade", WithStore(store), WithExchangeFactory(func(string) (exchange.ExchangeAdapter, error) { return adapter, nil })).Order
}

func TestOrderService_NewAdapter(t *testing.T) {
	svc := New("trade", WithExchangeFactory(exchange.New))
	adapter, err := svc.Order.NewAdapter("binance")
	require.NoError(t, err)
	assert.Equal(t, "binance", adapter.Name())
	svc = New("trade", WithExchangeFactory(func(string) (exchange.ExchangeAdapter, error) { return nil, errors.New("unknown") }))
	_, err = svc.Order.NewAdapter("missing")
	assert.Error(t, err)
}

func TestOrderService_ChannelUtilities(t *testing.T) {
	adapter := &testExchangeAdapter{instruments: []exchange.Instrument{{Symbol: "ETHUSDT"}}}
	svc := newTestOrderService(adapter)
	ok, latency, err := svc.TestChannel(context.Background(), "crypto", "ch_1")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, int64(12), latency)
	got, err := svc.ListInstruments(context.Background(), "crypto", "ch_1", exchange.MarketSpot)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "ETHUSDT", got[0].Symbol)
	assert.NoError(t, svc.SetLeverage(context.Background(), "crypto", "ch_1", "BTCUSDT", "10"))
}

func TestOrderService_ConvertDustFiltersUnsupportedAssets(t *testing.T) {
	adapter := &testExchangeAdapter{
		convertible: []exchange.DustConvertibleAsset{{Asset: "USDT"}},
		dust:        &exchange.DustTransferResult{},
	}
	result, err := newTestOrderService(adapter).ConvertDust(context.Background(), "crypto", "ch_1", []string{"usdt", "tiny"})
	require.NoError(t, err)
	assert.Equal(t, []string{"USDT"}, adapter.gotAssets)
	require.Len(t, result.Skipped, 1)
	assert.Equal(t, "TINY", result.Skipped[0].Asset)
}

func TestService_Health(t *testing.T) {
	svc := New("")
	assert.Equal(t, "trade", svc.Health().Module)
	assert.True(t, svc.Health().Ready)
}
