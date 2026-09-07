package target

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func TestPinnedCutoverNewSessionTargetDoesNotCancelExistingOrder(t *testing.T) {
	ctx := context.Background()
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "1"}})
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "old-open", TradingAccountID: "account-a", ClientOrderID: "old-open",
		Exchange: "BINANCE", MarketType: "SWAP", ExchangeSymbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY",
		PositionSide: "NET", Quantity: "1", ReferencePrice: "100", ReferencePriceAt: 2000,
		OwnerType: "TARGET", OwnerID: "target-current", LogicalAccountID: "logical-1", RunnerID: "runner-1", State: "OPEN", Version: 1,
	})
	require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_logical_account_targets SET c_targets_json=?`,
		`[{"instrument_id":"BTC-USDT-SWAP","quantity":"1","trading_account_id":"account-a","exchange_symbol":"BTCUSDT"}]`).Error)
	require.NoError(t, f.store.Close())
	reopened, err := store.Open(f.path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	f.store = reopened
	f.now = time.Now().UTC()
	logical, err := reopened.GetLogicalAccount(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "PAUSED", logical.AutomationState)
	require.NoError(t, reopened.Transaction(ctx, func(tx *store.Tx) error {
		_, _, err := tx.ClaimLogicalAccountSession("space-1", "logical-1", "new-instance", "new-session", logical.AuthFence)
		return err
	}))
	_, accepted, err := reopened.AcceptLogicalAccountTarget(ctx, store.LogicalAccountTargetRecord{
		SpaceID: "space-1", LogicalAccountID: "logical-1", TargetID: "new-target", InstanceID: "new-instance", SessionID: "new-session", StrategyID: "strategy-1",
		BarEndTime: f.now.UnixMilli(), EffectiveAt: f.now.UnixMilli(), ValidUntil: f.now.Add(time.Hour).UnixMilli(), AcceptedAt: f.now.UnixMilli(),
		Targets: []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "2"}}, Status: StatusPending,
	})
	require.NoError(t, err)
	require.True(t, accepted)
	result, err := f.executor().Converge(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusPaused, result.Status)
	require.Empty(t, result.Action)
	require.Empty(t, f.orders.canceled, "new session/target is not explicit authorization to cancel pre-upgrade orders")
	require.Empty(t, f.orders.specs)
	old, err := reopened.GetOrder(ctx, "space-1", "old-open")
	require.NoError(t, err)
	require.Equal(t, "OPEN", old.State)
	// AccountSync and FactsObserver use this same setter when discovering an
	// external order. That automatic pause must not release the cutover gate.
	require.NoError(t, reopened.Transaction(ctx, func(tx *store.Tx) error {
		return tx.SetLogicalAccountAutomation("space-1", "logical-1", "PAUSED", "external order discovered")
	}))
	result, err = f.executor().Converge(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Empty(t, result.Action)
	require.Empty(t, f.orders.canceled)
	logical, err = reopened.GetLogicalAccount(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, store.TargetPinMigrationPauseReason, logical.PauseReason)
	require.NoError(t, reopened.Transaction(ctx, func(tx *store.Tx) error {
		return tx.SetLogicalAccountAutomation("space-1", "logical-1", "ACTIVE", "")
	}))
	logical, err = reopened.GetLogicalAccount(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Empty(t, logical.PauseReason)
}
