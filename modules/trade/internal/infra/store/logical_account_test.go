package store

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/logicalaccount"
	"github.com/stretchr/testify/require"
)

func TestLogicalAccountMembershipAllowsOneGroupPerTradingAccount(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	second := testAccount()
	second.TradingAccountID = "account-2"
	second.Name = "second"
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateTradingAccount(testAccount()); err != nil {
			return err
		}
		if err := tx.CreateTradingAccount(second); err != nil {
			return err
		}
		for _, account := range []LogicalAccountRecord{
			{
				SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "one",
				Environment: "PAPER", SettlementAsset: "USDT", PauseReason: "configure",
			},
			{
				SpaceID: "space-1", LogicalAccountID: "logical-2", Name: "two",
				Environment: "PAPER", SettlementAsset: "USDT", PauseReason: "configure",
			},
		} {
			if err := tx.CreateLogicalAccount(account); err != nil {
				return err
			}
		}
		return tx.PutLogicalAccountMember(LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			TradingAccountID: "account-1", Enabled: true,
		})
	}))

	err := s.Transaction(ctx, func(tx *Tx) error {
		return tx.PutLogicalAccountMember(LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-2",
			TradingAccountID: "account-1", Enabled: true,
		})
	})
	require.ErrorIs(t, err, logicalaccount.ErrMemberElsewhere)

	// 停用成员也算归属：同一执行账户不能再以停用成员加入另一个组合。
	err = s.Transaction(ctx, func(tx *Tx) error {
		return tx.PutLogicalAccountMember(LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-2",
			TradingAccountID: "account-1", Enabled: false,
		})
	})
	require.ErrorIs(t, err, logicalaccount.ErrMemberElsewhere)

	// 绕过服务层直接写库时，唯一约束兜底。
	err = s.Transaction(ctx, func(tx *Tx) error {
		return tx.db.Exec(`INSERT INTO t_logical_account_members
			(c_space_id, c_logical_account_id, c_trading_account_id, c_enabled, c_priority)
			VALUES ('space-1', 'logical-2', 'account-1', 0, 0)`).Error
	})
	require.Error(t, err)
}

func TestLogicalAccountMembershipRejectsInhomogeneousAccount(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// 成员同质性要求环境与结算资产一致；市场不要求一致。
	other := testAccount()
	other.TradingAccountID = "usdc-1"
	other.Name = "usdc"
	other.SettlementAsset = "USDC"
	err := s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateTradingAccount(other); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "main",
			Environment: "PAPER", SettlementAsset: "USDT", PauseReason: "configure",
		}); err != nil {
			return err
		}
		return tx.PutLogicalAccountMember(LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			TradingAccountID: "usdc-1", Enabled: true,
		})
	})
	require.ErrorIs(t, err, ErrInvalidRecord)
}

func TestLogicalAccountMarketsAreUnionOfEnabledMembers(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	spot := testAccount()
	spot.TradingAccountID, spot.Name = "spot-1", "spot"
	swap := testAccount()
	swap.TradingAccountID, swap.Name = "swap-1", "swap"
	swap.Markets, swap.MarginMode = []string{"swap"}, "CROSS"
	both := testAccount()
	both.TradingAccountID, both.Name = "both-1", "both"
	both.Markets, both.MarginMode = []string{"spot", "swap"}, "CROSS"
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		for _, account := range []TradingAccountRecord{spot, swap, both} {
			if err := tx.CreateTradingAccount(account); err != nil {
				return err
			}
		}
		return tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "main",
			Environment: "PAPER", SettlementAsset: "USDT", PauseReason: "configure",
		})
	}))
	markets := func() []string {
		got, err := s.LogicalAccountMarkets(ctx, "space-1", "logical-1")
		require.NoError(t, err)
		return got
	}
	require.Empty(t, markets())
	put := func(accountID string, enabled bool) {
		require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
			return tx.PutLogicalAccountMember(LogicalAccountMemberRecord{
				SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: accountID, Enabled: enabled,
			})
		}))
	}
	put("swap-1", true)
	require.Equal(t, []string{"swap"}, markets())
	put("spot-1", true)
	require.Equal(t, []string{"spot", "swap"}, markets())
	// 停用的成员不算。
	put("spot-1", false)
	require.Equal(t, []string{"swap"}, markets())
	put("both-1", true)
	require.Equal(t, []string{"spot", "swap"}, markets())
}

func TestLogicalAccountMembershipRejectsMixedEnvironmentProfiles(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	testnet := testAccount()
	testnet.TradingAccountID = "testnet-1"
	testnet.Name = "testnet"
	testnet.ExecutionMode = "LIVE"
	testnet.Environment = "TESTNET"
	production := testnet
	production.TradingAccountID = "production-1"
	production.Name = "production"
	production.Environment = "PRODUCTION"
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		for _, account := range []TradingAccountRecord{testnet, production} {
			if err := tx.CreateTradingAccount(account); err != nil {
				return err
			}
		}
		if err := tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "live",
			Environment: "TESTNET", SettlementAsset: "USDT", PauseReason: "configure",
		}); err != nil {
			return err
		}
		return tx.PutLogicalAccountMember(LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			TradingAccountID: "testnet-1", Enabled: true,
		})
	}))

	err := s.Transaction(ctx, func(tx *Tx) error {
		return tx.PutLogicalAccountMember(LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			TradingAccountID: "production-1", Enabled: true,
		})
	})
	require.ErrorIs(t, err, ErrInvalidRecord)

	// 即使组合没有任何成员，环境本身也限制不能加入不同环境的执行账户。
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-prod", Name: "prod",
			Environment: "PRODUCTION", SettlementAsset: "USDT", PauseReason: "configure",
		})
	}))
	err = s.Transaction(ctx, func(tx *Tx) error {
		return tx.PutLogicalAccountMember(LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-prod",
			TradingAccountID: "testnet-1", Enabled: true,
		})
	})
	require.ErrorIs(t, err, ErrInvalidRecord)
}

func TestLogicalAccountMembershipChangeRequiresPause(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateTradingAccount(testAccount()); err != nil {
			return err
		}
		return tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "active",
			Environment: "PAPER", SettlementAsset: "USDT",
		})
	}))
	err := s.Transaction(ctx, func(tx *Tx) error {
		return tx.PutLogicalAccountMember(LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			TradingAccountID: "account-1", Enabled: true,
		})
	})
	require.ErrorIs(t, err, ErrInvalidRecord)
}

func TestLogicalAccountAutomationRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "main",
			Environment: "PAPER", SettlementAsset: "USDT", PauseReason: "new",
		}); err != nil {
			return err
		}
		return tx.ResumeLogicalAccount("space-1", "logical-1")
	}))
	got, err := s.GetLogicalAccount(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "ACTIVE", got.AutomationState())
	require.True(t, got.IsActive())
	require.Empty(t, got.PauseReason)
}

func TestLogicalAccountUpdatedAtFollowsAutomationChange(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "one",
			Environment: "PAPER", SettlementAsset: "USDT", PauseReason: "configure",
		})
	}))
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.ResumeLogicalAccount("space-1", "logical-1")
	}))
	record, err := s.GetLogicalAccount(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	// 换手告警靠它判断 1 小时内是否暂停或恢复过。
	require.WithinDuration(t, time.Now(), record.UpdatedAt, time.Minute)
}
