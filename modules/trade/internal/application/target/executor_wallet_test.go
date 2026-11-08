package target

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/wallet"
	"github.com/mooyang-code/moox/modules/trade/internal/application/wallet/wallettest"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// 集成测试：把真实的 wallet.Service（经 NewWalletTransfer）接进执行器，交易所用假交易所（余额、划转记录可控），
// 覆盖第 4 阶段的整条链路：划转、等到账、等现货钱包快照、买入，以及“划转刚完成就重启”。

func newWalletIntegration(t *testing.T) (*targetFixture, *wallettest.Exchange, func() *Executor) {
	t.Helper()
	f := newMixedExecutorFixture(t)
	require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_trading_accounts SET c_execution_mode = 'LIVE', c_live_environment = 'PRODUCTION',
		c_credential_secret_id = 'secret-1', c_account_mode = 'BINANCE_STANDARD'`).Error)
	f.setSpotBalance("ETH", "0")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SPOT", Quantity: "0.01"}})
	f.now = time.Now().UTC()
	fake := &wallettest.Exchange{Spot: "10", Futures: "100000", Ask: "50000"}
	// 每次调用都新建服务和执行器：等价于进程重启，状态只在库里（划转表）。
	newExecutor := func() *Executor {
		executor := f.executor()
		executor.Transfers = NewWalletTransfer(&wallet.Service{Store: f.store, Adapters: wallettest.Adapters{"account-a": fake}})
		return executor
	}
	return f, fake, newExecutor
}

// markSpotSnapshot 让现货钱包的存储快照更新时间停在 at（交易所时钟）。
func markSpotSnapshot(t *testing.T, f *targetFixture, at time.Time) {
	t.Helper()
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpdateTradingAccountSync("space-1", "account-a", store.TradingAccountSyncState{
			Ready: true, SnapshotSourceTime: at.UnixMilli(), LastSyncAt: at.UnixMilli(),
			Snapshot:         store.TradingAccountSnapshot{WalletUpdatedAt: map[string]int64{"SPOT": at.UnixMilli()}, AvailableFunds: "100000"},
			LeverageSettings: store.LeverageSettings{"*": "5"},
		})
	}))
}

func TestWalletTransferFlowWithRealServiceInExecutor(t *testing.T) {
	f, fake, newExecutor := newWalletIntegration(t)
	ctx := context.Background()
	executor := newExecutor()

	// 现货钱包只有 10U，买单需要 510：先划转，这一轮不买。
	result, err := executor.Converge(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "transfer", result.Action)
	require.Empty(t, f.orders.specs, "划转没完成不买入")
	require.Len(t, fake.Calls(), 1)
	require.Equal(t, exchange.WalletFutures, fake.Calls()[0].From)
	require.Equal(t, "500", fake.Calls()[0].Amount.String())

	// 交易所还没确认：继续等，不重复划转。
	result, err = executor.Converge(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "transfer", result.Action)
	require.Len(t, fake.Calls(), 1)

	// 划转到账，但现货钱包的存储快照还没追上：继续等。
	fake.ConfirmAll()
	completedAt := time.Now()
	markSpotSnapshot(t, f, completedAt.Add(-time.Hour))
	result, err = executor.Converge(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "transfer", result.Action)
	require.Empty(t, f.orders.specs)

	// 划转刚完成就重启：新的服务和执行器，状态只在划转表里，仍然要等快照，不会重复划转。
	restarted := newExecutor()
	result, err = restarted.Converge(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "transfer", result.Action)
	require.Len(t, fake.Calls(), 1)

	// 快照追上后买入。
	markSpotSnapshot(t, f, time.Now().Add(time.Second))
	result, err = restarted.Converge(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "place", result.Action)
	require.Equal(t, []string{"BTC-USDT-SPOT:BUY"}, placedInstruments(f))
	require.Len(t, fake.Calls(), 1, "整条链路只划了一次")
}

// 合约钱包可划转的资金不够：买单按比例缩小，只买得起的部分；剩下的原因写进 blocked_targets。
func TestWalletShortfallShrinksBuyAndRecordsReasonWithRealService(t *testing.T) {
	f, fake, newExecutor := newWalletIntegration(t)
	fake.Set(func(e *wallettest.Exchange) { e.Futures = "100"; e.Spot = "10" })
	ctx := context.Background()
	executor := newExecutor()

	result, err := executor.Converge(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "transfer", result.Action)
	require.Equal(t, "99", fake.Calls()[0].Amount.String(), "最多划出可划转额度的 99%")
	fake.ConfirmAll()
	markSpotSnapshot(t, f, time.Now().Add(time.Second))
	result, err = executor.Converge(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "place", result.Action)
	require.Len(t, f.orders.specs, 1)
	// 买得起 109 / 510 约 21%：数量按比例缩小到 0.01 的约五分之一。
	require.True(t, f.orders.specs[0].Quantity.Cmp(mustDec("0.01")) < 0)
	require.Len(t, fake.Calls(), 1, "不会再划第二笔")
}

// 额度不足、缩小后的买单太小下不了：整轮收敛不报错（缩小比例是有理数，数量写不进 blocked_targets 曾让 Trade 整体未就绪），
// 目标定终态 BLOCKED，blocked_targets 记原始差额和原因，不能当“已满足”悄悄定成 CONVERGED。
func TestWalletScaledBuyTooSmallIsBlockedWithOriginalQuantity(t *testing.T) {
	f, fake, newExecutor := newWalletIntegration(t)
	fake.Set(func(e *wallettest.Exchange) { e.Spot = "5"; e.Futures = "0" })
	ctx := context.Background()
	executor := newExecutor()

	result, err := executor.Converge(ctx, "space-1", "logical-1")
	require.NoError(t, err, "缩小后的数量必须能写进 blocked_targets，不能让整轮收敛失败")
	require.Equal(t, StatusBlocked, result.Status)
	require.Empty(t, f.orders.specs)
	require.Empty(t, fake.Calls(), "合约钱包没有可划转的资金，不发划转")
	stored, err := f.store.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusBlocked, stored.Status)
	require.Len(t, stored.BlockedTargets, 1)
	require.Equal(t, "BTC-USDT-SPOT", stored.BlockedTargets[0].InstrumentID)
	require.Equal(t, "0.01", stored.BlockedTargets[0].Quantity, "记缩小前的原始差额")
	require.Contains(t, stored.BlockedTargets[0].Reason, "资金不足")
}

func TestScaleBuysRoundsDownAndKeepsOriginalDelta(t *testing.T) {
	member := memberState{account: store.TradingAccountRecord{TradingAccountID: "account-a"}}
	buys := []laneAction{{member: member, delta: mustDec("0.01")}}
	ratio := mustDec("1").Div(mustDec("510"))

	scaled := scaleBuys(buys, map[string]shared.Decimal{"account-a": ratio}, nil)
	require.Len(t, scaled, 1)
	require.True(t, scaled[0].scaled)
	require.Equal(t, "0.01", scaled[0].blockedQuantity())
	require.Equal(t, scaled[0].delta.RoundDown(8).String(), scaled[0].delta.String(), "缩小后的数量最多 8 位小数")
	_, err := shared.ParseDecimal(scaled[0].delta.String())
	require.NoError(t, err)

	// 向下取整后为 0 的按“这轮买不了”处理。
	require.Empty(t, scaleBuys(buys, map[string]shared.Decimal{"account-a": mustDec("0.000000001")}, nil))
}

// 账户的划转被交易所拒绝：这个账户的买入记 blocked 并写明原因，整轮收敛不报错，目标定终态 BLOCKED。
func TestWalletRejectionIsRecordedAsBlockedReasonNotRoundError(t *testing.T) {
	f, fake, newExecutor := newWalletIntegration(t)
	fake.TransferErr = &exchange.Error{Kind: exchange.ErrorRejected, Code: "-5013", Err: errors.New("no")}
	ctx := context.Background()
	executor := newExecutor()

	result, err := executor.Converge(ctx, "space-1", "logical-1")
	require.NoError(t, err, "账户级的划转问题不能让整轮收敛失败")
	require.Equal(t, StatusBlocked, result.Status)
	require.Empty(t, f.orders.specs)
	stored, err := f.store.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Len(t, stored.BlockedTargets, 1)
	require.Contains(t, stored.BlockedTargets[0].Reason, "划转没有成功")
	require.Contains(t, stored.BlockedTargets[0].Reason, "-5013")
}

// 限频等软失败：目标保持收敛中并按退避重试，不是 BLOCKED；原因是中文类别，退避期内（目标每秒收敛一次）不再请求交易所。
func TestWalletSoftFailureKeepsConvergingAndBacksOff(t *testing.T) {
	f, fake, newExecutor := newWalletIntegration(t)
	fake.TransferErr = &exchange.Error{Kind: exchange.ErrorRateLimited, HTTPStatus: 429, Err: errors.New("429")}
	ctx := context.Background()
	executor := newExecutor()

	result, err := executor.Converge(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusConverging, result.Status, "限频不让目标直接 BLOCKED")
	require.Equal(t, "transfer", result.Action)
	require.Greater(t, result.RetryAfter, time.Duration(0))
	require.Empty(t, f.orders.specs)
	stored, err := f.store.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Len(t, stored.BlockedTargets, 1)
	require.True(t, stored.BlockedTargets[0].Deferred, "等重试不是阻塞，界面用中性色")
	require.Contains(t, stored.BlockedTargets[0].Reason, "被限频或本地限流暂停")
	require.NotContains(t, stored.BlockedTargets[0].Reason, "RATE_LIMITED")

	calls, queries := len(fake.Calls()), fake.Queries()
	for range 3 {
		result, err = executor.Converge(ctx, "space-1", "logical-1")
		require.NoError(t, err)
		require.Equal(t, "transfer", result.Action)
	}
	require.Len(t, fake.Calls(), calls, "退避期内不再发划转")
	require.Equal(t, queries, fake.Queries(), "退避期内不查余额")
}

// 没被额度封顶的划转完成后价格上涨 1%：再划一笔差额，买单不缩小（不会缩成 100/101），买够后目标收敛。
func TestWalletUncappedTransferTopsUpWhenPriceRises(t *testing.T) {
	f, fake, newExecutor := newWalletIntegration(t)
	ctx := context.Background()
	executor := newExecutor()

	result, err := executor.Converge(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "transfer", result.Action)
	fake.ConfirmAll()
	markSpotSnapshot(t, f, time.Now().Add(time.Second))
	fake.Set(func(e *wallettest.Exchange) { e.Ask = "50500" })

	result, err = executor.Converge(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "transfer", result.Action, "缺的 5.1 从合约钱包补划")
	require.Len(t, fake.Calls(), 2)
	require.Equal(t, "5.1", fake.Calls()[1].Amount.String())
	fake.ConfirmAll()
	markSpotSnapshot(t, f, time.Now().Add(2*time.Second))
	result, err = executor.Converge(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "place", result.Action)
	require.Equal(t, "0.01", f.orders.specs[0].Quantity.String(), "买单没有被缩小")
}

// 每小时上限已经用完：买单顺延到下一个整点，不为它划转（划进去这个小时也买不了，钱会一直停在现货钱包）；
// 现货钱包里已有的资金到整点还要用，也不归集回合约钱包。
func TestWalletHourlyCapUsedUpNeitherTransfersNorCollects(t *testing.T) {
	f, fake, newExecutor := newWalletIntegration(t)
	// 现货 510、合约 600：没有顺延保护时现货钱包多出的 509 够归集。
	fake.Set(func(e *wallettest.Exchange) { e.Spot = "510"; e.Futures = "600" })
	f.setOrderSettings(t, `{"order_mode":"MARKET","min_notional_spot":"0","min_notional_swap":"0","hourly_cap_spot":{"BTC-USDT-SPOT":"1000"}}`)
	hourStart := time.Now().UTC().Truncate(time.Hour).Add(time.Second).UnixMilli()
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "used", TradingAccountID: "account-a", LogicalAccountID: "logical-1", RunnerID: "runner-1",
		ClientOrderID: "used", Exchange: "BINANCE", MarketType: "SPOT", InstrumentID: "BTC-USDT-SPOT", ExchangeSymbol: "BTCUSDT",
		OrderType: "MARKET", Side: "BUY", Quantity: "10", ReferencePrice: "100", AveragePrice: "100", FilledQuantity: "10",
		OwnerType: "TARGET", OwnerID: "target-current", State: "FILLED", SubmittedAt: hourStart, FinishedAt: hourStart, Version: 1,
	})
	ctx := context.Background()
	executor := newExecutor()

	for range 3 {
		result, err := executor.Converge(ctx, "space-1", "logical-1")
		require.NoError(t, err)
		require.Equal(t, StatusConverged, result.Status, "买单顺延到下一个整点")
		require.Greater(t, result.RetryAfter, time.Duration(0), "到整点自己唤醒")
	}
	require.Empty(t, f.orders.specs)
	require.Empty(t, fake.Calls(), "既不划转也不归集")
	stored, err := f.store.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Len(t, stored.BlockedTargets, 1)
	require.True(t, stored.BlockedTargets[0].Deferred)
}

// 划转完成后某一轮买单被每小时上限顺延（上限在划转之后才用完）：这一轮不能把刚划进来的钱归集回合约钱包。
func TestWalletCollectDoesNotTakeBackFundsForDeferredBuy(t *testing.T) {
	f, fake, newExecutor := newWalletIntegration(t)
	fake.Set(func(e *wallettest.Exchange) { e.Futures = "600" })
	f.setOrderSettings(t, `{"order_mode":"MARKET","min_notional_spot":"0","min_notional_swap":"0","hourly_cap_spot":{"BTC-USDT-SPOT":"1000"}}`)
	ctx := context.Background()
	executor := newExecutor()

	result, err := executor.Converge(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "transfer", result.Action, "上限还没用完：先划转")
	fake.ConfirmAll()
	markSpotSnapshot(t, f, time.Now().Add(time.Second))

	// 划转到账后、买入之前，这个小时的额度被别处用完。
	hourStart := time.Now().UTC().Truncate(time.Hour).Add(time.Second).UnixMilli()
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "used", TradingAccountID: "account-a", LogicalAccountID: "logical-1", RunnerID: "runner-1",
		ClientOrderID: "used", Exchange: "BINANCE", MarketType: "SPOT", InstrumentID: "BTC-USDT-SPOT", ExchangeSymbol: "BTCUSDT",
		OrderType: "MARKET", Side: "BUY", Quantity: "10", ReferencePrice: "100", AveragePrice: "100", FilledQuantity: "10",
		OwnerType: "TARGET", OwnerID: "target-current", State: "FILLED", SubmittedAt: hourStart, FinishedAt: hourStart, Version: 1,
	})
	result, err = executor.Converge(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusConverged, result.Status, "买单顺延到下一个整点")
	require.Empty(t, f.orders.specs)
	for _, call := range fake.Calls() {
		require.Equal(t, exchange.WalletSpot, call.To, "不能把现货钱包里的钱归集回合约钱包")
	}
	require.Len(t, fake.Calls(), 1)
}

// 低于设置最小下单额的差额视为已满足：合约钱包资金充足、现货钱包不够时也不发起划转（划转了这笔单也不会下）。
func TestWalletNoTransferForBuyBelowConfiguredMinimum(t *testing.T) {
	f, fake, newExecutor := newWalletIntegration(t)
	f.setOrderSettings(t, `{"order_mode":"MARKET","min_notional_spot":"10","min_notional_swap":"0"}`)
	result, err := newExecutor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusConverged, result.Status)
	require.Empty(t, fake.Calls(), "差额 1U 低于最小下单额 10U：不为它划转")
	require.Empty(t, f.orders.specs)
}

func mustDec(value string) shared.Decimal { return shared.MustDecimal(value) }
