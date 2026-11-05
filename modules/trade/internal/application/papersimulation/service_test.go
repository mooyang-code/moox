package papersimulation

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCreatePreservesControlModeAndRejectsUnknownAtomically(t *testing.T) {
	for _, mode := range []string{"", "STRATEGY", "MANUAL", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			s := Service{Store: db}
			result, err := s.Create(context.Background(), CreateCommand{SpaceID: "space", AccountName: "paper", LogicalAccountName: "logical", ControlMode: mode, Exchange: "BINANCE", Markets: []exchange.MarketType{"SPOT"}, SettlementAsset: "USDT", InitialBalance: shared.MustDecimal("100")})
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
		Markets: []exchange.MarketType{exchange.MarketTypeSpot}, SettlementAsset: "USDT", InitialBalance: shared.MustDecimal("1000"),
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

func TestCloseRemovesDisabledMembership(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	s := Service{Store: db}
	first, err := s.Create(ctx, paperCommand("paper-a", "logical"))
	require.NoError(t, err)
	logicalID := first.LogicalAccount.LogicalAccountID
	accountID := first.Account.TradingAccountID
	require.NoError(t, db.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.SetLogicalAccountAutomation("space", logicalID, "PAUSED", "configure"); err != nil {
			return err
		}
		return tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space", LogicalAccountID: logicalID, TradingAccountID: accountID, Enabled: false,
		})
	}))

	require.NoError(t, s.Close(ctx, "space", accountID))

	_, _, err = db.FindLogicalAccountMembership(ctx, "space", accountID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestCreateNormalizesMarketsAndRejectsUnsupportedSets(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	s := Service{Store: db}
	ctx := context.Background()

	for name, markets := range map[string][]exchange.MarketType{
		"空集合":    nil,
		"未知市场":   {"FUTURES"},
		"股票混合现货": {exchange.MarketTypeStock, exchange.MarketTypeSpot},
	} {
		command := paperCommand("bad-"+name, "")
		command.Markets = markets
		_, err := s.Create(ctx, command)
		require.Error(t, err, name)
	}
	// 现货加合约：一个账户、一个资金池同时撮合两个市场。
	command := paperCommand("both", "")
	command.Markets = []exchange.MarketType{exchange.MarketTypeSwap, exchange.MarketTypeSpot}
	both, err := s.Create(ctx, command)
	require.NoError(t, err)
	require.Equal(t, []string{"SPOT", "SWAP"}, both.Account.Markets)
	require.Equal(t, "CROSS", both.Account.MarginMode)
	require.Equal(t, "UNIFIED", both.Account.Snapshot.Balances[0].Wallet)

	command = paperCommand("swap", "")
	command.Markets = []exchange.MarketType{exchange.MarketTypeSwap}
	result, err := s.Create(ctx, command)
	require.NoError(t, err)
	require.Equal(t, []string{"SWAP"}, result.Account.Markets)
	require.Equal(t, "CROSS", result.Account.MarginMode)
	require.Equal(t, "1", result.Account.LeverageSettings[store.DefaultLeverageKey])
	require.Equal(t, "UNIFIED", result.Account.Snapshot.Balances[0].Wallet)
}
