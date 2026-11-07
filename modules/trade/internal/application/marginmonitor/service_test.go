package marginmonitor

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/fundsettings"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

var testNow = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

func defaults(t *testing.T) fundsettings.Settings {
	t.Helper()
	settings, _, err := fundsettings.ParseLenient("")
	require.NoError(t, err)
	return settings
}

func standardAccount(walletUSDT string) store.TradingAccountRecord {
	return store.TradingAccountRecord{
		TradingAccountID: "std", Name: "std", Exchange: "BINANCE", Markets: []string{"SPOT", "SWAP"}, ExecutionMode: "LIVE",
		AccountMode: "BINANCE_STANDARD", Environment: "PRODUCTION", SettlementAsset: "USDT", Status: "ENABLED", Ready: true,
		Snapshot: store.TradingAccountSnapshot{Balances: []store.AssetBalance{
			{Wallet: "FUTURES", Asset: "USDT", Total: walletUSDT, Available: walletUSDT},
			{Wallet: "SPOT", Asset: "USDT", Total: "9999", Available: "9999"},
		}},
	}
}

func position(quantity, mark, upl string) store.PositionRecord {
	return store.PositionRecord{MarketType: "SWAP", ExchangeSymbol: "BTCUSDT", SignedQuantity: quantity, MarkPrice: mark, EntryPrice: "1", UnrealizedPnL: upl}
}

// 普通账户：合约钱包权益 ÷ 持仓名义，等于阈值也触发（不高于）。现货钱包里的 USDT 不算。
func TestEvaluateStandardAccountTriggersAtOrBelowThreshold(t *testing.T) {
	settings := defaults(t)                                                                     // 阈值 0.5
	positions := []store.PositionRecord{position("10", "100", "0"), position("-5", "100", "0")} // 名义 1500

	reading, ok := Evaluate(standardAccount("750"), positions, settings)
	require.True(t, ok)
	require.False(t, reading.Unified)
	require.Equal(t, "0.5", reading.Value.String())
	require.True(t, reading.Triggered, "等于阈值触发")

	reading, ok = Evaluate(standardAccount("750.01"), positions, settings)
	require.True(t, ok)
	require.False(t, reading.Triggered, "略高于阈值不触发")

	reading, _ = Evaluate(standardAccount("800"), []store.PositionRecord{position("10", "100", "-60"), position("-5", "100", "-20")}, settings)
	require.Equal(t, "0.48", reading.Value.String(), "权益 = 800 + (-80) = 720，名义 1500")
	require.True(t, reading.Triggered)
}

// 数据缺失不触发：没有持仓、持仓名义为零。
func TestEvaluateWithoutPositionsOrMaintenanceDoesNotTrigger(t *testing.T) {
	settings := defaults(t)
	_, ok := Evaluate(standardAccount("1"), nil, settings)
	require.False(t, ok, "没有持仓：分母为零")
	_, ok = Evaluate(standardAccount("1"), []store.PositionRecord{position("0", "100", "0")}, settings)
	require.False(t, ok)

	unified := standardAccount("0")
	unified.AccountMode = "BINANCE_PORTFOLIO_MARGIN"
	unified.Snapshot.ExchangeEquity = "1000"
	_, ok = Evaluate(unified, nil, settings)
	require.False(t, ok, "维持保证金为零（或还没有数据）不触发")
}

// 统一账户：uniMMR 低于阈值才触发，等于阈值不触发。
func TestEvaluateUnifiedAccountTriggersOnlyBelowThreshold(t *testing.T) {
	settings := defaults(t) // 阈值 1.5
	account := standardAccount("0")
	account.AccountMode = "BINANCE_PORTFOLIO_MARGIN"
	account.Snapshot.MaintenanceMargin = "100"

	for equity, want := range map[string]bool{"149.99": true, "150": false, "151": false} {
		account.Snapshot.ExchangeEquity = equity
		reading, ok := Evaluate(account, nil, settings)
		require.True(t, ok)
		require.True(t, reading.Unified)
		require.Equal(t, want, reading.Triggered, equity)
	}
	// 交易所没给总权益时用统一公式的权益；OKX 统一账户同一口径。
	account.AccountMode = "OKX_UNIFIED"
	account.Snapshot.ExchangeEquity, account.Snapshot.Equity = "", "120"
	reading, ok := Evaluate(account, nil, settings)
	require.True(t, ok)
	require.True(t, reading.Triggered)
}

func TestEvaluateUsesTheConfiguredThresholds(t *testing.T) {
	settings, _, err := fundsettings.ParseLenient(`{"margin_ratio_threshold":"0.3","unified_mmr_threshold":"2"}`)
	require.NoError(t, err)
	reading, _ := Evaluate(standardAccount("750"), []store.PositionRecord{position("15", "100", "0")}, settings)
	require.False(t, reading.Triggered, "0.5 高于配置的阈值 0.3")
	unified := standardAccount("0")
	unified.AccountMode = "BINANCE_PORTFOLIO_MARGIN"
	unified.Snapshot.ExchangeEquity, unified.Snapshot.MaintenanceMargin = "180", "100"
	reading, _ = Evaluate(unified, nil, settings)
	require.True(t, reading.Triggered, "1.8 低于配置的阈值 2")
}

func TestApplies(t *testing.T) {
	account := standardAccount("1")
	require.True(t, Applies(account))
	for name, mutate := range map[string]func(*store.TradingAccountRecord){
		"模拟盘":  func(a *store.TradingAccountRecord) { a.ExecutionMode = "PAPER" },
		"测试网":  func(a *store.TradingAccountRecord) { a.Environment = "TESTNET" },
		"未就绪":  func(a *store.TradingAccountRecord) { a.Ready = false },
		"已停用":  func(a *store.TradingAccountRecord) { a.Status = "DISABLED" },
		"没有永续": func(a *store.TradingAccountRecord) { a.Markets = []string{"SPOT"} },
		"类型未知": func(a *store.TradingAccountRecord) { a.AccountMode = "" },
	} {
		changed := account
		mutate(&changed)
		require.False(t, Applies(changed), name)
	}
}

// ---- 触发流程 ----

type reduceCall struct {
	spaceID, actionID, logicalID string
	ratio                        shared.Decimal
	reason                       string
}

type fixture struct {
	t       *testing.T
	store   *store.Store
	service *Service
	reduces []reduceCall
	collect []string
	pending bool
}

func newFixture(t *testing.T, fundSettings, walletUSDT string) *fixture {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tradeStore.Close()) })
	account := standardAccount(walletUSDT)
	account.SpaceID, account.TradingAccountID, account.Name = "space-1", "account-1", "币安主账户"
	account.CredentialSecretID = "secret-1"
	account.LastSyncAt, account.SnapshotSourceTime = testNow.UnixMilli(), testNow.UnixMilli()
	account.MarginMode = "CROSS"
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(account); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "实盘组合", ExecutionMode: "LIVE", SettlementAsset: "USDT",
			OwnerInstanceID: "i", OwnerSessionID: "s", FundSettingsJSON: fundSettings, AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-1", Enabled: true, Priority: 1,
		}); err != nil {
			return err
		}
		if err := tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "BINANCE", Environment: "PRODUCTION", MarketType: "SWAP", ExchangeSymbol: "BTCUSDT", InstrumentID: "BTC-USDT-SWAP",
			BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT", Linear: true, ContractValue: "1", ContractValueAsset: "BTC",
			PriceTick: "0.1", ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", Status: "TRADING",
		}); err != nil {
			return err
		}
		if err := tx.UpsertPosition(store.PositionRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", MarketType: "SWAP", ExchangeSymbol: "BTCUSDT", PositionSide: "NET",
			SignedQuantity: "10", EntryPrice: "100", MarkPrice: "100", Leverage: "5", MarginMode: "CROSS", UnrealizedPnL: "0", ExchangeUpdatedAt: 1,
		}); err != nil {
			return err
		}
		return tx.SetLogicalAccountAutomation("space-1", "logical-1", "ACTIVE", "")
	}))
	f := &fixture{t: t, store: tradeStore}
	f.service = &Service{
		Store: tradeStore, Now: func() time.Time { return testNow },
		Reduce: func(_ context.Context, spaceID, actionID, logicalID string, ratio shared.Decimal, reason string) (store.OperatorActionRecord, error) {
			f.reduces = append(f.reduces, reduceCall{spaceID, actionID, logicalID, ratio, reason})
			// 和真实的 REDUCE 一样：先暂停组合、写下操作记录。
			var record store.OperatorActionRecord
			err := tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
				if err := tx.SetLogicalAccountAutomation(spaceID, logicalID, "PAUSED", reason); err != nil {
					return err
				}
				var ensureErr error
				record, _, ensureErr = tx.EnsureOperatorAction(store.OperatorActionRecord{
					SpaceID: spaceID, ActionID: actionID, LogicalAccountID: logicalID, ActionType: "REDUCE", Reason: reason,
					RequestJSON: `{"logical_account_id":"logical-1","reduce_ratio":"0.1"}`, Status: "RUNNING",
				})
				return ensureErr
			})
			return record, err
		},
		Collect: func(_ context.Context, _, logicalID, actionID string, accounts []store.TradingAccountRecord) (bool, error) {
			f.collect = append(f.collect, logicalID+"|"+actionID+"|"+accounts[0].TradingAccountID)
			return f.pending, nil
		},
	}
	return f
}

func (f *fixture) run() {
	f.t.Helper()
	require.NoError(f.t, f.service.RunAll(context.Background()))
}

// 触发：暂停组合、发起默认 10% 的减仓，动作号带风控前缀，原因是“自动暂停：…”（会进自动暂停告警）。
func TestRunTriggersReduceAndPausesOnce(t *testing.T) {
	f := newFixture(t, "", "400") // 权益 400 ÷ 名义 1000 = 0.4 ≤ 0.5
	f.run()

	require.Len(t, f.reduces, 1)
	call := f.reduces[0]
	require.Equal(t, "space-1", call.spaceID)
	require.Equal(t, "logical-1", call.logicalID)
	require.True(t, strings.HasPrefix(call.actionID, ActionIDPrefix+"logical-1-"))
	require.Equal(t, "0.1", call.ratio.String())
	require.True(t, store.IsAutoPauseReason(call.reason))
	require.Contains(t, call.reason, "保证金率过低")
	require.Contains(t, call.reason, "币安主账户")

	// 组合已经被暂停：下一轮不再触发（不会每个周期再减 10%）。
	f.run()
	f.run()
	require.Len(t, f.reduces, 1)
}

func TestRunDoesNotTriggerWhenSafeOrDisabledOrStale(t *testing.T) {
	// 安全：0.6 > 0.5。
	f := newFixture(t, "", "600")
	f.run()
	require.Empty(t, f.reduces)

	// 关闭监控。
	f = newFixture(t, `{"margin_monitor_disabled":true}`, "400")
	f.run()
	require.Empty(t, f.reduces)

	// 快照太久没更新：数据不可信，不触发。
	f = newFixture(t, "", "400")
	f.service.Now = func() time.Time { return testNow.Add(10 * time.Minute) }
	f.run()
	require.Empty(t, f.reduces)

	// 自定义阈值生效：0.4 高于配置的 0.3。
	f = newFixture(t, `{"margin_ratio_threshold":"0.3"}`, "400")
	f.run()
	require.Empty(t, f.reduces)
}

// 已有清仓、减仓在跑时不叠加发起。
func TestRunSkipsWhileAnotherOperatorActionIsRunning(t *testing.T) {
	f := newFixture(t, "", "400")
	_, _, err := f.store.CreateOperatorAction(context.Background(), store.OperatorActionRecord{
		SpaceID: "space-1", ActionID: "manual-flatten", LogicalAccountID: "logical-1", ActionType: "FLATTEN",
		Reason: "人工清仓", RequestJSON: `{"logical_account_id":"logical-1"}`, Status: "RUNNING",
	})
	require.NoError(t, err)
	f.run()
	require.Empty(t, f.reduces)
}

// 模拟盘和测试网不监控。
func TestRunIgnoresPaperLogicalAccounts(t *testing.T) {
	f := newFixture(t, "", "400")
	require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_logical_accounts SET c_execution_mode = 'PAPER'`).Error)
	f.run()
	require.Empty(t, f.reduces)
}

// 减仓做完后归集一次：减仓还在跑时不归集；pending 时下一轮再调；做完后不再重复。
func TestCollectRunsAfterTheReduceFinishes(t *testing.T) {
	f := newFixture(t, "", "400")
	f.run()
	require.Len(t, f.reduces, 1)
	actionID := f.reduces[0].actionID

	f.run()
	require.Empty(t, f.collect, "减仓还在运行，先不归集")

	complete := func(status string) {
		require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_operator_actions SET c_status = ? WHERE c_action_id = ?`, status, actionID).Error)
	}
	complete("COMPLETED")
	f.pending = true
	f.run()
	require.Equal(t, []string{"logical-1|" + actionID + "|account-1"}, f.collect)
	f.run()
	require.Len(t, f.collect, 2, "归集还没完成，下一轮继续")

	f.pending = false
	f.run()
	require.Len(t, f.collect, 3)
	f.run()
	require.Len(t, f.collect, 3, "归集完成后不再重复")
	require.Len(t, f.reduces, 1, "组合仍是暂停的，不会再次触发")
}

// 划转还没接入时只告警一次、不报错，组合照样暂停等用户处理。
func TestCollectWithoutTransferSupportOnlyWarns(t *testing.T) {
	f := newFixture(t, "", "400")
	f.service.Collect = nil
	f.run()
	require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_operator_actions SET c_status = 'PARTIAL'`).Error)
	f.run()
	f.run()
}

// 接入了划转入口、但具体账户的划转还没实现（返回“不可用”）：和没接入一样，只告警一次，不记为失败、不重复调用。
func TestCollectTreatsUnavailableTransferAsDone(t *testing.T) {
	f := newFixture(t, "", "400")
	calls := 0
	f.service.Collect = func(context.Context, string, string, string, []store.TradingAccountRecord) (bool, error) {
		calls++
		return false, ErrTransferUnavailable
	}
	f.run()
	require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_operator_actions SET c_status = 'COMPLETED'`).Error)
	f.run()
	f.run()
	require.Equal(t, 1, calls)
}

// 人工暂停的组合照样监控（价格不会因为组合暂停就停下）；只有上一次由保证金风控暂停、还没恢复的组合才不再触发。
func TestRunMonitorsManuallyPausedGroupsButNotMarginPausedOnes(t *testing.T) {
	f := newFixture(t, "", "400")
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.SetLogicalAccountAutomation("space-1", "logical-1", "PAUSED", "人工暂停")
	}))
	f.run()
	require.Len(t, f.reduces, 1, "人工暂停的组合保证金率太低，照样触发")

	// 风控暂停之后（暂停原因带标记）不再触发。
	f.run()
	require.Len(t, f.reduces, 1)
}
