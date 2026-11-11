package test

import (
	"context"
	"strings"
	"testing"
	"time"

	logicalapp "github.com/mooyang-code/moox/modules/trade/internal/application/logicalaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/application/operator"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// 账户带着在交易所直接下的挂单加入组合：同步导入这笔外部单时账户还不在组合里，没有组合编号。
// 加入组合（启用成员）时补上组合编号，组合的订单列表能看到它，就绪原因能指出它，人工撤单走真实撤单路径能撤掉。
func TestExternalOrderPlacedBeforeJoiningIsVisibleAndCancelable(t *testing.T) {
	ctx := context.Background()
	fake := newFakeExchange(exchange.MarketTypeSpot)
	f := newFixture(t, exchange.MarketTypeSpot, fake)
	limit := shared.MustDecimal("40000")
	fake.orders["outside-client"] = exchange.Order{Market: exchange.MarketTypeSpot,
		ExchangeOrderID: "outside-order", ClientOrderID: "outside-client",
		ExchangeSymbol: testSymbol, OrderType: exchange.OrderTypeLimit, TimeInForce: exchange.TimeInForceGTC,
		Side: exchange.SideBuy, Quantity: shared.MustDecimal("0.01"), LimitPrice: &limit,
		Status: exchange.OrderStatusOpen, CreatedAt: testNow.Add(time.Second), UpdatedAt: testNow.Add(time.Second),
	}
	_, err := f.sync.SyncAccount(ctx, testAccount)
	require.NoError(t, err)
	imported, err := f.store.GetOrderByClientID(ctx, testSpace, testAccount, exchange.MarketTypeSpot, "outside-client")
	require.NoError(t, err)
	require.Equal(t, "EXTERNAL", imported.OwnerType)
	require.Empty(t, imported.LogicalAccountID, "导入时账户还不在组合里")

	require.NoError(t, f.store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: testSpace, LogicalAccountID: testLogicalAccount, Name: "带挂单加入",
			ExecutionMode: "PAPER", SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "configure",
		})
	}))
	logical := &logicalapp.Service{Store: f.store, Syncer: syncBridge{service: f.sync}, Now: func() time.Time { return testNow }}
	_, err = logical.AddMember(ctx, logicalapp.AddMemberCommand{
		SpaceID: testSpace, LogicalAccountID: testLogicalAccount, TradingAccountID: testAccount,
		Enabled: true, Priority: 1, AdoptExistingExposure: true,
	})
	require.NoError(t, err)

	orders, _, err := f.store.ListOrders(ctx, testSpace, store.OrderQuery{LogicalAccountID: testLogicalAccount, OnlyOpen: true, Limit: 10})
	require.NoError(t, err)
	require.Len(t, orders, 1)
	require.Equal(t, imported.OrderID, orders[0].OrderID)
	readiness, err := logical.Readiness(ctx, logical.Store, testSpace, testLogicalAccount)
	require.NoError(t, err)
	require.False(t, readiness.Ready)
	require.True(t, containsText(readiness.Reasons, "E2E 有外部挂单 "+imported.OrderID), readiness.Reasons)

	operatorService := &operator.Service{
		Store: f.store, Orders: f.orders, Syncer: syncBridge{service: f.sync},
		Prices: logicalAccountE2EPriceSource{at: testNow}, Now: func() time.Time { return testNow },
	}
	result, err := operatorService.CancelOrder(ctx, operator.CancelOrderCommand{
		SpaceID: testSpace, ActionID: "cancel-outside", OrderID: imported.OrderID, Reason: "撤掉加入前的挂单",
	})
	require.NoError(t, err)
	require.Equal(t, "COMPLETED", result.Action.Status)
	canceled, err := f.store.GetOrder(ctx, testSpace, imported.OrderID)
	require.NoError(t, err)
	require.Equal(t, "CANCELED", canceled.State)
	require.Equal(t, exchange.OrderStatusCanceled, fake.orders["outside-client"].Status)
}

func containsText(values []string, needle string) bool {
	for _, value := range values {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
