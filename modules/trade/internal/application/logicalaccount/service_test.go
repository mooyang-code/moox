package logicalaccount

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	logicaldomain "github.com/mooyang-code/moox/modules/trade/internal/domain/logicalaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store/storetest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAddMemberRequiresAdoptionForExistingExposure(t *testing.T) {
	service, tradeStore := logicalAccountServiceFixture(t)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpsertPosition(store.PositionRecord{
			SpaceID: "space-1", TradingAccountID: "account-b",
			ExchangeSymbol: "BTCUSDT", PositionSide: "NET", SignedQuantity: "1",
			Leverage: "5", MarginMode: "CROSS",
			ExchangeUpdatedAt: 1_900,
		})
	}))

	err := addMember(context.Background(), service, AddMemberCommand{
		SpaceID: "space-1", LogicalAccountID: "logical-1",
		TradingAccountID: "account-b", Enabled: true, Priority: 2,
	})
	require.ErrorIs(t, err, ErrAdoptionRequired)

	require.NoError(t, addMember(context.Background(), service, AddMemberCommand{
		SpaceID: "space-1", LogicalAccountID: "logical-1",
		TradingAccountID: "account-b", Enabled: true, Priority: 2,
		AdoptExistingExposure: true,
	}))
}

func TestManualControlRejectsResume(t *testing.T) {
	s, db := logicalAccountServiceFixture(t)
	ctx := context.Background()
	require.NoError(t, db.DBForTest().Exec("UPDATE t_logical_accounts SET c_control_mode = 'MANUAL' WHERE c_logical_account_id = 'logical-1'").Error)
	_, _, err := s.Resume(ctx, "space-1", "logical-1")
	require.Error(t, err)
}

func TestManualReadinessUsesActualMembers(t *testing.T) {
	s, db := logicalAccountServiceFixture(t)
	require.NoError(t, db.DBForTest().Exec("UPDATE t_logical_accounts SET c_control_mode = 'MANUAL'").Error)
	ready, err := s.Readiness(context.Background(), s.Store, "space-1", "logical-1")
	require.NoError(t, err)
	require.True(t, ready.Ready, ready.Reasons)
	require.NoError(t, db.DBForTest().Exec("DELETE FROM t_logical_account_members").Error)
	ready, err = s.Readiness(context.Background(), s.Store, "space-1", "logical-1")
	require.NoError(t, err)
	require.False(t, ready.Ready)
}

func TestRemoveMemberRejectsActiveOrdersOrPositions(t *testing.T) {
	service, tradeStore := logicalAccountServiceFixture(t)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpsertPosition(store.PositionRecord{
			SpaceID: "space-1", TradingAccountID: "account-a",
			ExchangeSymbol: "BTCUSDT", PositionSide: "NET", SignedQuantity: "1",
			Leverage: "5", MarginMode: "CROSS",
			ExchangeUpdatedAt: 1_900,
		})
	}))

	_, err := service.RemoveMember(
		context.Background(), "space-1", "logical-1", "account-a",
	)
	require.ErrorIs(t, err, ErrRemovalHasExposure)
}

type authFailingSyncer struct{}

func (authFailingSyncer) SyncAccount(context.Context, string) error {
	return &orderapp.AccountExecutionError{TradingAccountID: "account", Operation: "snapshot",
		Err: &exchange.Error{Kind: exchange.ErrorAuthentication, Err: errors.New("invalid api key")}}
}

func TestRemoveMemberWhenSyncFailsUsesLocalRecords(t *testing.T) {
	service, tradeStore := logicalAccountServiceFixture(t)
	calls := 0
	// 暂时性失败（不是凭证问题）不能退到只看本地记录。
	service.Syncer = failingAccountSyncer{calls: &calls}
	_, err := service.RemoveMember(context.Background(), "space-1", "logical-1", "account-a")
	require.ErrorContains(t, err, "sync must not run")
	service.Syncer = authFailingSyncer{}

	// 本地没有挂单和持仓：同步失败也允许移除，并提示交易所侧没有核对。
	warning, err := service.RemoveMember(context.Background(), "space-1", "logical-1", "account-a")
	require.NoError(t, err)
	require.Equal(t, UnverifiedMemberWarning, warning)

	// 本地还有持仓：同步失败时不能移除，按“有持仓不能移出”报，同步失败的原因附在后面。
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-b", Enabled: true,
		}); err != nil {
			return err
		}
		return tx.UpsertPosition(store.PositionRecord{
			SpaceID: "space-1", TradingAccountID: "account-b",
			ExchangeSymbol: "BTCUSDT", PositionSide: "NET", SignedQuantity: "1",
			Leverage: "5", MarginMode: "CROSS", ExchangeUpdatedAt: 1_900,
		})
	}))
	_, err = service.RemoveMember(context.Background(), "space-1", "logical-1", "account-b")
	require.ErrorIs(t, err, ErrRemovalHasExposure)
	require.ErrorContains(t, err, "凭证失效")
	require.NotContains(t, err.Error(), "invalid api key", "给前端的原因不带交易所英文原文")
}

func TestDisableMemberCannotAdoptAwayExistingExposure(t *testing.T) {
	service, tradeStore := logicalAccountServiceFixture(t)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpsertPosition(store.PositionRecord{
			SpaceID: "space-1", TradingAccountID: "account-a",
			ExchangeSymbol: "BTCUSDT", PositionSide: "NET", SignedQuantity: "1",
			Leverage: "5", MarginMode: "CROSS",
			ExchangeUpdatedAt: 1_900,
		})
	}))

	err := addMember(context.Background(), service, AddMemberCommand{
		SpaceID: "space-1", LogicalAccountID: "logical-1",
		TradingAccountID: "account-a", Enabled: false, Priority: 1,
		AdoptExistingExposure: true,
	})

	require.ErrorIs(t, err, ErrMemberHasExposure)
	_, member, findErr := tradeStore.FindLogicalAccountByTradingAccount(
		context.Background(), "space-1", "account-a",
	)
	require.NoError(t, findErr)
	require.True(t, member.Enabled)
}

func TestLogicalReadinessRequiresEveryEnabledMemberAndTargetMetadata(t *testing.T) {
	service, tradeStore := logicalAccountServiceFixture(t)
	require.NoError(t, addMember(context.Background(), service, AddMemberCommand{
		SpaceID: "space-1", LogicalAccountID: "logical-1",
		TradingAccountID: "account-b", Enabled: true, Priority: 2,
	}))
	acceptSessionTarget(t, tradeStore, "target-1")

	readiness, err := service.Readiness(
		context.Background(), service.Store, "space-1", "logical-1",
	)
	require.NoError(t, err)
	require.True(t, readiness.Ready)

	setLogicalFixtureReady(t, tradeStore, "account-b", false)
	readiness, err = service.Readiness(
		context.Background(), service.Store, "space-1", "logical-1",
	)
	require.NoError(t, err)
	require.False(t, readiness.Ready)
	require.Contains(t, readiness.Reasons[0], "account-b")
}

func TestLogicalReadinessRejectsTargetForDifferentSettlementAsset(t *testing.T) {
	service, tradeStore := logicalAccountServiceFixture(t)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "binance", MarketType: "swap", ExchangeSymbol: "BTCUSDT",
			InstrumentID: "BTC-USDT-SWAP", BaseAsset: "BTC", QuoteAsset: "USDT",
			SettlementAsset: "USDC", Linear: true, ContractValue: "0.001",
			ContractValueAsset: "BTC", ExchangeQuantityStep: "1",
			MinExchangeQuantity: "1", PriceTick: "0.1", Status: "TRADING",
		})
	}))
	acceptSessionTarget(t, tradeStore, "target-usdc")

	readiness, err := service.Readiness(
		context.Background(), service.Store, "space-1", "logical-1",
	)
	require.NoError(t, err)
	require.False(t, readiness.Ready)
	require.Contains(t, readiness.Reasons, "目标里的标的 BTC-USDT-SWAP 在启用的执行账户上都不能交易（没有这个标的、已暂停或下架，或结算资产不同）")
}

func TestResumeRequiresReadyNoConflictAndWarnsAboutReopen(t *testing.T) {
	service, tradeStore := logicalAccountServiceFixture(t)
	acceptSessionTarget(t, tradeStore, "target-1")

	account, warning, err := service.Resume(
		context.Background(), "space-1", "logical-1",
	)
	require.NoError(t, err)
	require.Equal(t, "ACTIVE", account.AutomationState)
	require.Contains(t, warning, "重新开仓")

	account, err = service.Pause(
		context.Background(), "space-1", "logical-1", "manual intervention",
	)
	require.NoError(t, err)
	require.Equal(t, "PAUSED", account.AutomationState)
	require.Equal(t, "manual intervention", account.PauseReason)

	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		_, _, err := tx.EnsureOperatorAction(store.OperatorActionRecord{
			SpaceID: "space-1", ActionID: "action-1",
			LogicalAccountID: "logical-1", ActionType: "FLATTEN",
			Reason: "flatten", RequestJSON: `{}`, Status: "RUNNING",
		})
		return err
	}))
	_, _, err = service.Resume(
		context.Background(), "space-1", "logical-1",
	)
	require.ErrorIs(t, err, ErrNotReady)
}

func TestModernSessionTargetCanResume(t *testing.T) {
	service, tradeStore := modernSessionFixture(t)
	acceptModernSessionTarget(t, tradeStore, "session-1")
	account, _, err := service.Resume(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "ACTIVE", account.AutomationState)
}

func TestModernResumeWithoutTargetWaitsForNextTarget(t *testing.T) {
	service, tradeStore := modernSessionFixture(t)
	account, _, err := service.Resume(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "ACTIVE", account.AutomationState)
	_, err = tradeStore.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestModernResumeIgnoresExpiredTargetMetadata(t *testing.T) {
	service, tradeStore := modernSessionFixture(t)
	want := acceptModernSessionTarget(t, tradeStore, "session-1")
	service.Now = func() time.Time { return time.UnixMilli(want.ValidUntil) }
	service.MaxSnapshotAge = 2 * time.Hour
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "binance", MarketType: "swap", ExchangeSymbol: "BTCUSDT",
			InstrumentID: "BTC-USDT-SWAP", BaseAsset: "BTC", QuoteAsset: "USDT",
			SettlementAsset: "USDC", Linear: true, ContractValue: "0.001",
			ContractValueAsset: "BTC", ExchangeQuantityStep: "1",
			MinExchangeQuantity: "1", PriceTick: "0.1", Status: "TRADING",
		})
	}))
	account, _, err := service.Resume(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "ACTIVE", account.AutomationState)
	got, err := tradeStore.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, want, got)
	executor := nonTradingExecutor(tradeStore, service.Now)
	result, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, targetapp.StatusExpired, result.Status)
	got, err = tradeStore.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	want.Status = targetapp.StatusExpired
	require.Equal(t, want.TargetID, got.TargetID)
	require.Equal(t, want.Status, got.Status)
}

func TestPausedModernTargetDoesNotTrade(t *testing.T) {
	service, tradeStore := modernSessionFixture(t)
	want := acceptModernSessionTarget(t, tradeStore, "session-1")
	executor := nonTradingExecutor(tradeStore, service.Now)
	result, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, targetapp.StatusPaused, result.Status)
	require.Empty(t, result.Action)
	got, err := tradeStore.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, want, got)
	orders, _, err := tradeStore.ListOrders(context.Background(), "space-1", store.OrderQuery{LogicalAccountID: "logical-1"})
	require.NoError(t, err)
	require.Empty(t, orders)
}

func nonTradingExecutor(tradeStore *store.Store, now func() time.Time) *targetapp.Executor {
	// Any quote or order call panics through the nil embedded interface:
	// these guards must return before touching an external trading dependency.
	return &targetapp.Executor{
		Store: tradeStore, Now: now,
		Orders: struct{ targetapp.OrderService }{},
		Prices: struct{ targetapp.PriceSource }{},
	}
}

func modernSessionFixture(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	service, tradeStore := logicalAccountServiceFixture(t)
	now := time.Now().UTC()
	service.Now = func() time.Time { return now }
	physical, err := tradeStore.GetTradingAccountByID(context.Background(), "account-a")
	require.NoError(t, err)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return storetest.SeedAccount(tx, "space-1", "account-a", storetest.AccountState{
			Ready: true, LeverageSettings: physical.LeverageSettings, Snapshot: physical.Snapshot,
			SnapshotSourceTime: now.UnixMilli(), LastSyncAt: now.UnixMilli(),
		})
	}))
	return service, tradeStore
}

// modernSessionStartedAt 是测试会话的开始时刻（毫秒）：session-1 < session-2，别的会话名按 session-1 处理。
func modernSessionStartedAt(session string) int64 {
	if session == "session-2" {
		return 2_000
	}
	return 1_000
}

func acceptModernSessionTarget(t *testing.T, tradeStore *store.Store, session string) store.LogicalAccountTargetRecord {
	t.Helper()
	now := time.Now().UTC()
	target, accepted, err := tradeStore.AcceptLogicalAccountTarget(context.Background(), store.LogicalAccountTargetRecord{
		SpaceID: "space-1", LogicalAccountID: "logical-1", TargetID: "target-" + session,
		InstanceID: "instance-1", SessionID: session, SessionStartedAt: modernSessionStartedAt(session), StrategyID: "strategy-1",
		BarEndTime: now.Add(-time.Second).UnixMilli(), ValidUntil: now.Add(time.Hour).UnixMilli(),
		Status: "PENDING", AcceptedAt: now.UnixMilli(),
		Targets: []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "1"}},
	})
	require.NoError(t, err)
	require.True(t, accepted)
	return target
}

func logicalAccountServiceFixture(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tradeStore.Close()) })
	now := time.UnixMilli(2_000).UTC()
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		for _, account := range []store.TradingAccountRecord{
			logicalFixtureAccount("account-a"),
			logicalFixtureAccount("account-b"),
		} {
			if err := tx.CreateTradingAccount(account); err != nil {
				return err
			}
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical",
			ExecutionMode:   "LIVE",
			SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			TradingAccountID: "account-a", Enabled: true, Priority: 1,
		}); err != nil {
			return err
		}
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "binance", MarketType: "swap", ExchangeSymbol: "BTCUSDT",
			InstrumentID: "BTC-USDT-SWAP", BaseAsset: "BTC", QuoteAsset: "USDT",
			SettlementAsset: "USDT", Linear: true, ContractValue: "0.001",
			ContractValueAsset: "BTC", ExchangeQuantityStep: "1",
			MinExchangeQuantity: "1", PriceTick: "0.1", Status: "TRADING",
		})
	}))
	return &Service{
		Store: tradeStore, Syncer: noopAccountSyncer{},
		Now:            func() time.Time { return now },
		MaxSnapshotAge: time.Minute,
	}, tradeStore
}

type noopAccountSyncer struct{}

func (noopAccountSyncer) SyncAccount(context.Context, string) error { return nil }

// addMember 调 AddMember，只返回错误；要看提示的用例直接调 AddMember。
func addMember(ctx context.Context, service *Service, command AddMemberCommand) error {
	_, err := service.AddMember(ctx, command)
	return err
}

func logicalFixtureAccount(id string) store.TradingAccountRecord {
	return store.TradingAccountRecord{
		SpaceID: "space-1", TradingAccountID: id, Name: id,
		Exchange: "binance", Markets: []string{"swap"},
		ExecutionMode: "LIVE", Environment: "TESTNET", CredentialSecretID: "secret-" + id,
		SettlementAsset: "USDT", MarginMode: "CROSS",
		Status: "ENABLED", Ready: true,
		LeverageSettings: store.LeverageSettings{"BTCUSDT": "5"},
		Snapshot: store.TradingAccountSnapshot{
			AvailableFunds: "1000",
			Balances: []store.AssetBalance{{Wallet: "UNIFIED",
				Asset: "USDT", Available: "1000", Total: "1000",
			}},
		},
		SnapshotSourceTime: 1_900, LastSyncAt: 1_900, LastReadyAt: 1_900,
	}
}

func setLogicalFixtureReady(
	t *testing.T,
	tradeStore *store.Store,
	accountID string,
	ready bool,
) {
	t.Helper()
	account, err := tradeStore.GetTradingAccountByID(context.Background(), accountID)
	require.NoError(t, err)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return storetest.SeedAccount(tx,
			account.SpaceID, account.TradingAccountID,
			storetest.AccountState{
				Ready: ready, LeverageSettings: account.LeverageSettings,
				Snapshot:           account.Snapshot,
				SnapshotSourceTime: account.SnapshotSourceTime,
				LastSyncAt:         1_900,
			},
		)
	}))
}

// 目标执行或完整同步持锁时，带期限的逻辑账户业务调用必须按期限返回，并释放已取得的锁（尤其不能占住全局成员锁）。
func TestLogicalAccountOperationsHonorDeadlineWhileLocksAreHeld(t *testing.T) {
	service, tradeStore := logicalAccountServiceFixture(t)
	unlockExecution := tradeStore.LockLogicalAccountExecution("space-1", "logical-1")
	defer unlockExecution()

	for name, call := range map[string]func(context.Context) error{
		"AddMember": func(ctx context.Context) error {
			return addMember(ctx, service, AddMemberCommand{SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-b", Enabled: true})
		},
		"Resume": func(ctx context.Context) error {
			_, _, err := service.Resume(ctx, "space-1", "logical-1")
			return err
		},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		done := make(chan error, 1)
		go func() { done <- call(ctx) }()
		select {
		case err := <-done:
			require.ErrorIs(t, err, context.DeadlineExceeded, name)
		case <-time.After(2 * time.Second):
			t.Fatalf("%s kept waiting for a held lock past the deadline", name)
		}
		cancel()
	}
	// 超时返回后没有遗留逻辑账户锁/成员锁：其它调用可以立刻拿到
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	unlock, err := tradeStore.LockLogicalAccountContext(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	unlock()
	unlockMembership, err := tradeStore.LockLogicalAccountMembershipContext(ctx)
	require.NoError(t, err)
	unlockMembership()
}

// acceptSessionTarget 写入 runner-1/session-1 的一份有效目标（目标数量 1 张 BTC-USDT-SWAP）。
func acceptSessionTarget(t *testing.T, tradeStore *store.Store, targetID string) {
	t.Helper()
	_, _, err := tradeStore.AcceptLogicalAccountTarget(context.Background(), store.LogicalAccountTargetRecord{
		SpaceID: "space-1", LogicalAccountID: "logical-1", TargetID: targetID,
		InstanceID: "runner-1", SessionID: "session-1", SessionStartedAt: 1000, StrategyID: "strategy-1",
		BarEndTime: 2_000, ValidUntil: time.Now().Add(time.Hour).UnixMilli(),
		Status: "PENDING", AcceptedAt: 2_000,
		Targets: []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "1"}},
	})
	require.NoError(t, err)
}

// 有持仓的启用成员也能改优先级：只改优先级不做敞口检查。
func TestAddMemberChangesPriorityOfExposedMember(t *testing.T) {
	service, tradeStore := logicalAccountServiceFixture(t)
	ctx := context.Background()
	require.NoError(t, tradeStore.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpsertPosition(store.PositionRecord{
			SpaceID: "space-1", TradingAccountID: "account-b", ExchangeSymbol: "BTCUSDT", PositionSide: "NET",
			SignedQuantity: "1", Leverage: "5", MarginMode: "CROSS", ExchangeUpdatedAt: 1_900,
		})
	}))
	require.NoError(t, addMember(ctx, service, AddMemberCommand{
		SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-b",
		Enabled: true, Priority: 2, AdoptExistingExposure: true,
	}))

	require.NoError(t, addMember(ctx, service, AddMemberCommand{
		SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-b", Enabled: true, Priority: 9,
	}))

	members, err := tradeStore.ListLogicalAccountMembers(ctx, "space-1", "logical-1", true)
	require.NoError(t, err)
	found := false
	for _, member := range members {
		if member.TradingAccountID == "account-b" {
			found = member.Priority == 9
		}
	}
	require.True(t, found)
}

type failingAccountSyncer struct{ calls *int }

func (s failingAccountSyncer) SyncAccount(context.Context, string) error {
	*s.calls++
	return errors.New("sync must not run")
}

func TestAddMemberRejectsAccountOwnedByAnotherGroupBeforeSync(t *testing.T) {
	service, tradeStore := logicalAccountServiceFixture(t)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-2", Name: "other",
			ExecutionMode: "LIVE", SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "configure",
		})
	}))
	calls := 0
	service.Syncer = failingAccountSyncer{calls: &calls}
	for _, enabled := range []bool{true, false} {
		err := addMember(context.Background(), service, AddMemberCommand{
			SpaceID: "space-1", LogicalAccountID: "logical-2",
			TradingAccountID: "account-a", Enabled: enabled,
		})
		require.ErrorIs(t, err, logicaldomain.ErrMemberElsewhere)
	}
	require.Zero(t, calls)
}
