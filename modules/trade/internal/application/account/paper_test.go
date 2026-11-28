package account

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func paperCommand(account string) CreatePaperCommand {
	return CreatePaperCommand{
		SpaceID: "space", AccountName: account, Exchange: exchange.ExchangeBinance,
		Markets: []exchange.MarketType{exchange.MarketTypeSpot}, SettlementAsset: "USDT", InitialBalance: shared.MustDecimal("1000"),
		MakerFeeRate: shared.Zero(), TakerFeeRate: shared.Zero(), SlippageBPS: shared.Zero(),
	}
}

func TestCreatePaperOnlyCreatesTradingAccount(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	s := Service{Store: db}

	created, err := s.CreatePaperAccount(context.Background(), paperCommand("paper-b"))

	require.NoError(t, err)
	require.NotEmpty(t, created.TradingAccountID)
	require.Equal(t, "PAPER", created.ExecutionMode)
	require.Equal(t, exchange.AccountEnvironmentPaper, created.EffectiveEnvironment())
	logicals, err := db.ListLogicalAccounts(context.Background(), "space")
	require.NoError(t, err)
	require.Empty(t, logicals)
}

func TestCreatePaperUsesRandomIDsNotNameHash(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	s := Service{Store: db}

	first, err := s.CreatePaperAccount(ctx, paperCommand("paper-old"))
	require.NoError(t, err)
	// 重命名账户后，再次用旧名称新建模拟盘账户，不会与旧账户 ID 冲突。
	require.NoError(t, db.DBForTest().Exec(
		`UPDATE t_trading_accounts SET c_name = ? WHERE c_space_id = ? AND c_trading_account_id = ?`,
		"paper-renamed", "space", first.TradingAccountID,
	).Error)
	second, err := s.CreatePaperAccount(ctx, paperCommand("paper-old"))
	require.NoError(t, err)
	require.NotEqual(t, first.TradingAccountID, second.TradingAccountID)
}

// 停用模拟盘与实盘统一：仍在组合里（无论启用还是停用成员）必须先移出组合再停用；已关闭的模拟盘不能重新启用。
func TestClosePaperRequiresRemovingFromLogicalAccountFirst(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	s := Service{Store: db}

	first, err := s.CreatePaperAccount(ctx, paperCommand("paper-a"))
	require.NoError(t, err)
	require.NoError(t, db.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space", LogicalAccountID: "logical-1", Name: "logical",
			Environment: "PAPER", SettlementAsset: "USDT", PauseReason: "configure",
		}); err != nil {
			return err
		}
		return tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space", LogicalAccountID: "logical-1",
			TradingAccountID: first.TradingAccountID, Enabled: true, Priority: 1,
		})
	}))

	disabled := exchange.AccountStatusDisabled
	_, err = s.Update(ctx, UpdateCommand{
		TradingAccountID: first.TradingAccountID,
		Status:           &disabled,
	})
	require.ErrorIs(t, err, ErrAccountInLogicalAccount)

	// 即使成员已停用，仍属于组合时也拒绝关闭。
	require.NoError(t, db.Transaction(ctx, func(tx *store.Tx) error {
		return tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space", LogicalAccountID: "logical-1",
			TradingAccountID: first.TradingAccountID, Enabled: false, Priority: 1,
		})
	}))
	_, err = s.ClosePaperAccount(ctx, "space", first.TradingAccountID)
	require.ErrorIs(t, err, ErrAccountInLogicalAccount)

	// 移出组合后再关闭：成功。
	require.NoError(t, db.Transaction(ctx, func(tx *store.Tx) error {
		return tx.DeleteLogicalAccountMember("space", "logical-1", first.TradingAccountID)
	}))
	closed, err := s.Update(ctx, UpdateCommand{
		TradingAccountID: first.TradingAccountID,
		Status:           &disabled,
	})
	require.NoError(t, err)
	require.Equal(t, string(exchange.AccountStatusDisabled), closed.Status)

	// 已关闭的模拟盘不能重新启用。
	enabled := exchange.AccountStatusEnabled
	_, err = s.Update(ctx, UpdateCommand{
		TradingAccountID: first.TradingAccountID,
		Status:           &enabled,
	})
	require.ErrorIs(t, err, ErrPaperAccountImmutable)
}

func TestCreatePaperNormalizesMarketsAndRejectsUnsupportedSets(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	s := Service{Store: db}
	ctx := context.Background()

	for name, markets := range map[string][]exchange.MarketType{
		"空集合":    nil,
		"未知市场":   {"FUTURES"},
		"股票混合现货": {exchange.MarketTypeEquity, exchange.MarketTypeSpot},
	} {
		command := paperCommand("bad-" + name)
		command.Markets = markets
		_, err := s.CreatePaperAccount(ctx, command)
		require.Error(t, err, name)
	}
	command := paperCommand("both")
	command.Markets = []exchange.MarketType{exchange.MarketTypeSwap, exchange.MarketTypeSpot}
	both, err := s.CreatePaperAccount(ctx, command)
	require.NoError(t, err)
	require.Equal(t, []string{"spot", "swap"}, both.Markets)
	require.Equal(t, "CROSS", both.MarginMode)
	require.Equal(t, "UNIFIED", both.Snapshot.Balances[0].Wallet)

	command = paperCommand("swap")
	command.Markets = []exchange.MarketType{exchange.MarketTypeSwap}
	result, err := s.CreatePaperAccount(ctx, command)
	require.NoError(t, err)
	require.Equal(t, []string{"swap"}, result.Markets)
	require.Equal(t, "CROSS", result.MarginMode)
	require.Equal(t, "1", result.LeverageSettings[store.DefaultLeverageKey])
	require.Equal(t, "UNIFIED", result.Snapshot.Balances[0].Wallet)
}

func TestCreatePaperRejectsSlippageAtOrAbove10000(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	s := Service{Store: db}

	for _, raw := range []string{"10000", "10000.01", "25000"} {
		cmd := paperCommand("bad-slip-" + raw)
		cmd.SlippageBPS = shared.MustDecimal(raw)
		_, err := s.CreatePaperAccount(ctx, cmd)
		require.ErrorIs(t, err, tradingaccount.ErrInvalidAccount, "slippage=%s", raw)
	}
	var count int64
	require.NoError(t, db.DBForTest().Table("t_trading_accounts").Count(&count).Error)
	require.Zero(t, count, "被拒绝的滑点配置不得落库")

	okCmd := paperCommand("ok-slip")
	okCmd.SlippageBPS = shared.MustDecimal("9999.99")
	created, err := s.CreatePaperAccount(ctx, okCmd)
	require.NoError(t, err)
	require.Equal(t, "9999.99", created.PaperConfig.SlippageBPS)
}

func TestUpdatePaperRejectsMixingRetentionDaysAndDisableAtomically(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	s := Service{Store: db}

	created, err := s.CreatePaperAccount(ctx, paperCommand("paper-mix"))
	require.NoError(t, err)
	disabled := exchange.AccountStatusDisabled
	days := 30
	_, err = s.Update(ctx, UpdateCommand{
		TradingAccountID:   created.TradingAccountID,
		Status:             &disabled,
		OrderRetentionDays: &days,
	})
	require.ErrorIs(t, err, tradingaccount.ErrInvalidAccount)

	// 被拒绝后保留天数与启用状态均未被部分提交。
	stored, err := db.GetTradingAccount(ctx, "space", created.TradingAccountID)
	require.NoError(t, err)
	require.Equal(t, "ENABLED", stored.Status)
	require.Equal(t, tradingaccount.DefaultOrderRetentionDays, stored.PaperConfig.OrderRetentionDays)
}

func TestClosePaperAccountSpaceMismatchAndConcurrentMembershipChange(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	s := Service{Store: db}

	created, err := s.CreatePaperAccount(ctx, paperCommand("paper-solo"))
	require.NoError(t, err)
	_, err = s.ClosePaperAccount(ctx, "other-space", created.TradingAccountID)
	require.ErrorIs(t, err, store.ErrInvalidRecord)

	// 并发测试：一个协程把独立模拟账户加入已暂停组合，另一个协程同时关闭该账户。
	// 两边都持账户锁，最终要么先关后加（账户 DISABLED 且无成员），要么先加后关（关闭返回 ErrAccountInLogicalAccount 且账户 ENABLED）。
	require.NoError(t, db.Transaction(ctx, func(tx *store.Tx) error {
		return tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space", LogicalAccountID: "logical-race", Name: "logical-race",
			Environment: "PAPER", SettlementAsset: "USDT", PauseReason: "configure",
		})
	}))
	targetID := created.TradingAccountID

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	var closeErr error
	go func() {
		defer wg.Done()
		<-start
		unlock, lockErr := db.LockMemberChange(ctx, "space", "logical-race", targetID)
		if lockErr != nil {
			return
		}
		defer unlock()
		acc, getErr := db.GetTradingAccount(ctx, "space", targetID)
		if getErr != nil || acc.Status != "ENABLED" {
			return
		}
		_ = db.Transaction(ctx, func(tx *store.Tx) error {
			return tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
				SpaceID: "space", LogicalAccountID: "logical-race", TradingAccountID: targetID, Enabled: true, Priority: 2,
			})
		})
	}()
	go func() {
		defer wg.Done()
		<-start
		_, closeErr = s.ClosePaperAccount(ctx, "space", targetID)
	}()
	close(start)
	wg.Wait()

	stored, err := db.GetTradingAccount(ctx, "space", targetID)
	require.NoError(t, err)
	_, _, memberErr := db.FindLogicalAccountMembership(ctx, "space", targetID)
	if closeErr == nil {
		require.Equal(t, "DISABLED", stored.Status)
		require.ErrorIs(t, memberErr, gorm.ErrRecordNotFound, "关闭后的模拟账户绝不能仍留在任何组合中")
	} else {
		require.ErrorIs(t, closeErr, ErrAccountInLogicalAccount)
		require.Equal(t, "ENABLED", stored.Status)
		require.NoError(t, memberErr)
	}
}
