package marginmonitor

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	operatorapp "github.com/mooyang-code/moox/modules/trade/internal/application/operator"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/fundsettings"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func defaults(t *testing.T) fundsettings.Settings {
	t.Helper()
	settings, _, err := fundsettings.ParseLenient("")
	require.NoError(t, err)
	return settings
}

// standardAccount 是币安普通账户：交易所总权益（合约接口的 totalMarginBalance）写在 ExchangeEquity 里。
func standardAccount(exchangeEquity string) store.TradingAccountRecord {
	return store.TradingAccountRecord{
		TradingAccountID: "std", Name: "std", Exchange: "BINANCE", Markets: []string{"SPOT", "SWAP"}, ExecutionMode: "LIVE",
		AccountMode: "BINANCE_STANDARD", Environment: "PRODUCTION", SettlementAsset: "USDT", Status: "ENABLED", Ready: true,
		Snapshot: store.TradingAccountSnapshot{ExchangeEquity: exchangeEquity, Balances: []store.AssetBalance{
			{Wallet: "FUTURES", Asset: "USDT", Total: "100", Available: "100"},
			{Wallet: "SPOT", Asset: "USDT", Total: "9999", Available: "9999"},
		}},
	}
}

func position(quantity, mark string) store.PositionRecord {
	return store.PositionRecord{MarketType: "SWAP", ExchangeSymbol: "BTCUSDT", SignedQuantity: quantity, MarkPrice: mark, EntryPrice: "1"}
}

// 普通账户：总权益 ÷ 持仓名义，等于阈值也触发（不高于）。
func TestEvaluateStandardAccountTriggersAtOrBelowThreshold(t *testing.T) {
	settings := defaults(t)                                                           // 阈值 0.5
	positions := []store.PositionRecord{position("10", "100"), position("-5", "100")} // 名义 1500

	reading, ok := Evaluate(standardAccount("750"), positions, settings)
	require.True(t, ok)
	require.False(t, reading.Unified)
	require.Equal(t, "0.5", reading.Value.String())
	require.True(t, reading.Triggered, "等于阈值触发")

	reading, ok = Evaluate(standardAccount("750.01"), positions, settings)
	require.True(t, ok)
	require.False(t, reading.Triggered, "略高于阈值不触发")

	// 没有标记价时用开仓均价算名义。
	noMark := store.PositionRecord{MarketType: "SWAP", SignedQuantity: "10", EntryPrice: "100"}
	reading, _ = Evaluate(standardAccount("400"), []store.PositionRecord{noMark}, settings)
	require.Equal(t, "0.4", reading.Value.String())
}

// 多资产模式：保证金币（BNB、USDC 等）手工划到合约钱包后，总权益里已经包含它们，分子不能只看钱包里的结算币。
// 合约钱包 USDT 只有 100，但总权益 10100、持仓名义 300：比值远高于阈值，不能误触发。
func TestEvaluateStandardAccountCountsMultiAssetCollateral(t *testing.T) {
	reading, ok := Evaluate(standardAccount("10100"), []store.PositionRecord{position("3", "100")}, defaults(t))
	require.True(t, ok)
	require.False(t, reading.Triggered)
	require.Greater(t, reading.Value.Float64(), 30.0)
}

// 数据缺失不触发：没有持仓、没有总权益、没有维持保证金。
func TestEvaluateWithMissingDataDoesNotTrigger(t *testing.T) {
	settings := defaults(t)
	_, ok := Evaluate(standardAccount("1"), nil, settings)
	require.False(t, ok, "没有持仓：分母为零")
	_, ok = Evaluate(standardAccount("1"), []store.PositionRecord{position("0", "100")}, settings)
	require.False(t, ok)
	_, ok = Evaluate(standardAccount(""), []store.PositionRecord{position("10", "100")}, settings)
	require.False(t, ok, "总权益没填：不能凭空判断，也不拿现货快照的权益兜底")

	unified := standardAccount("1000")
	unified.AccountMode = "BINANCE_PORTFOLIO_MARGIN"
	_, ok = Evaluate(unified, nil, settings)
	require.False(t, ok, "维持保证金为零（或还没有数据）不触发")
}

// 统一账户：uniMMR 低于阈值才触发，等于阈值不触发。
func TestEvaluateUnifiedAccountTriggersOnlyBelowThreshold(t *testing.T) {
	settings := defaults(t) // 阈值 1.5
	account := standardAccount("")
	account.AccountMode = "BINANCE_PORTFOLIO_MARGIN"
	account.Snapshot.MaintenanceMargin = "100"

	for equity, want := range map[string]bool{"149.99": true, "150": false, "151": false} {
		account.Snapshot.ExchangeEquity = equity
		reading, ok := Evaluate(account, nil, settings)
		require.True(t, ok)
		require.True(t, reading.Unified)
		require.Equal(t, want, reading.Triggered, equity)
	}
}

func TestEvaluateUsesTheConfiguredThresholds(t *testing.T) {
	settings, _, err := fundsettings.ParseLenient(`{"margin_ratio_threshold":"0.3","unified_mmr_threshold":"2"}`)
	require.NoError(t, err)
	reading, _ := Evaluate(standardAccount("750"), []store.PositionRecord{position("15", "100")}, settings)
	require.False(t, reading.Triggered, "0.5 高于配置的阈值 0.3")
	unified := standardAccount("180")
	unified.AccountMode = "BINANCE_PORTFOLIO_MARGIN"
	unified.Snapshot.MaintenanceMargin = "100"
	reading, _ = Evaluate(unified, nil, settings)
	require.True(t, reading.Triggered, "1.8 低于配置的阈值 2")
}

func TestAppliesOnlyBinanceProductionLiveAccountsWithAKnownType(t *testing.T) {
	account := standardAccount("1")
	require.True(t, Applies(account))
	require.Empty(t, unsupportedNote(account))
	for name, mutate := range map[string]func(*store.TradingAccountRecord){
		"模拟盘":  func(a *store.TradingAccountRecord) { a.ExecutionMode = "PAPER" },
		"测试网":  func(a *store.TradingAccountRecord) { a.Environment = "TESTNET" },
		"未就绪":  func(a *store.TradingAccountRecord) { a.Ready = false },
		"已停用":  func(a *store.TradingAccountRecord) { a.Status = "DISABLED" },
		"没有永续": func(a *store.TradingAccountRecord) { a.Markets = []string{"SPOT"} },
	} {
		changed := account
		mutate(&changed)
		require.False(t, Applies(changed), name)
		require.Empty(t, unsupportedNote(changed), name+"：不在监控范围，也不用提示")
	}

	// OKX 暂不接入；账户类型没有检测写入：都不监控，但要说明原因。
	okx := account
	okx.Exchange, okx.AccountMode = "OKX", "OKX_UNIFIED"
	require.False(t, Applies(okx))
	require.Contains(t, unsupportedNote(okx), "OKX")
	unknown := account
	unknown.AccountMode = ""
	require.False(t, Applies(unknown))
	require.Contains(t, unsupportedNote(unknown), "账户类型")
	unknownOKX := okx
	unknownOKX.AccountMode = ""
	require.Contains(t, unsupportedNote(unknownOKX), "OKX")
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
	pauses  []string
	collect []string
	pending bool
	// busy 为真时 Reduce 返回“组合忙”（例如有下架清仓在跑）。
	busy bool
	// offset 是监控的时钟相对真实时间的偏移：操作记录的创建时间是数据库的真实时间。
	offset time.Duration
}

// newFixture 建一个币安普通账户的实盘组合，持仓 10 个、名义 1000；exchangeEquity 是交易所总权益。
func newFixture(t *testing.T, fundSettings, exchangeEquity string) *fixture {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tradeStore.Close()) })
	account := standardAccount(exchangeEquity)
	account.SpaceID, account.TradingAccountID, account.Name = "space-1", "account-1", "币安主账户"
	account.CredentialSecretID = "secret-1"
	now := time.Now()
	account.LastSyncAt, account.SnapshotSourceTime = now.UnixMilli(), now.UnixMilli()
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
		Store: tradeStore, Now: func() time.Time { return time.Now().Add(f.offset) },
		Reduce: func(_ context.Context, spaceID, actionID, logicalID string, ratio shared.Decimal, reason string) (store.OperatorActionRecord, error) {
			if f.busy {
				return store.OperatorActionRecord{}, operatorapp.ErrLogicalAccountBusy
			}
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
		Pause: func(_ context.Context, spaceID, logicalID, reason string) error {
			f.pauses = append(f.pauses, reason)
			return tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
				return tx.SetLogicalAccountAutomation(spaceID, logicalID, "PAUSED", reason)
			})
		},
		Collect: func(_ context.Context, _, logicalID, actionID string, accounts []store.TradingAccountRecord) (bool, error) {
			f.collect = append(f.collect, logicalID+"|"+actionID+"|"+accounts[0].TradingAccountID)
			return f.pending, nil
		},
	}
	return f
}

// restart 模拟 Trade 重启：新的监控实例，没有任何内存状态，只剩库里的东西。
func (f *fixture) restart() {
	old := f.service
	f.service = &Service{Store: old.Store, Now: old.Now, Reduce: old.Reduce, Pause: old.Pause, Collect: old.Collect}
}

func (f *fixture) run() {
	f.t.Helper()
	require.NoError(f.t, f.service.RunAll(context.Background()))
}

// advance 把监控的时钟往后拨，同时刷新账户快照时间（快照太旧不判断）。
func (f *fixture) advance(d time.Duration) {
	f.t.Helper()
	f.offset += d
	require.NoError(f.t, f.store.DBForTest().Exec(`UPDATE t_trading_accounts SET c_last_sync_at = ?`, time.Now().Add(f.offset).UnixMilli()).Error)
}

func (f *fixture) logical() store.LogicalAccountRecord {
	f.t.Helper()
	record, err := f.store.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(f.t, err)
	return record
}

// 触发：暂停组合、发起默认 10% 的减仓，动作号带风控前缀；暂停原因是固定文字（不带比值），算保证金风控的暂停。
func TestRunTriggersReduceAndPausesOnce(t *testing.T) {
	f := newFixture(t, "", "400") // 总权益 400 ÷ 名义 1000 = 0.4 ≤ 0.5
	f.run()

	require.Len(t, f.reduces, 1)
	call := f.reduces[0]
	require.Equal(t, "space-1", call.spaceID)
	require.Equal(t, "logical-1", call.logicalID)
	require.Contains(t, call.actionID, ActionIDPrefix+"logical-1-")
	require.Equal(t, "0.1", call.ratio.String())
	require.Equal(t, store.MarginPauseReason(), call.reason, "暂停原因是固定文字，不带变化的数字")
	require.True(t, store.IsAutoPauseReason(call.reason))
	require.True(t, store.IsMarginPauseReason(f.logical().PauseReason))

	// 组合已经被暂停、又没有恢复过：下一轮不再触发（不会每个周期再减 10%）。
	f.run()
	f.run()
	require.Len(t, f.reduces, 1)
}

func TestRunDoesNotTriggerWhenSafeOrDisabledOrStale(t *testing.T) {
	f := newFixture(t, "", "600") // 0.6 > 0.5
	f.run()
	require.Empty(t, f.reduces)

	f = newFixture(t, `{"margin_monitor_disabled":true}`, "400")
	f.run()
	require.Empty(t, f.reduces)

	f = newFixture(t, "", "400")
	f.offset = 10 * time.Minute // 快照太久没更新：数据不可信
	f.run()
	require.Empty(t, f.reduces)

	f = newFixture(t, `{"margin_ratio_threshold":"0.3"}`, "400") // 0.4 高于配置的 0.3
	f.run()
	require.Empty(t, f.reduces)
}

// 多资产模式下保证金币手工划到合约钱包：合约钱包里结算币只有 100，但总权益够，不能误触发。
func TestRunDoesNotTriggerOnMultiAssetCollateral(t *testing.T) {
	f := newFixture(t, "", "10100")
	f.run()
	require.Empty(t, f.reduces)
}

// 整组清仓、减仓在跑时它们自己会暂停组合，这一轮不叠加；人工单这类动作不挡监控。
func TestRunSkipsOnlyWhileFlattenOrReduceIsRunning(t *testing.T) {
	create := func(f *fixture, id, actionType string) {
		_, _, err := f.store.CreateOperatorAction(context.Background(), store.OperatorActionRecord{
			SpaceID: "space-1", ActionID: id, LogicalAccountID: "logical-1", ActionType: actionType,
			Reason: "人工", RequestJSON: `{"logical_account_id":"logical-1"}`, Status: "RUNNING",
		})
		require.NoError(t, err)
	}
	for _, actionType := range []string{"FLATTEN", "REDUCE"} {
		f := newFixture(t, "", "400")
		create(f, "manual-"+actionType, actionType)
		f.run()
		require.Empty(t, f.reduces, actionType)
	}
	for _, actionType := range []string{"MANUAL_ORDER", "CANCEL_ORDER", "SUBMIT_ORDER"} {
		f := newFixture(t, "", "400")
		create(f, "manual-"+actionType, actionType)
		f.run()
		require.Len(t, f.reduces, 1, actionType+" 不挡保证金率监控")
	}
}

// 有下架清仓在跑时减仓排不进去（返回忙）：先把组合暂停停住并告警，下一轮再试减仓；组合已经暂停了就不重复暂停。
func TestRunPausesFirstWhenTheReduceCannotStart(t *testing.T) {
	f := newFixture(t, "", "400")
	f.busy = true
	f.run()
	require.Empty(t, f.reduces)
	require.Equal(t, []string{store.MarginPausePendingReason()}, f.pauses, "先暂停时不能写已减仓")
	require.Equal(t, "PAUSED", f.logical().AutomationState)

	f.run()
	require.Len(t, f.pauses, 1, "已经暂停，不重复暂停")

	f.busy = false
	f.run()
	require.Len(t, f.reduces, 1, "下架清仓结束后，下一轮把减仓补上")
}

// resume 模拟用户恢复组合：库里记下恢复时间（比减仓动作晚一秒以上）。
func (f *fixture) resume() {
	f.t.Helper()
	require.NoError(f.t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.SetLogicalAccountAutomation("space-1", "logical-1", "ACTIVE", "")
	}))
	require.NoError(f.t, f.store.DBForTest().Exec(`UPDATE t_logical_accounts SET c_resumed_at = ?`, time.Now().Add(5*time.Second).UnixMilli()).Error)
}

// 暂停期间用户人工下单、撤单、清仓会改写暂停原因：比值仍低也不会再自动减一次；用户恢复之后重新监控。
func TestRunDoesNotReduceAgainAfterTheManualOverrideOfThePauseReason(t *testing.T) {
	f := newFixture(t, "", "400")
	f.run()
	require.Len(t, f.reduces, 1)

	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.SetLogicalAccountAutomation("space-1", "logical-1", "PAUSED", "人工清仓")
	}))
	f.run()
	require.Len(t, f.reduces, 1, "暂停原因被人工操作改写后不再触发")

	// 用户确认后恢复（第一次减仓早已做完）：比值仍低就再触发。
	require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_operator_actions SET c_status = 'COMPLETED'`).Error)
	f.resume()
	f.run()
	require.Len(t, f.reduces, 2)
}

// 重启不丢保护（M1）：几天前触发过风控减仓、用户已恢复，之后用户人工暂停；Trade 重启（监控的进程内存清零）；价格大跌比值很低：
// “恢复过”记在库里，所以照样触发，不能因为历史上有过风控减仓就漏掉。
func TestRestartKeepsTheProtectionAfterAnEarlierMarginReduceWasResumed(t *testing.T) {
	f := newFixture(t, "", "400")
	f.run()
	require.Len(t, f.reduces, 1)
	require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_operator_actions SET c_status = 'COMPLETED'`).Error)
	f.resume()
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.SetLogicalAccountAutomation("space-1", "logical-1", "PAUSED", "人工暂停")
	}))

	f.restart()
	f.run()

	require.Len(t, f.reduces, 2, "重启后照样监控")
}

// 重启不丢待补的减仓（M2）：有下架清仓在跑，减仓排不进去，先暂停；Trade 重启；下架清仓结束后要把减仓补上，
// 而且先暂停时的原因不能写“已减仓”（用户会以为已经减过）。
func TestRestartStillCompletesAReduceThatWasPendingBehindADelistFlatten(t *testing.T) {
	f := newFixture(t, "", "400")
	f.busy = true
	f.run()
	require.Empty(t, f.reduces)
	require.Equal(t, store.MarginPausePendingReason(), f.logical().PauseReason)
	require.NotContains(t, f.logical().PauseReason, "已暂停并按设置的比例减仓", "减仓还没做，不能说已减仓")
	require.True(t, store.IsMarginPauseReason(f.logical().PauseReason), "仍然算保证金风控暂停（关注项照报）")

	f.restart()
	f.busy = false
	f.run()

	require.Len(t, f.reduces, 1, "下架清仓结束后补上减仓")
	require.Equal(t, store.MarginPauseReason(), f.logical().PauseReason, "减仓发出后原因换成已减仓")
}

// 人工暂停的组合照样监控（价格不会因为组合暂停就停下）。
func TestRunMonitorsManuallyPausedGroups(t *testing.T) {
	f := newFixture(t, "", "400")
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.SetLogicalAccountAutomation("space-1", "logical-1", "PAUSED", "人工暂停")
	}))
	f.run()
	require.Len(t, f.reduces, 1)
}

// 模拟盘、OKX、账户类型未知的账户不监控。
func TestRunIgnoresPaperOkxAndUnknownTypeAccounts(t *testing.T) {
	f := newFixture(t, "", "400")
	require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_logical_accounts SET c_execution_mode = 'PAPER'`).Error)
	f.run()
	require.Empty(t, f.reduces)

	f = newFixture(t, "", "400")
	require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_trading_accounts SET c_account_mode = ''`).Error)
	f.run()
	require.Empty(t, f.reduces, "账户类型还没有检测写入：不监控（只告警一次）")
}

// 减仓做完后归集一次：减仓还在跑时不归集；pending 时下一轮再调；账户没就绪不记完成；做完后不再重复。
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

	require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_trading_accounts SET c_ready = 0`).Error)
	f.run()
	require.Empty(t, f.collect, "账户这一分钟没就绪：先不归集")
	require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_trading_accounts SET c_ready = 1`).Error)

	f.pending = true
	f.run()
	require.Equal(t, []string{"logical-1|" + actionID + "|account-1"}, f.collect, "没就绪那一轮没有记完成，现在补上")
	f.run()
	require.Len(t, f.collect, 2, "归集还没完成，下一轮继续")

	f.pending = false
	f.run()
	require.Len(t, f.collect, 3)
	f.run()
	require.Len(t, f.collect, 3, "归集完成后不再重复")
	require.Len(t, f.reduces, 1, "组合仍是暂停的，不会再次触发")
}

// 组合后来因为别的原因自动暂停（暂停原因不是保证金风控）：不会对很早以前的风控减仓再调归集。
func TestCollectOnlyFollowsAMarginPause(t *testing.T) {
	f := newFixture(t, "", "400")
	f.run()
	require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_operator_actions SET c_status = 'COMPLETED'`).Error)
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.SetLogicalAccountAutomation("space-1", "logical-1", "PAUSED", store.AutoPauseReason("发现外部订单"))
	}))
	f.run()
	require.Empty(t, f.collect)
}

// 划转还没接入（没配 Collect，或返回“不可用”）：只告警一次，不记为失败、不重复调用。
func TestCollectWithoutTransferSupportOnlyWarns(t *testing.T) {
	f := newFixture(t, "", "400")
	f.service.Collect = nil
	f.run()
	require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_operator_actions SET c_status = 'PARTIAL'`).Error)
	f.run()
	f.run()

	f = newFixture(t, "", "400")
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
