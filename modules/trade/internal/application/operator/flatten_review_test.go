package operator

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// spotHolding 给 account-a 设好现货余额，并用一笔已成交的买单让 BTC 成为受管资产。
func spotHolding(t *testing.T, fixture *operatorFixture, total, available string) {
	t.Helper()
	account, err := fixture.store.GetTradingAccountByID(context.Background(), "account-a")
	require.NoError(t, err)
	account.Snapshot.Balances = []store.AssetBalance{
		{Wallet: "UNIFIED", Asset: "USDT", Available: "500", Total: "500"},
		{Wallet: "UNIFIED", Asset: "BTC", Available: available, Total: total},
	}
	require.NoError(t, fixture.store.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.UpdateTradingAccountSync(account.SpaceID, account.TradingAccountID, store.TradingAccountSyncState{
			Ready: true, Snapshot: account.Snapshot,
			SnapshotSourceTime: fixture.now.UnixMilli(), LastSyncAt: fixture.now.UnixMilli(),
		}); err != nil {
			return err
		}
		return tx.CreateOrder(store.OrderRecord{
			SpaceID: "space-1", TradingAccountID: "account-a", OrderID: "bought", ClientOrderID: "bought",
			Exchange: account.Exchange, MarketType: "SPOT", ExchangeSymbol: "BTCUSDT", InstrumentID: "BTC-USDT-SPOT",
			OrderType: "MARKET", Side: "BUY", Quantity: total, FilledQuantity: total, ReferencePrice: "100",
			State: "FILLED", OwnerType: "OPERATOR", OwnerID: "action-0", LogicalAccountID: "logical-1", Version: 1,
		})
	}))
}

func flattenOnce(t *testing.T, service *Service, actionID string) (FlattenResult, error) {
	t.Helper()
	return service.FlattenLogicalAccount(context.Background(), FlattenCommand{
		SpaceID: "space-1", ActionID: actionID, LogicalAccountID: "logical-1", Reason: "close risk",
	})
}

func accountResult(t *testing.T, result FlattenResult, accountID string) FlattenAccountResult {
	t.Helper()
	for _, account := range result.Accounts {
		if account.TradingAccountID == accountID {
			return account
		}
	}
	t.Fatalf("no result for %s: %+v", accountID, result.Accounts)
	return FlattenAccountResult{}
}

func TestFlattenSpotDustCompletesWithoutRetry(t *testing.T) {
	for name, total := range map[string]string{
		"below minimum quantity": "0.0005",
		"below minimum notional": "0.02", // 0.02 × 100 = 2，低于最小名义金额 5
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newOperatorFixture(t, exchange.MarketTypeSpot)
			spotHolding(t, fixture, total, total)
			service := fixture.service()
			service.FlattenMaxAttempts = 0 // 不限次数：零头不能让清仓空转

			result, err := flattenOnce(t, service, "flatten-1")

			require.NoError(t, err)
			require.Equal(t, "COMPLETED", result.Action.Status)
			account := accountResult(t, result, "account-a")
			require.Empty(t, account.Remaining)
			require.Len(t, account.Dust, 1)
			require.Equal(t, "BTC", account.Dust[0].Asset)
			require.Empty(t, fixture.orders.specs)
		})
	}
}

func TestFlattenSpotLockedBalanceKeepsRetrying(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSpot)
	spotHolding(t, fixture, "1", "0") // 总量不是零头，可用余额被挂单占着
	service := fixture.service()
	service.FlattenMaxAttempts = 2

	result, err := flattenOnce(t, service, "flatten-1")

	require.NoError(t, err)
	require.Equal(t, "RUNNING", result.Action.Status)
	account := accountResult(t, result, "account-a")
	require.Len(t, account.Remaining, 1)
	require.Empty(t, account.Dust)
}

func TestFlattenSwapQuantityRuleEndsPartialImmediately(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)
	fixture.position(t, "account-a", "BTCUSDT", "0.0005") // 低于最小数量 0.001
	service := fixture.service()
	service.FlattenMaxAttempts = 0

	started := time.Now()
	result, err := flattenOnce(t, service, "flatten-1")

	require.NoError(t, err)
	require.Less(t, time.Since(started), 2*time.Second, "final reasons must not spin until the budget ends")
	require.Equal(t, "PARTIAL", result.Action.Status)
	require.Equal(t, 2, fixture.syncer.callsFor("account-a"))
}

func endedChild(fixture *operatorFixture, state, filled string) store.OrderRecord {
	quantity := shared.MustDecimal("2")
	return store.OrderRecord{
		SpaceID: "space-1", OrderID: "ended-child", TradingAccountID: "account-a",
		ClientOrderID:  flattenClientOrderIDForSpec("flatten-1", "account-a", "BTCUSDT", exchange.SideSell, quantity),
		ExchangeSymbol: "BTCUSDT", InstrumentID: fixture.instrumentID(), OrderType: "MARKET",
		Side: "SELL", PositionSide: "NET", Quantity: quantity.String(), FilledQuantity: filled,
		ReferencePrice: "100", ReferencePriceAt: fixture.now.UnixMilli(), ReduceOnly: true,
		OwnerType: "OPERATOR", OwnerID: "flatten-1", LogicalAccountID: "logical-1",
		State: state, Version: 1, FinishedAt: fixture.now.UnixMilli(),
	}
}

func TestFlattenZeroFillRejectedChildEndsPartial(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)
	fixture.position(t, "account-a", "BTCUSDT", "2")
	fixture.order(t, endedChild(fixture, "REJECTED", "0"))
	service := fixture.service()
	service.FlattenMaxAttempts = 0

	result, err := flattenOnce(t, service, "flatten-1")

	require.NoError(t, err)
	require.Equal(t, "PARTIAL", result.Action.Status)
	require.Empty(t, fixture.orders.specs)
}

func TestFlattenPartiallyFilledEndedChildRetries(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)
	fixture.position(t, "account-a", "BTCUSDT", "2")
	fixture.order(t, endedChild(fixture, "PARTIALLY_CANCELED", "1"))
	service := fixture.service()
	service.FlattenMaxAttempts = 2

	result, err := flattenOnce(t, service, "flatten-1")

	require.NoError(t, err)
	require.Equal(t, "RUNNING", result.Action.Status)
}

func runningFlatten(t *testing.T, fixture *operatorFixture, actionID string) {
	t.Helper()
	_, _, err := fixture.store.CreateOperatorAction(context.Background(), store.OperatorActionRecord{
		SpaceID: "space-1", ActionID: actionID, LogicalAccountID: "logical-1", ActionType: "FLATTEN",
		RequestJSON: `{"logical_account_id":"logical-1"}`, Reason: "close risk", Status: "RUNNING",
	})
	require.NoError(t, err)
}

func TestFlattenReturnsTheRunningFlattenOfTheSameGroup(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)
	fixture.position(t, "account-a", "BTCUSDT", "2")
	runningFlatten(t, fixture, "flatten-1")
	service := fixture.service()

	result, err := flattenOnce(t, service, "flatten-2")
	require.NoError(t, err)
	require.Equal(t, "flatten-1", result.Action.ActionID)
	require.Empty(t, fixture.orders.specs)

	// 第一个清仓持锁期间发起第二个，也拿到第一个动作。
	unlock := fixture.store.LockLogicalAccount("space-1", "logical-1")
	result, err = flattenOnce(t, service, "flatten-3")
	unlock()
	require.NoError(t, err)
	require.Equal(t, "flatten-1", result.Action.ActionID)
	_, err = fixture.store.GetOperatorAction(context.Background(), "space-1", "flatten-3")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestFlattenResumesEachLeftoverRunningAction(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)
	fixture.position(t, "account-a", "BTCUSDT", "2")
	runningFlatten(t, fixture, "flatten-1")
	runningFlatten(t, fixture, "flatten-2")

	result, err := flattenOnce(t, fixture.service(), "flatten-1")

	require.NoError(t, err)
	require.Equal(t, "flatten-1", result.Action.ActionID)
	require.Len(t, fixture.orders.specs, 1, "resuming its own action must run the flatten")
}

func TestFlattenNewActionReportsBusyLock(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)
	unlock := fixture.store.LockLogicalAccount("space-1", "logical-1")
	defer unlock()

	started := time.Now()
	_, err := flattenOnce(t, fixture.service(), "flatten-1")

	require.ErrorIs(t, err, ErrLogicalAccountBusy)
	require.Less(t, time.Since(started), 4*time.Second)
}

func TestFlattenWithManyMembersStillPlacesOrders(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)
	extra := []string{"account-c", "account-d", "account-e", "account-f"}
	require.NoError(t, fixture.store.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.SetLogicalAccountAutomation("space-1", "logical-1", "PAUSED", "configure"); err != nil {
			return err
		}
		for _, id := range extra {
			account := fixture.account(id)
			if err := tx.CreateTradingAccount(account); err != nil {
				return err
			}
			if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
				SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: id, Enabled: false,
			}); err != nil {
				return err
			}
		}
		return tx.SetLogicalAccountAutomation("space-1", "logical-1", "ACTIVE", "")
	}))
	fixture.position(t, "account-a", "BTCUSDT", "2")
	fixture.orders.nextID = ""
	// 每次同步 300ms，接近实盘轻量同步的耗时；子单成交后的同步里持仓归零。
	fixture.syncer.onSync = func(_ context.Context, accountID string, _ int) error {
		time.Sleep(300 * time.Millisecond)
		if accountID == "account-a" && len(fixture.orders.specs) > 0 {
			fixture.position(t, "account-a", "BTCUSDT", "0")
		}
		return nil
	}
	service := fixture.service()
	service.FlattenMaxAttempts = 0
	service.FlattenTimeout = 0

	result, err := flattenOnce(t, service, "flatten-1")

	require.NoError(t, err)
	require.Equal(t, "COMPLETED", result.Action.Status)
	require.Len(t, fixture.orders.specs, 1, "the member with a position must get its flatten order")
}

func TestFlattenTradingAccountOnlyTouchesThatMember(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)
	fixture.orders.nextID = ""
	fixture.position(t, "account-a", "BTCUSDT", "2")
	fixture.position(t, "account-b", "BTC-USDT-SWAP", "3")
	fixture.order(t, activeOrder(fixture, "other-member-order", "EXTERNAL"))
	other, err := fixture.store.GetOrder(context.Background(), "space-1", "other-member-order")
	require.NoError(t, err)
	require.Equal(t, "account-a", other.TradingAccountID)
	service := fixture.service()

	result, err := service.FlattenTradingAccount(context.Background(), FlattenCommand{
		SpaceID: "space-1", ActionID: "flatten-b", TradingAccountID: "account-b", Reason: "close one member",
	})

	require.NoError(t, err)
	require.Equal(t, "logical-1", result.Action.LogicalAccountID)
	require.Len(t, result.Accounts, 1)
	require.Equal(t, "account-b", result.Accounts[0].TradingAccountID)
	for _, spec := range fixture.orders.specs {
		require.Equal(t, "account-b", spec.TradingAccountID)
	}
	require.NotContains(t, fixture.trace, "cancel:other-member-order")
	logical, err := fixture.store.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "PAUSED", logical.AutomationState)

	// 工作线程恢复时从请求 JSON 读回过滤条件。
	command, err := decodeFlattenRequest(result.Action)
	require.NoError(t, err)
	require.Equal(t, "account-b", command.TradingAccountID)
}

func TestFlattenTradingAccountRequiresLogicalMembership(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)
	require.NoError(t, fixture.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateTradingAccount(fixture.account("account-solo"))
	}))
	_, err := fixture.service().FlattenTradingAccount(context.Background(), FlattenCommand{
		SpaceID: "space-1", ActionID: "flatten-solo", TradingAccountID: "account-solo", Reason: "close",
	})
	require.ErrorIs(t, err, ErrNotInLogicalAccount)
}

func TestFlattenTradingAccountIsBusyWhileAnotherMemberFlattens(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)
	_, _, err := fixture.store.CreateOperatorAction(context.Background(), store.OperatorActionRecord{
		SpaceID: "space-1", ActionID: "flatten-a", LogicalAccountID: "logical-1", ActionType: "FLATTEN",
		RequestJSON: `{"logical_account_id":"logical-1","trading_account_id":"account-a"}`, Reason: "close a", Status: "RUNNING",
	})
	require.NoError(t, err)
	service := fixture.service()

	// 平 account-b 时另一个成员正在单独平仓：那次的进度不代表 account-b，报正忙。
	_, err = service.FlattenTradingAccount(context.Background(), FlattenCommand{
		SpaceID: "space-1", ActionID: "flatten-b", TradingAccountID: "account-b", Reason: "close b",
	})
	require.ErrorIs(t, err, ErrLogicalAccountBusy)
	// 同一个账户再发起，返回那次的进度。
	result, err := service.FlattenTradingAccount(context.Background(), FlattenCommand{
		SpaceID: "space-1", ActionID: "flatten-a2", TradingAccountID: "account-a", Reason: "close a again",
	})
	require.NoError(t, err)
	require.Equal(t, "flatten-a", result.Action.ActionID)
	require.Empty(t, fixture.orders.specs)
}
