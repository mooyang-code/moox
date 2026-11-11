package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 启用成员时，账户上还没结束、也没有组合编号的外部单补上本组合的编号（不动版本号）；
// 已结束的单和以停用状态写成员时都不动。
func TestEnablingMemberAdoptsOpenOrdersWithoutLogicalAccount(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateTradingAccount(testAccount()); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "one",
			ExecutionMode: "PAPER", SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		for id, state := range map[string]string{"open-ext": "OPEN", "done-ext": "FILLED"} {
			if err := tx.CreateOrder(OrderRecord{
				SpaceID: "space-1", OrderID: id, TradingAccountID: "account-1", ClientOrderID: id,
				ExchangeSymbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY", Quantity: "1",
				ReferencePrice: "100", ReferencePriceAt: 1, OwnerType: "EXTERNAL", OwnerID: id, State: state,
			}); err != nil {
				return err
			}
		}
		return nil
	}))
	put := func(enabled bool) {
		require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
			return tx.PutLogicalAccountMember(LogicalAccountMemberRecord{
				SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-1", Enabled: enabled,
			})
		}))
	}
	order := func(id string) OrderRecord {
		value, err := s.GetOrder(ctx, "space-1", id)
		require.NoError(t, err)
		return value
	}

	put(false)
	require.Empty(t, order("open-ext").LogicalAccountID, "停用成员不接管订单")

	before := order("open-ext")
	put(true)
	adopted := order("open-ext")
	require.Equal(t, "logical-1", adopted.LogicalAccountID)
	require.Equal(t, before.Version, adopted.Version)
	require.Empty(t, order("done-ext").LogicalAccountID, "已结束的单不动")
}
