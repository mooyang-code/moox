package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/instrument"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ledger"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestTradeKernelTimerConfig(t *testing.T) {
	raw, err := os.ReadFile("../../config/trpc_go.yaml")
	require.NoError(t, err)
	var cfg struct {
		Server struct {
			Services []struct {
				Name     string `yaml:"name"`
				Port     int    `yaml:"port"`
				Network  string `yaml:"network"`
				Protocol string `yaml:"protocol"`
				Timeout  int    `yaml:"timeout"`
			} `yaml:"service"`
		} `yaml:"server"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &cfg))
	want := map[string]struct {
		port    int
		network string
		timeout int
	}{
		tradeFillReconcileTimerService: {11213, "*/5 * * * * *?startAtOnce=1", 5000},
		tradeOrderRecoveryTimerService: {11214, "*/15 * * * * *?startAtOnce=1", 15000},
	}
	for _, service := range cfg.Server.Services {
		expected, ok := want[service.Name]
		if !ok {
			continue
		}
		assert.Equal(t, expected.port, service.Port)
		assert.Equal(t, expected.network, service.Network)
		assert.Equal(t, "timer", service.Protocol)
		assert.Equal(t, expected.timeout, service.Timeout)
		delete(want, service.Name)
	}
	assert.Empty(t, want)
}

func TestRecoverOrdersOnceHandlesEveryRecoverableOrderState(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	adapter := &recoveryStubAdapter{}
	engine := &command.Engine{Store: s, Adapter: adapter}
	seedRecoveryBalance(t, s)
	states := []string{"READY", "SUBMITTING", "SUBMIT_UNKNOWN", "CANCELING", "CANCEL_UNKNOWN"}
	for i, state := range states {
		orderID := "recover-" + state
		_, err := engine.Place(context.Background(), command.PlaceInput{
			SpaceID: "space", OrderID: orderID, ClientOrderID: "client-" + state,
			AccountID: "acct", ChannelID: "chan", Symbol: "BTC-USDT", MarketType: "spot",
			BaseAsset: "BTC", QuoteAsset: "USDT", Side: "BUY", Quantity: "1", Price: "10",
		})
		require.NoError(t, err, i)
		require.NoError(t, s.DBForTest().Exec("UPDATE t_trade_order_aggregates SET c_state=? WHERE c_space_id=? AND c_order_id=?", state, "space", orderID).Error)
	}
	require.NoError(t, recoverOrdersOnce(context.Background(), s, engine))
	assert.Equal(t, 1, adapter.placeCalls)
	assert.Equal(t, 4, adapter.queryCalls)
}

func TestRecoverOrdersOnceReturnsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, recoverOrdersOnce(ctx, &store.Store{}, &command.Engine{}), context.Canceled)
}

func TestRecoverOrdersOnceContinuesAfterOrderFailure(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	wantErr := errors.New("place failed")
	adapter := &recoveryStubAdapter{}
	engine := &command.Engine{Store: s, Adapter: adapter}
	seedRecoveryBalance(t, s)
	for _, id := range []string{"recover-a", "recover-b"} {
		_, err := engine.Place(context.Background(), command.PlaceInput{
			SpaceID: "space", OrderID: id, ClientOrderID: "client-" + id,
			AccountID: "acct", ChannelID: "chan", Symbol: "BTC-USDT", MarketType: "spot",
			BaseAsset: "BTC", QuoteAsset: "USDT", Side: "BUY", Quantity: "1", Price: "10",
		})
		require.NoError(t, err)
		require.NoError(t, s.DBForTest().Exec("UPDATE t_trade_order_aggregates SET c_state='READY' WHERE c_space_id=? AND c_order_id=?", "space", id).Error)
	}
	orders, err := s.ListRecoverableOrders(context.Background(), 100)
	require.NoError(t, err)
	require.Len(t, orders, 2)
	resolver := &failingRecoveryResolver{err: wantErr}
	engine.Resolver = resolver
	err = recoverOrdersOnce(context.Background(), s, engine)
	require.ErrorIs(t, err, wantErr)
	assert.Equal(t, 2, resolver.calls)
}

type recoveryStubAdapter struct {
	placeCalls int
	queryCalls int
}

func (a *recoveryStubAdapter) Place(_ context.Context, req exchange.PlaceRequest) (exchange.ExchangeOrderResult, error) {
	a.placeCalls++
	return exchange.ExchangeOrderResult{ExchangeOrderID: "exchange-" + req.ClientOrderID, Status: "OPEN"}, nil
}
func (*recoveryStubAdapter) Cancel(context.Context, string, string) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{Status: "CANCELED"}, nil
}
func (a *recoveryStubAdapter) QueryByClientOrderID(_ context.Context, _ string, clientOrderID string) (exchange.ExchangeOrderResult, error) {
	a.queryCalls++
	return exchange.ExchangeOrderResult{ExchangeOrderID: "exchange-" + clientOrderID, Status: "OPEN"}, nil
}
func (*recoveryStubAdapter) Rules(context.Context, string) (instrument.Rules, error) {
	return instrument.Rules{BaseAsset: "BTC", QuoteAsset: "USDT"}, nil
}
func (*recoveryStubAdapter) ListFills(context.Context, string, string) ([]exchange.FillEvent, error) {
	return nil, nil
}
func (*recoveryStubAdapter) SubscribePrivate(context.Context, exchange.PrivateEventHandler) error {
	return nil
}

type failingRecoveryResolver struct {
	calls int
	err   error
}

func (r *failingRecoveryResolver) Resolve(context.Context, string, string) (exchange.TradingAdapter, error) {
	r.calls++
	return nil, r.err
}

func seedRecoveryBalance(t *testing.T, s *store.Store) {
	t.Helper()
	require.NoError(t, s.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.PostLedger("space", ledger.Transaction{
			ID: shared.LedgerTransactionID("recovery-seed"), BizType: "seed", RefType: "test", RefID: "recovery",
			Entries: []ledger.Entry{
				{AccountID: "clearing", Asset: "USDT", Bucket: "clearing", Amount: shared.MustDecimal("1000").Neg()},
				{AccountID: "acct", Asset: "USDT", Bucket: "available", Amount: shared.MustDecimal("1000")},
			},
		})
	}))
}
