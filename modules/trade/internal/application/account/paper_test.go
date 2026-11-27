package account

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/logicalaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCreatePaperPreservesControlModeAndRejectsUnknownAtomically(t *testing.T) {
	for _, mode := range []string{"", "STRATEGY", "MANUAL", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			s := Service{Store: Repository{Store: db}}
			result, err := s.CreatePaperAccount(context.Background(), CreatePaperCommand{
				SpaceID: "space", AccountName: "paper", LogicalAccountName: "logical", ControlMode: mode,
				Exchange: exchange.ExchangeBinance, Markets: []exchange.MarketType{exchange.MarketTypeSpot},
				SettlementAsset: "USDT", InitialBalance: shared.MustDecimal("100"),
			})
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
			require.NotEqual(t, result.Account.TradingAccountID, result.LogicalAccount.LogicalAccountID)
			persisted, err := db.GetLogicalAccount(context.Background(), "space", result.LogicalAccount.LogicalAccountID)
			require.NoError(t, err)
			require.Equal(t, want, persisted.ControlMode)
		})
	}
}

func paperCommand(account, logical string) CreatePaperCommand {
	return CreatePaperCommand{
		SpaceID: "space", AccountName: account, LogicalAccountName: logical, Exchange: exchange.ExchangeBinance,
		Markets: []exchange.MarketType{exchange.MarketTypeSpot}, SettlementAsset: "USDT", InitialBalance: shared.MustDecimal("1000"),
		MakerFeeRate: shared.Zero(), TakerFeeRate: shared.Zero(), SlippageBPS: shared.Zero(),
	}
}

func TestCreatePaperWithoutLogicalNameOnlyCreatesAccount(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	s := Service{Store: Repository{Store: db}}

	result, err := s.CreatePaperAccount(context.Background(), paperCommand("paper-b", ""))

	require.NoError(t, err)
	require.Empty(t, result.LogicalAccount.LogicalAccountID)
	logicals, err := db.ListLogicalAccounts(context.Background(), "space")
	require.NoError(t, err)
	require.Empty(t, logicals)
}

func TestCreatePaperUsesRandomIDsNotNameHash(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	s := Service{Store: Repository{Store: db}}

	first, err := s.CreatePaperAccount(ctx, paperCommand("paper-a", "logical-old"))
	require.NoError(t, err)
	// 重命名组合后，再次用旧组合名称新建模拟盘账户和组合，不会与旧组合的哈希 ID 冲突。
	require.NoError(t, db.DBForTest().Exec(
		`UPDATE t_logical_accounts SET c_name = ? WHERE c_space_id = ? AND c_logical_account_id = ?`,
		"logical-renamed", "space", first.LogicalAccount.LogicalAccountID,
	).Error)
	second, err := s.CreatePaperAccount(ctx, paperCommand("paper-b", "logical-old"))
	require.NoError(t, err)
	require.NotEqual(t, first.LogicalAccount.LogicalAccountID, second.LogicalAccount.LogicalAccountID)
	require.NotEqual(t, first.Account.TradingAccountID, second.Account.TradingAccountID)
}

// 关闭一个成员只影响它自己：移出成员，另一个成员留在组合账户里；通过统一 Update(DISABLED) 与 ClosePaperAccount 口径一致。
func TestClosePaperRemovesOnlyClosedMember(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	s := Service{Store: Repository{Store: db}}
	first, err := s.CreatePaperAccount(ctx, paperCommand("paper-a", "logical"))
	require.NoError(t, err)
	second, err := s.CreatePaperAccount(ctx, paperCommand("paper-b", ""))
	require.NoError(t, err)
	logicalID := first.LogicalAccount.LogicalAccountID
	require.NoError(t, db.Transaction(ctx, func(tx *store.Tx) error {
		return tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space", LogicalAccountID: logicalID,
			TradingAccountID: second.Account.TradingAccountID, Enabled: true, Priority: 2,
		})
	}))

	disabled := exchange.AccountStatusDisabled
	closed, affected, err := s.Update(ctx, UpdateCommand{
		TradingAccountID: first.Account.TradingAccountID,
		Status:           &disabled,
	})
	require.NoError(t, err)
	require.Equal(t, exchange.AccountStatusDisabled, closed.Status)
	require.NotNil(t, affected)
	require.Equal(t, logicalID, affected.ID)
	require.Equal(t, logicalaccount.AutomationPaused, affected.AutomationState)

	// 已关闭的模拟盘不能重新启用。
	enabled := exchange.AccountStatusEnabled
	_, _, err = s.Update(ctx, UpdateCommand{
		TradingAccountID: first.Account.TradingAccountID,
		Status:           &enabled,
	})
	require.ErrorIs(t, err, ErrPaperAccountImmutable)

	members, err := db.ListLogicalAccountMembers(ctx, "space", logicalID, true)
	require.NoError(t, err)
	require.Len(t, members, 1)
	require.Equal(t, second.Account.TradingAccountID, members[0].TradingAccountID)
	logical, err := db.GetLogicalAccount(ctx, "space", logicalID)
	require.NoError(t, err)
	require.Equal(t, "PAUSED", logical.AutomationState)
}

func TestClosePaperRemovesDisabledMembership(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	s := Service{Store: Repository{Store: db}}
	first, err := s.CreatePaperAccount(ctx, paperCommand("paper-a", "logical"))
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

	result, err := s.ClosePaperAccount(ctx, "space", accountID)
	require.NoError(t, err)
	require.Equal(t, "DISABLED", result.Account.Status)
	require.Equal(t, logicalID, result.LogicalAccount.LogicalAccountID)

	_, _, err = db.FindLogicalAccountMembership(ctx, "space", accountID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestCreatePaperNormalizesMarketsAndRejectsUnsupportedSets(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	s := Service{Store: Repository{Store: db}}
	ctx := context.Background()

	for name, markets := range map[string][]exchange.MarketType{
		"空集合":    nil,
		"未知市场":   {"FUTURES"},
		"股票混合现货": {exchange.MarketTypeEquity, exchange.MarketTypeSpot},
	} {
		command := paperCommand("bad-"+name, "")
		command.Markets = markets
		_, err := s.CreatePaperAccount(ctx, command)
		require.Error(t, err, name)
	}
	command := paperCommand("both", "")
	command.Markets = []exchange.MarketType{exchange.MarketTypeSwap, exchange.MarketTypeSpot}
	both, err := s.CreatePaperAccount(ctx, command)
	require.NoError(t, err)
	require.Equal(t, []string{"spot", "swap"}, both.Account.Markets)
	require.Equal(t, "CROSS", both.Account.MarginMode)
	require.Equal(t, "UNIFIED", both.Account.Snapshot.Balances[0].Wallet)

	command = paperCommand("swap", "")
	command.Markets = []exchange.MarketType{exchange.MarketTypeSwap}
	result, err := s.CreatePaperAccount(ctx, command)
	require.NoError(t, err)
	require.Equal(t, []string{"swap"}, result.Account.Markets)
	require.Equal(t, "CROSS", result.Account.MarginMode)
	require.Equal(t, "1", result.Account.LeverageSettings[store.DefaultLeverageKey])
	require.Equal(t, "UNIFIED", result.Account.Snapshot.Balances[0].Wallet)
}

func TestCreatePaperJoinsExistingPausedLogicalAccountInOneTransaction(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	s := Service{Store: Repository{Store: db}}
	first, err := s.CreatePaperAccount(ctx, paperCommand("paper-a", "logical"))
	require.NoError(t, err)
	logicalID := first.LogicalAccount.LogicalAccountID

	command := paperCommand("paper-b", "")
	command.LogicalAccountID = logicalID
	second, err := s.CreatePaperAccount(ctx, command)
	require.NoError(t, err)
	require.Equal(t, logicalID, second.LogicalAccount.LogicalAccountID)
	members, err := db.ListLogicalAccountMembers(ctx, "space", logicalID, true)
	require.NoError(t, err)
	require.Len(t, members, 2)
	require.Equal(t, second.Account.TradingAccountID, members[1].TradingAccountID)
	require.Equal(t, 2, members[1].Priority)
	require.True(t, members[1].Enabled)

	command = paperCommand("paper-c", "")
	command.LogicalAccountID = logicalID
	command.Priority = 9
	third, err := s.CreatePaperAccount(ctx, command)
	require.NoError(t, err)
	members, err = db.ListLogicalAccountMembers(ctx, "space", logicalID, true)
	require.NoError(t, err)
	require.Equal(t, third.Account.TradingAccountID, members[2].TradingAccountID)
	require.Equal(t, 9, members[2].Priority)
}

func TestCreatePaperIntoRejectsRunningMismatchedAndUnknownLogicalAccountsWithoutCreatingAccount(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	s := Service{Store: Repository{Store: db}}
	first, err := s.CreatePaperAccount(ctx, paperCommand("paper-a", "logical"))
	require.NoError(t, err)
	logicalID := first.LogicalAccount.LogicalAccountID
	require.NoError(t, db.DBForTest().Table("t_logical_accounts").
		Where("c_logical_account_id = ?", logicalID).Updates(map[string]any{"c_automation_state": "ACTIVE", "c_pause_reason": "", "c_pause_source": ""}).Error)

	countAccounts := func() int64 {
		var count int64
		require.NoError(t, db.DBForTest().Table("t_trading_accounts").Count(&count).Error)
		return count
	}
	before := countAccounts()

	running := paperCommand("paper-b", "")
	running.LogicalAccountID = logicalID
	_, err = s.CreatePaperAccount(ctx, running)
	require.ErrorContains(t, err, "先暂停")

	require.NoError(t, db.DBForTest().Table("t_logical_accounts").
		Where("c_logical_account_id = ?", logicalID).Updates(map[string]any{"c_automation_state": "PAUSED", "c_pause_reason": "测试", "c_pause_source": "USER"}).Error)
	mismatched := paperCommand("paper-c", "")
	mismatched.LogicalAccountID = logicalID
	mismatched.SettlementAsset = "USDC"
	_, err = s.CreatePaperAccount(ctx, mismatched)
	require.ErrorContains(t, err, "结算资产")

	unknown := paperCommand("paper-d", "")
	unknown.LogicalAccountID = "no-such-logical"
	_, err = s.CreatePaperAccount(ctx, unknown)
	require.Error(t, err)

	both := paperCommand("paper-e", "new-name")
	both.LogicalAccountID = logicalID
	_, err = s.CreatePaperAccount(ctx, both)
	require.Error(t, err)

	// 每次被拒绝都不留下孤立的执行账户。
	require.Equal(t, before, countAccounts())
}

func TestCreatePaperRejectsSlippageAtOrAbove10000(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	s := Service{Store: Repository{Store: db}}

	for _, raw := range []string{"10000", "10000.01", "25000"} {
		cmd := paperCommand("bad-slip-"+raw, "")
		cmd.SlippageBPS = shared.MustDecimal(raw)
		_, err := s.CreatePaperAccount(ctx, cmd)
		require.ErrorIs(t, err, tradingaccount.ErrInvalidAccount, "slippage=%s", raw)
	}
	var count int64
	require.NoError(t, db.DBForTest().Table("t_trading_accounts").Count(&count).Error)
	require.Zero(t, count, "被拒绝的滑点配置不得落库")

	okCmd := paperCommand("ok-slip", "")
	okCmd.SlippageBPS = shared.MustDecimal("9999.99")
	created, err := s.CreatePaperAccount(ctx, okCmd)
	require.NoError(t, err)
	require.Equal(t, "9999.99", created.Account.PaperConfig.SlippageBPS)
}

func TestUpdatePaperRejectsMixingRetentionDaysAndDisableAtomically(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	s := Service{Store: Repository{Store: db}}

	created, err := s.CreatePaperAccount(ctx, paperCommand("paper-mix", "logical-mix"))
	require.NoError(t, err)
	disabled := exchange.AccountStatusDisabled
	days := 30
	_, affected, err := s.Update(ctx, UpdateCommand{
		TradingAccountID:   created.Account.TradingAccountID,
		Status:             &disabled,
		OrderRetentionDays: &days,
	})
	require.ErrorIs(t, err, tradingaccount.ErrInvalidAccount)
	require.Nil(t, affected)

	// 被拒绝后保留天数与启用状态均未被部分提交。
	stored, err := db.GetTradingAccount(ctx, "space", created.Account.TradingAccountID)
	require.NoError(t, err)
	require.Equal(t, "ENABLED", stored.Status)
	require.Equal(t, tradingaccount.DefaultOrderRetentionDays, stored.PaperConfig.OrderRetentionDays)
}

func TestClosePaperAccountSpaceMismatchAndConcurrentMembershipChange(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	s := Service{Store: Repository{Store: db}}

	created, err := s.CreatePaperAccount(ctx, paperCommand("paper-solo", ""))
	require.NoError(t, err)
	_, err = s.ClosePaperAccount(ctx, "other-space", created.Account.TradingAccountID)
	require.ErrorIs(t, err, store.ErrInvalidRecord)

	// 并发测试：一个协程把独立模拟账户加入已暂停组合，另一个协程同时关闭该账户。
	// 加锁后重查归属机制确保不会发生错锁，关闭后绝不留下悬空成员。
	logical, err := s.CreatePaperAccount(ctx, paperCommand("paper-holder", "logical-race"))
	require.NoError(t, err)
	logicalID := logical.LogicalAccount.LogicalAccountID
	targetID := created.Account.TradingAccountID

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	var closeRes PaperAccountResult
	var closeErr error
	go func() {
		defer wg.Done()
		<-start
		unlock, lockErr := db.LockMemberChange(ctx, "space", logicalID, targetID)
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
				SpaceID: "space", LogicalAccountID: logicalID, TradingAccountID: targetID, Enabled: true, Priority: 2,
			})
		})
	}()
	go func() {
		defer wg.Done()
		<-start
		closeRes, closeErr = s.ClosePaperAccount(ctx, "space", targetID)
	}()
	close(start)
	wg.Wait()

	require.NoError(t, closeErr)
	require.Equal(t, "DISABLED", closeRes.Account.Status)
	_, _, memberErr := db.FindLogicalAccountMembership(ctx, "space", targetID)
	require.ErrorIs(t, memberErr, gorm.ErrRecordNotFound, "关闭后的模拟账户绝不能仍留在任何组合中")
}
