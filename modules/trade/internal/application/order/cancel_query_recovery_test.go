package order

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/stretchr/testify/require"
)

type recoveryQueryAdapter struct {
	*adapterStub
	wait bool
}

type recoveryFillSync struct {
	reducer *consumer.Reducer
	fill    exchange.Fill
}

func (s recoveryFillSync) SyncAccount(ctx context.Context, id string) error {
	_, err := s.reducer.ApplyFill(ctx, s.fill, consumer.Source{SpaceID: "space-1", TradingAccountID: id, Kind: consumer.OriginRESTSnapshot})
	return err
}

func TestRecoverCancelTerminalQueryWaitsForRealFillReducer(t *testing.T) {
	s, db, a := newTestService(t)
	require.NoError(t, db.DBForTest().Exec("UPDATE t_trade_instruments SET c_instrument_id='BTC-USDT',c_environment='TESTNET'").Error)
	spec := testSpec(s.now())
	pending, err := s.Place(context.Background(), "space-1", spec)
	require.NoError(t, err)
	_, err = s.Submit(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	setStoredOrderState(t, db, string(pending.ID), orderdomain.CancelUnknown, s.now())
	a.getResult = exchange.Order{ExchangeOrderID: "exchange-order-1", Status: exchange.OrderStatusFilled}
	s.Syncer = recoveryFillSync{reducer: &consumer.Reducer{Store: db}, fill: exchange.Fill{ExchangeTradeID: "terminal-fill", ExchangeOrderID: "exchange-order-1", ClientOrderID: spec.ClientOrderID, ExchangeSymbol: "BTC-USDT", Side: exchange.SideBuy, Quantity: spec.Quantity, Price: shared.MustDecimal("100"), Fee: shared.Zero(), FeeAsset: "USDT", TradedAt: s.now()}}
	result, err := s.RecoverCancel(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Filled, result.State)
	require.Zero(t, a.cancelCalls)
	require.Equal(t, 1, a.getCalls)
	stored, err := db.GetOrder(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	require.Equal(t, "0", stored.RemainingReservedQuantity)
}

func (a recoveryQueryAdapter) Adapter(string) (execution.ExecutionAdapter, error) { return a, nil }
func (a recoveryQueryAdapter) GetOrder(ctx context.Context, symbol shared.ExchangeSymbol, id string) (exchange.Order, error) {
	if a.wait {
		a.getCalls++
		<-ctx.Done()
		return exchange.Order{}, ctx.Err()
	}
	return a.adapterStub.GetOrder(ctx, symbol, id)
}

func TestRecoverCancelQueriesBeforeAnyMutation(t *testing.T) {
	for _, path := range []string{"timeout", "filled", "open"} {
		t.Run(path, func(t *testing.T) {
			s, db, a := newTestService(t)
			pending, err := s.Place(context.Background(), "space-1", testSpec(s.now()))
			require.NoError(t, err)
			_, err = s.Submit(context.Background(), "space-1", string(pending.ID))
			require.NoError(t, err)
			setStoredOrderState(t, db, string(pending.ID), orderdomain.CancelUnknown, s.now())
			s.Syncer = &syncerStub{}
			a.getResult = exchange.Order{ExchangeOrderID: "exchange-1", Status: exchange.OrderStatusOpen}
			if path == "filled" {
				a.getResult.Status = exchange.OrderStatusFilled
			}
			s.Adapters = recoveryQueryAdapter{adapterStub: a, wait: path == "timeout"}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			_, err = s.RecoverCancel(ctx, "space-1", string(pending.ID))
			require.Equal(t, 1, a.getCalls)
			if path == "open" {
				require.NoError(t, err)
				require.Equal(t, 1, a.cancelCalls)
			} else {
				require.Zero(t, a.cancelCalls)
			}
			if path == "timeout" {
				require.ErrorIs(t, err, context.DeadlineExceeded)
			}
			stored, readErr := db.GetOrder(context.Background(), "space-1", string(pending.ID))
			require.NoError(t, readErr)
			require.Equal(t, "CANCEL_UNKNOWN", stored.State)
			require.NotEqual(t, "0", stored.RemainingReservedQuantity)
		})
	}
}
