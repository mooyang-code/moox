package target

import (
	"context"
	"testing"
	"time"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

type expiryPriceSource struct{ t *testing.T }

type expiryAdvancingPriceSource struct{ fixture *targetFixture }

func (s expiryAdvancingPriceSource) LatestPrice(context.Context, string, string) (Quote, error) {
	s.fixture.now = time.UnixMilli(2000)
	return Quote{Price: shared.MustDecimal("100"), UpdatedAt: s.fixture.now}, nil
}

func (s expiryPriceSource) LatestPrice(context.Context, string, string) (Quote, error) {
	s.t.Fatal("expired or future target must not fetch market data")
	return Quote{}, nil
}

func modernExpiryTarget(t *testing.T, f *targetFixture) {
	t.Helper()
	f.target(t, nil)
	require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_logical_accounts
		SET c_owner_instance_id = 'instance-1', c_owner_session_id = 'session-1'
		WHERE c_logical_account_id = 'logical-1'`).Error)
	require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_logical_account_targets
		SET c_instance_id = 'instance-1', c_session_id = 'session-1', c_strategy_id = 'strategy-1',
		c_bar_end_time = 1000, c_effective_at = 1000, c_valid_until = 2000
		WHERE c_logical_account_id = 'logical-1'`).Error)
}

func TestExpiredTargetIsPersistedWithoutTouchingOpenOrders(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	modernExpiryTarget(t, f)
	f.position(t, "account-a", "BTCUSDT", "1")
	limit := "100"
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "open-child", TradingAccountID: "account-a",
		ClientOrderID: "open-child", ExchangeOrderID: "exchange-child", ExchangeSymbol: "BTCUSDT",
		OrderType: "LIMIT", LimitPrice: &limit, Side: "BUY", PositionSide: "NET", Quantity: "1", ReferencePrice: "100",
		OwnerType: "TARGET", OwnerID: "target-current", RunnerID: "instance-1",
		LogicalAccountID: "logical-1", State: "OPEN", Version: 1,
		ReservedAsset: "USDT", ReservedQuantity: "20", RemainingReservedQuantity: "20",
	})
	e := f.executor()
	e.Prices = expiryPriceSource{t}
	result, err := e.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "EXPIRED", result.Status)
	target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "EXPIRED", target.Status)
	open, err := f.store.GetOrder(context.Background(), "space-1", "open-child")
	require.NoError(t, err)
	require.Equal(t, "OPEN", open.State)
	require.Equal(t, "20", open.RemainingReservedQuantity)
	require.Empty(t, f.orders.specs)
	require.Empty(t, f.orders.submitted)
	require.Empty(t, f.orders.canceled)
	require.Empty(t, f.orders.discarded)
	require.Empty(t, f.orders.resolved)
}

func TestFutureTargetRemainsPending(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	modernExpiryTarget(t, f)
	e := f.executor()
	e.Now = func() time.Time { return time.UnixMilli(999) }
	e.Prices = expiryPriceSource{t}
	result, err := e.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusPaused, result.Status)
	target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusPending, target.Status)
}

func TestExpiryCASMissReturnsSupersededWithoutTouchingReplacement(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	modernExpiryTarget(t, f)
	e := f.executor()
	e.Prices = expiryPriceSource{t}
	e.Now = func() time.Time {
		require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_logical_account_targets
			SET c_target_id = 'new-target', c_bar_end_time = 2000, c_effective_at = 2000, c_valid_until = 3000,
			c_status = 'PENDING' WHERE c_logical_account_id = 'logical-1'`).Error)
		return time.UnixMilli(2000)
	}
	result, err := e.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "SUPERSEDED", result.Status)
	target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "new-target", target.TargetID)
	require.Equal(t, StatusPending, target.Status)
}

func TestExpiryDuringQuotePersistsTerminalStateWithoutPlacingOrder(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	modernExpiryTarget(t, f)
	f.position(t, "account-a", "BTCUSDT", "1")
	f.now = time.UnixMilli(1500)
	e := f.executor()
	e.Prices = expiryAdvancingPriceSource{f}
	result, err := e.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusExpired, result.Status)
	target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusExpired, target.Status)
	require.Empty(t, f.orders.specs)
	require.Empty(t, f.orders.submitted)
	require.Empty(t, f.orders.canceled)
}

func TestExpiryCASChecksSessionAndBarIdentity(t *testing.T) {
	for name, replacement := range map[string]string{
		"session": "c_session_id = 'session-2'",
		"bar":     "c_bar_end_time = 2000, c_effective_at = 2000, c_valid_until = 3000",
	} {
		t.Run(name, func(t *testing.T) {
			f := newTargetFixture(t, exchange.MarketTypeSwap)
			modernExpiryTarget(t, f)
			e := f.executor()
			e.Prices = expiryPriceSource{t}
			e.Now = func() time.Time {
				require.NoError(t, f.store.DBForTest().Exec("UPDATE t_logical_account_targets SET "+replacement).Error)
				return time.UnixMilli(2000)
			}
			result, err := e.Converge(context.Background(), "space-1", "logical-1")
			require.NoError(t, err)
			require.Equal(t, StatusSuperseded, result.Status)
			target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
			require.NoError(t, err)
			require.Equal(t, StatusPending, target.Status)
		})
	}
}

func TestExpiryBetweenReadAndExistingOrderActionHasNoSideEffects(t *testing.T) {
	for _, owner := range []string{"TARGET", "EXTERNAL"} {
		t.Run(owner, func(t *testing.T) {
			f := newTargetFixture(t, exchange.MarketTypeSwap)
			modernExpiryTarget(t, f)
			runner := ""
			if owner == "TARGET" {
				runner = "instance-1"
			}
			f.order(t, store.OrderRecord{
				SpaceID: "space-1", TradingAccountID: "account-a", OrderID: "old-order",
				ClientOrderID: "old-order", ExchangeOrderID: "old-order", ExchangeSymbol: "BTCUSDT",
				OrderType: "MARKET", Side: "BUY", Quantity: "1", ReferencePrice: "100",
				PositionSide: "NET", OwnerType: owner, OwnerID: "older-target",
				RunnerID: runner, LogicalAccountID: "logical-1", State: "OPEN", Version: 1,
			})
			e := f.executor()
			calls := 0
			e.Now = func() time.Time {
				calls++
				if calls == 1 {
					return time.UnixMilli(1500)
				}
				return time.UnixMilli(2000)
			}
			result, err := e.Converge(context.Background(), "space-1", "logical-1")
			require.NoError(t, err)
			require.Equal(t, StatusExpired, result.Status)
			require.Empty(t, f.orders.canceled)
			require.Empty(t, f.orders.discarded)
			logical, err := f.store.GetLogicalAccount(context.Background(), "space-1", "logical-1")
			require.NoError(t, err)
			require.Equal(t, "ACTIVE", logical.AutomationState)
		})
	}
}

func TestOrderLayerExpiryIsPersistedInSameConvergence(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	modernExpiryTarget(t, f)
	f.position(t, "account-a", "BTCUSDT", "1")
	f.now = time.UnixMilli(1500)
	f.orders.submitErrors = map[string]error{"child-account-a": orderapp.ErrTargetExpired}
	result, err := f.executor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusExpired, result.Status)
	current, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusExpired, current.Status)
	require.Empty(t, f.orders.canceled)
	require.Equal(t, []string{"child-account-a"}, f.orders.discarded)
}

func TestExpiredTargetDiscardsPersistedPendingChildren(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	modernExpiryTarget(t, f)
	f.orders.store = f.store
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "pending-child", TradingAccountID: "account-a",
		ClientOrderID: "pending-child", ExchangeOrderID: "", ExchangeSymbol: "BTCUSDT",
		OrderType: "MARKET", Side: "BUY", PositionSide: "NET",
		Quantity: "1", ReferencePrice: "100", OwnerType: "TARGET", OwnerID: "target-current",
		RunnerID: "instance-1", LogicalAccountID: "logical-1", State: "PENDING", Version: 1,
		ReservedAsset: "USDT", ReservedQuantity: "20", RemainingReservedQuantity: "20",
	})

	result, err := f.executor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusExpired, result.Status)
	require.Equal(t, []string{"pending-child"}, f.orders.discarded)
	current, err := f.store.GetOrder(context.Background(), "space-1", "pending-child")
	require.NoError(t, err)
	require.Equal(t, "CANCELED", current.State)
	require.Equal(t, "0", current.RemainingReservedQuantity)
}

func TestAlreadyExpiredTargetRepairsPersistedPendingChildren(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	modernExpiryTarget(t, f)
	f.orders.store = f.store
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "pending-child", TradingAccountID: "account-a",
		ClientOrderID: "pending-child", ExchangeOrderID: "", ExchangeSymbol: "BTCUSDT",
		OrderType: "MARKET", Side: "BUY", PositionSide: "NET",
		Quantity: "1", ReferencePrice: "100", OwnerType: "TARGET", OwnerID: "target-current",
		RunnerID: "instance-1", LogicalAccountID: "logical-1", State: "PENDING", Version: 1,
		ReservedAsset: "USDT", ReservedQuantity: "20", RemainingReservedQuantity: "20",
	})
	require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_logical_account_targets
		SET c_status = 'EXPIRED' WHERE c_space_id = 'space-1' AND c_logical_account_id = 'logical-1'`).Error)

	result, err := f.executor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusExpired, result.Status)
	require.Equal(t, []string{"pending-child"}, f.orders.discarded)
	current, err := f.store.GetOrder(context.Background(), "space-1", "pending-child")
	require.NoError(t, err)
	require.Equal(t, "CANCELED", current.State)
	require.Equal(t, "0", current.RemainingReservedQuantity)
}
