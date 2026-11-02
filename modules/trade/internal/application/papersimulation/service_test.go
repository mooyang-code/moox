package papersimulation

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func TestCreatePreservesControlModeAndRejectsUnknownAtomically(t *testing.T) {
	for _, mode := range []string{"", "STRATEGY", "MANUAL", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			s := Service{Store: db}
			result, err := s.Create(context.Background(), CreateCommand{SpaceID: "space", AccountName: "paper", LogicalAccountName: "logical", ControlMode: mode, Exchange: "BINANCE", MarketType: "SPOT", SettlementAsset: "USDT", InitialBalance: shared.MustDecimal("100")})
			if mode == "unknown" {
				require.Error(t, err)
				var count int64
				require.NoError(t, db.DBForTest().Table("t_trading_accounts").Count(&count).Error)
				require.Zero(t, count)
				return
			}
			require.NoError(t, err)
			want := mode
			if want == "" {
				want = "STRATEGY"
			}
			require.Equal(t, want, result.LogicalAccount.ControlMode)
			persisted, err := db.GetLogicalAccount(context.Background(), "space", result.LogicalAccount.LogicalAccountID)
			require.NoError(t, err)
			require.Equal(t, want, persisted.ControlMode)
		})
	}
}

func paperCommand(account, logical string) CreateCommand {
	return CreateCommand{
		SpaceID: "space", AccountName: account, LogicalAccountName: logical, Exchange: exchange.ExchangeBinance,
		MarketType: exchange.MarketTypeSpot, SettlementAsset: "USDT", InitialBalance: shared.MustDecimal("1000"),
		MakerFeeRate: shared.Zero(), TakerFeeRate: shared.Zero(), SlippageBPS: shared.Zero(),
	}
}

func TestCreateWithoutLogicalNameOnlyCreatesAccount(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	s := Service{Store: db}

	result, err := s.Create(context.Background(), paperCommand("paper-b", ""))

	require.NoError(t, err)
	require.Empty(t, result.LogicalAccount.LogicalAccountID)
	logicals, err := db.ListLogicalAccounts(context.Background(), "space")
	require.NoError(t, err)
	require.Empty(t, logicals)
}

// 关闭一个成员只影响它自己：移出成员，另一个成员留在组合账户里。
func TestCloseRemovesOnlyClosedMember(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	s := Service{Store: db}
	first, err := s.Create(ctx, paperCommand("paper-a", "logical"))
	require.NoError(t, err)
	second, err := s.Create(ctx, paperCommand("paper-b", ""))
	require.NoError(t, err)
	logicalID := first.LogicalAccount.LogicalAccountID
	require.NoError(t, db.Transaction(ctx, func(tx *store.Tx) error {
		return tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space", LogicalAccountID: logicalID,
			TradingAccountID: second.Account.TradingAccountID, Enabled: true, Priority: 2,
		})
	}))

	require.NoError(t, s.Close(ctx, "space", first.Account.TradingAccountID))

	members, err := db.ListLogicalAccountMembers(ctx, "space", logicalID, true)
	require.NoError(t, err)
	require.Len(t, members, 1)
	require.Equal(t, second.Account.TradingAccountID, members[0].TradingAccountID)
	logical, err := db.GetLogicalAccount(ctx, "space", logicalID)
	require.NoError(t, err)
	require.Equal(t, "PAUSED", logical.AutomationState)
}
