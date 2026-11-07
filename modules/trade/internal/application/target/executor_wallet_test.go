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

func mustDec(value string) shared.Decimal { return shared.MustDecimal(value) }
