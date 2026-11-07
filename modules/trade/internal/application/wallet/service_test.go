package wallet

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/wallet/wallettest"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"github.com/stretchr/testify/require"
)

type env struct {
	t        *testing.T
	store    *store.Store
	exchange *wallettest.Exchange
	service  *Service
	now      time.Time
	syncs    []string
	target   store.LogicalAccountTargetRecord
	account  store.TradingAccountRecord
}

func newEnv(t *testing.T, spot, futures string) *env { return newEnvWithFund(t, spot, futures, "") }

// newEnvWithFund 创建测试环境，fund 是组合账户的资金设置 JSON。
func newEnvWithFund(t *testing.T, spot, futures, fund string) *env {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tradeStore.Close()) })
	account := store.TradingAccountRecord{
		SpaceID: "space-1", TradingAccountID: "account-1", Name: "main", Exchange: "BINANCE", Markets: []string{"SPOT", "SWAP"},
		ExecutionMode: "LIVE", AccountMode: "BINANCE_STANDARD", Environment: "PRODUCTION", CredentialSecretID: "secret-1",
		SettlementAsset: "USDT", MarginMode: "CROSS", Status: "ENABLED",
		LeverageSettings: store.LeverageSettings{store.DefaultLeverageKey: "1"},
	}
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(account); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical", OwnerInstanceID: "runner-1", OwnerSessionID: "session-1",
			ExecutionMode: "LIVE", SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "setup",
			FundSettingsJSON: fund,
		}); err != nil {
			return err
		}
		return tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-1", Enabled: true, Priority: 1,
		})
	}))
	e := &env{
		t: t, store: tradeStore, now: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC), account: account,
		target: store.LogicalAccountTargetRecord{SpaceID: "space-1", LogicalAccountID: "logical-1", TargetID: "target-1"},
	}
	e.exchange = &wallettest.Exchange{Spot: spot, Futures: futures, Ask: "50000", Clock: func() time.Time { return e.now }}
	e.service = e.newService()
	return e
}

// newService 创建一个服务实例；“重启”就是用同一个库再建一个。
func (e *env) newService() *Service {
	counter := 0
	return &Service{
		Store: e.store, Adapters: wallettest.Adapters{"account-1": e.exchange},
		RequestSync: func(id string) { e.syncs = append(e.syncs, id) },
		Now:         func() time.Time { return e.now },
		NewID:       func() string { counter++; return fmt.Sprintf("t-%d-%d", e.now.UnixNano(), counter) },
	}
}

func (e *env) buys(quantity string) []Buy {
	return []Buy{{
		Account:    e.account,
		Instrument: store.InstrumentRecord{InstrumentID: "BTC-USDT-SPOT", MarketType: "SPOT", ExchangeSymbol: "BTCUSDT"},
		Quantity:   shared.MustDecimal(quantity),
	}}
}

// markSynced 让现货钱包的存储快照更新时间（交易所时钟）停在 at。
func (e *env) markSynced(at time.Time) {
	e.t.Helper()
	require.NoError(e.t, e.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpdateTradingAccountSync("space-1", "account-1", store.TradingAccountSyncState{
			LastSyncAt: e.now.UnixMilli(), SnapshotSourceTime: at.UnixMilli(),
			Snapshot:         store.TradingAccountSnapshot{WalletUpdatedAt: map[string]int64{"SPOT": at.UnixMilli()}},
			LeverageSettings: store.LeverageSettings{store.DefaultLeverageKey: "1"},
		})
	}))
}

func (e *env) transfers(direction string) []store.WalletTransferRecord {
	e.t.Helper()
	records, err := e.store.ListWalletTransfersForTarget(context.Background(), "space-1", "logical-1", "target-1", "account-1", direction)
	require.NoError(e.t, err)
	return records
}

func (e *env) before(quantity string) Outcome {
	e.t.Helper()
	outcome, err := e.service.BeforeSpotBuy(context.Background(), "space-1", e.target, e.buys(quantity))
	require.NoError(e.t, err, "账户级问题不返回错误，不让整轮收敛失败")
	return outcome
}

// 现货钱包不够：从合约钱包划转 max(0, 买单名义 × 1.02 − 现货可用)；划转完成、现货快照追上之后才放行买入。
func TestBeforeSpotBuyTransfersShortfallAndWaitsForSnapshot(t *testing.T) {
	e := newEnv(t, "10", "1000")
	outcome := e.before("0.01") // 0.01 × 50000 = 500，× 1.02 = 510
	require.True(t, waiting(outcome))
	calls := e.exchange.Calls()
	require.Len(t, calls, 1)
	require.Equal(t, exchange.WalletFutures, calls[0].From)
	require.Equal(t, exchange.WalletSpot, calls[0].To)
	require.Equal(t, "500", calls[0].Amount.String(), "510 − 现货可用 10")
	rows := e.transfers(store.TransferToSpot)
	require.Len(t, rows, 1)
	require.Equal(t, store.TransferStatePending, rows[0].State)
	require.Equal(t, "tran-1", rows[0].ExchangeTransferID)

	// 反复调用：交易所还没确认，不会重复划转。
	for range 3 {
		require.True(t, waiting(e.before("0.01")))
	}
	require.Len(t, e.exchange.Calls(), 1, "没有幂等键，靠本地 PENDING 记录避免重复划转")

	// 到账了，但现货钱包的快照还没追上划转时间：继续等，并请求同步。
	e.exchange.ConfirmAll()
	completedAt := e.now
	e.now = e.now.Add(2 * time.Second)
	e.markSynced(completedAt.Add(-time.Minute))
	require.True(t, waiting(e.before("0.01")))
	require.Equal(t, []string{"account-1"}, e.syncs)
	require.Equal(t, store.TransferStateCompleted, e.transfers(store.TransferToSpot)[0].State)
	require.EqualValues(t, completedAt.UnixMilli(), e.transfers(store.TransferToSpot)[0].CompletedAt, "完成时间取交易所划转记录的时间")

	// 现货钱包的快照时间追上划转时间后放行，按现在的余额不缩小。
	e.markSynced(completedAt)
	outcome = e.before("0.01")
	require.False(t, waiting(outcome))
	require.Empty(t, outcome.BuyScale)
	require.Len(t, e.exchange.Calls(), 1)
}

// 划转刚完成就重启：状态在划转表里，新实例接着走，不重复划转，也不会漏等快照。
func TestRestartRightAfterTransferCompletedResumesWithoutDuplicate(t *testing.T) {
	e := newEnv(t, "10", "1000")
	e.before("0.01")
	e.exchange.ConfirmAll()
	completedAt := e.now
	e.now = e.now.Add(time.Second)
	e.markSynced(completedAt.Add(-time.Hour))
	require.True(t, waiting(e.before("0.01")), "划转已完成，等快照")

	restarted := e.newService()
	outcome, err := restarted.BeforeSpotBuy(context.Background(), "space-1", e.target, e.buys("0.01"))
	require.NoError(t, err)
	require.True(t, waiting(outcome), "重启后仍要等现货钱包的快照")
	e.now = e.now.Add(time.Second)
	e.markSynced(completedAt)
	outcome, err = restarted.BeforeSpotBuy(context.Background(), "space-1", e.target, e.buys("0.01"))
	require.NoError(t, err)
	require.False(t, waiting(outcome))
	require.Len(t, e.exchange.Calls(), 1)
}

// 快照迟迟没追上：超过宽限期放行（余额是现查的，存储快照稍旧最多让交易所拒单），不会永远卡住。
func TestSnapshotWaitIsBoundedByGracePeriod(t *testing.T) {
	e := newEnv(t, "10", "1000")
	e.before("0.01")
	e.exchange.ConfirmAll()
	e.markSynced(e.now.Add(-time.Hour))
	e.now = e.now.Add(time.Second)
	require.True(t, waiting(e.before("0.01")))
	e.now = e.now.Add(snapshotGrace)
	require.False(t, waiting(e.before("0.01")))
}

// 请求超时但交易所其实已经执行：下一轮靠交易所划转记录（时间窗口、金额、方向）认领，不会重复划转。
func TestTimeoutAfterDeliveryIsAdoptedFromExchangeRecordsWithoutRetry(t *testing.T) {
	e := newEnv(t, "10", "1000")
	e.exchange.TransferErr = &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: errors.New("timeout")}
	e.exchange.TransferDelivers = true

	require.True(t, waiting(e.before("0.01")), "结果不明按等待处理，不是错误")
	rows := e.transfers(store.TransferToSpot)
	require.Len(t, rows, 1)
	require.Equal(t, store.TransferStatePending, rows[0].State, "结果不明：记录保持 PENDING")
	require.Empty(t, rows[0].ExchangeTransferID)

	e.exchange.TransferErr = nil
	e.now = e.now.Add(2 * time.Second)
	e.exchange.ConfirmAll()
	completedAt := e.now.Add(-2 * time.Second)
	e.markSynced(completedAt)
	require.False(t, waiting(e.before("0.01")))
	require.Len(t, e.exchange.Calls(), 1, "划转已经执行，不能再划一次")
	rows = e.transfers(store.TransferToSpot)
	require.Equal(t, store.TransferStateCompleted, rows[0].State)
	require.Equal(t, "tran-1", rows[0].ExchangeTransferID)
}

// 请求超时且交易所确实没执行：过了对账宽限期，交易所记录里仍没有，才作废这条记录并重试；这种作废不计入连续失败。
func TestTimeoutWithoutDeliveryRetriesOnlyAfterGracePeriod(t *testing.T) {
	e := newEnv(t, "10", "1000")
	e.exchange.TransferErr = &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: errors.New("timeout")}
	require.True(t, waiting(e.before("0.01")))
	e.exchange.TransferErr = nil

	e.now = e.now.Add(10 * time.Second)
	require.True(t, waiting(e.before("0.01")), "宽限期内记录里没有也不能下结论")
	require.Len(t, e.exchange.Calls(), 1)

	e.now = e.now.Add(2 * confirmGrace)
	require.True(t, waiting(e.before("0.01")), "重试已发出，等到账")
	require.Len(t, e.exchange.Calls(), 2)
	rows := e.transfers(store.TransferToSpot)
	require.Len(t, rows, 2)
	require.Equal(t, store.TransferStateFailed, rows[0].State)
	require.True(t, strings.HasPrefix(rows[0].LastError, softNotArrive))
	require.NotEqual(t, rows[0].ClientRef, rows[1].ClientRef)
	failures, _ := trailingHardFailures(rows)
	require.Zero(t, failures)
}

// 目标 1 的划转超时（PENDING 没有流水号，交易所正在处理、余额还没变）时目标 2 到达：
// 发起新划转前先对账这个账户所有未结的记录，有未结的就等，不会按没变化的余额再划一笔。
func TestNewTargetWaitsForPendingTransferOfPreviousTarget(t *testing.T) {
	e := newEnv(t, "10", "1000")
	e.exchange.TransferErr = &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: errors.New("timeout")}
	e.exchange.TransferDelivers = true
	require.True(t, waiting(e.before("0.01")))
	e.exchange.TransferErr = nil
	require.Len(t, e.exchange.Calls(), 1)

	// 目标 2 到达：余额还没变（交易所正在处理），旧记录没有流水号。
	e.target = store.LogicalAccountTargetRecord{SpaceID: "space-1", LogicalAccountID: "logical-1", TargetID: "target-2"}
	e.now = e.now.Add(3 * time.Second)
	outcome := e.before("0.01")
	require.True(t, waiting(outcome), "有别的目标留下的未结划转，不发起新划转")
	require.Len(t, e.exchange.Calls(), 1, "没有重复划转")

	// 交易所把旧划转确认了：旧记录被对账认领、记入完成，目标 2 看到现货钱包有了资金，不再划转。
	e.exchange.ConfirmAll()
	e.markSynced(e.now)
	outcome = e.before("0.01")
	require.False(t, waiting(outcome))
	require.Len(t, e.exchange.Calls(), 1)
	old, err := e.store.ListWalletTransfersForTarget(context.Background(), "space-1", "logical-1", "target-1", "account-1", store.TransferToSpot)
	require.NoError(t, err)
	require.Equal(t, store.TransferStateCompleted, old[0].State, "旧目标的记录也被对账完成，会出现在资金流水里")
	require.Equal(t, "tran-1", old[0].ExchangeTransferID)
}

// 被拒绝是硬失败：这一轮这个账户不买（比例 0，写明原因），不返回错误；连续 3 次后这个目标不再重试，仍然只是比例 0。
func TestRejectedTransferScalesToZeroWithReasonAndStopsAfterRepeatedFailures(t *testing.T) {
	e := newEnv(t, "10", "1000")
	e.exchange.TransferErr = &exchange.Error{Kind: exchange.ErrorRejected, Code: "-5013", Err: errors.New("asset not available")}
	for attempt := 1; attempt <= maxTransferFailures; attempt++ {
		e.now = e.now.Add(time.Second)
		outcome := e.before("0.01")
		require.False(t, waiting(outcome))
		scale, ok := outcome.BuyScale["account-1"]
		require.True(t, ok)
		require.True(t, scale.IsZero())
		require.Contains(t, outcome.Reasons["account-1"], "错误码 -5013")
		require.NotContains(t, outcome.Reasons["account-1"], "asset not available", "不带交易所响应体原文")
		require.Len(t, e.exchange.Calls(), attempt)
	}
	e.now = e.now.Add(time.Second)
	outcome := e.before("0.01")
	require.Contains(t, outcome.Reasons["account-1"], "不再重试")
	require.True(t, outcome.BuyScale["account-1"].IsZero())
	require.Len(t, e.exchange.Calls(), maxTransferFailures, "到上限后不再发起划转")
}

// 现货钱包已经够用时直接放行，不受连续失败次数影响；失败次数只在确实需要划转时才拦。
func TestEnoughSpotFundsIsNotBlockedByPastTransferFailures(t *testing.T) {
	e := newEnv(t, "10", "1000")
	e.exchange.TransferErr = &exchange.Error{Kind: exchange.ErrorRejected, Err: errors.New("x")}
	for range maxTransferFailures {
		e.now = e.now.Add(time.Second)
		e.before("0.01")
	}
	e.exchange.TransferErr = nil
	e.exchange.Set(func(f *wallettest.Exchange) { f.Spot = "1000" })
	outcome := e.before("0.01")
	require.False(t, waiting(outcome))
	require.Empty(t, outcome.BuyScale, "现货钱包够用就直接买")
	require.Empty(t, outcome.Reasons)
}

// 限频没发出请求：不计入连续失败，不会因为限频把目标永久拦住；这个账户按退避等待后自动重试（目标保持收敛中，不是阻塞），
// 原因是中文类别，不含英文枚举。
func TestRateLimitedTransfersDoNotCountAsFailures(t *testing.T) {
	e := newEnv(t, "10", "1000")
	e.exchange.TransferErr = &exchange.Error{Kind: exchange.ErrorRateLimited, HTTPStatus: 429, Err: errors.New("429")}
	for attempt := 1; attempt <= maxTransferFailures+2; attempt++ {
		e.now = e.now.Add(time.Minute)
		outcome := e.before("0.01")
		require.True(t, waiting(outcome), "限频：账户等待重试，不缩成 0、不阻塞")
		require.Empty(t, outcome.BuyScale)
		reason := outcome.Waiting["account-1"]
		require.Contains(t, reason, "暂时没发出")
		require.Contains(t, reason, "被限频或本地限流暂停")
		require.Contains(t, reason, "HTTP 429")
		require.NotContains(t, reason, "RATE_LIMITED")
		require.NotContains(t, reason, "NOT_SENT")
		require.Positive(t, outcome.RetryAfter)
		require.Len(t, e.exchange.Calls(), attempt, "退避期过后每轮都继续尝试")
	}
	rows := e.transfers(store.TransferToSpot)
	failures, _ := trailingHardFailures(rows)
	require.Zero(t, failures)
}

// 软失败的退避在钱包服务里生效：目标每秒收敛一次，退避期内直接返回等待，不请求交易所，也不再新写划转记录。
func TestSoftFailureBackoffDoesNotCallExchangeBeforeRetryTime(t *testing.T) {
	e := newEnv(t, "10", "1000")
	e.exchange.TransferErr = &exchange.Error{Kind: exchange.ErrorNotSent, Err: errors.New("paused")}
	first := e.before("0.01")
	require.True(t, waiting(first))
	require.Len(t, e.exchange.Calls(), 1)
	queries := e.exchange.Queries()
	rows := len(e.transfers(store.TransferToSpot))
	for range 5 {
		e.now = e.now.Add(100 * time.Millisecond)
		outcome := e.before("0.01")
		require.True(t, waiting(outcome))
		require.Contains(t, outcome.Waiting["account-1"], "暂时没发出", "等待期间仍说明原因")
	}
	require.Len(t, e.exchange.Calls(), 1)
	require.Equal(t, queries, e.exchange.Queries(), "退避期内不查余额、不查划转记录")
	require.Len(t, e.transfers(store.TransferToSpot), rows)

	e.now = e.now.Add(first.RetryAfter)
	e.exchange.TransferErr = nil
	require.True(t, waiting(e.before("0.01")))
	require.Len(t, e.exchange.Calls(), 2, "退避期过后才重试，重试成功就发出划转")
}

// 额度不足：合约钱包可划转额度的 99% 只用一次；同一个目标之后的轮次只按比例缩小，不再划转，不会把留的 1% 也用掉；告警每个目标一次。
func TestInsufficientCapacityTransfersOnceThenOnlyScales(t *testing.T) {
	e := newEnv(t, "10", "100")
	before := len(telemetry.Recent(telemetry.AttentionTransferShortfall, e.now, time.Minute))
	outcome := e.before("0.01") // 需要 510
	require.True(t, waiting(outcome))
	calls := e.exchange.Calls()
	require.Len(t, calls, 1)
	require.Equal(t, "99", calls[0].Amount.String(), "不超过合约钱包可划转额度 × 0.99")
	after := len(telemetry.Recent(telemetry.AttentionTransferShortfall, e.now, time.Minute))
	require.Greater(t, after, before, "额度不足要告警")

	e.exchange.ConfirmAll()
	completedAt := e.now
	e.now = e.now.Add(time.Second)
	e.markSynced(completedAt)
	outcome = e.before("0.01")
	// 到账后现货 109、合约 1：不再划第二笔，只缩放 109 / 510。
	require.False(t, waiting(outcome))
	scale, ok := outcome.BuyScale["account-1"]
	require.True(t, ok)
	require.Equal(t, shared.MustDecimal("109").Div(shared.MustDecimal("510")).RoundDown(8).String(), scale.String())
	// 即使合约钱包又有了可划转的资金，这个目标也不再划转；比例按本轮算，不累计。
	e.exchange.Set(func(f *wallettest.Exchange) { f.Futures = "5000" })
	for range 3 {
		outcome = e.before("0.005")
		require.Equal(t, shared.MustDecimal("109").Div(shared.MustDecimal("255")).RoundDown(8).String(), outcome.BuyScale["account-1"].String())
	}
	require.Len(t, e.exchange.Calls(), 1, "同一个目标不会再划转")
	require.Len(t, telemetry.Recent(telemetry.AttentionTransferShortfall, e.now, time.Minute), after, "告警每个目标一次，不是每轮一次")
}

// 合约钱包可划转额度取 maxWithdrawAmount（Transferable），不是可用余额：两者不一致时不会划出超过允许转出的数量。
func TestTransferCapacityUsesMaxWithdrawAmountNotAvailableBalance(t *testing.T) {
	e := newEnv(t, "10", "1000")
	e.exchange.FuturesTransferable = "200" // 有持仓浮盈：可用 1000，但最多只能转出 200
	outcome := e.before("0.01")            // 需要 500
	require.True(t, waiting(outcome))
	calls := e.exchange.Calls()
	require.Len(t, calls, 1)
	require.Equal(t, "198", calls[0].Amount.String(), "200 × 0.99，不是 1000 × 0.99")
}

func TestNoFuturesFundsScalesBuysToAvailableSpotWithoutTransfer(t *testing.T) {
	e := newEnv(t, "0", "0")
	outcome := e.before("0.01")
	require.False(t, waiting(outcome))
	require.Empty(t, e.exchange.Calls())
	scale, ok := outcome.BuyScale["account-1"]
	require.True(t, ok)
	require.True(t, scale.IsZero(), "没有任何资金：这轮买不了")
	require.NotEmpty(t, outcome.Reasons["account-1"])
}

func TestEnoughSpotFundsDoesNotTransfer(t *testing.T) {
	e := newEnv(t, "600", "1000")
	outcome := e.before("0.01")
	require.False(t, waiting(outcome))
	require.Empty(t, outcome.BuyScale)
	require.Empty(t, e.exchange.Calls())
}

// 查询持续失败：返回等待，退避时间指数增长有上限，退避期内不请求交易所；连续失败到阈值告警；恢复后清零。
func TestQueryFailuresBackOffAlertAndRecover(t *testing.T) {
	e := newEnv(t, "10", "1000")
	e.exchange.QueryErr = errors.New("network down")
	var last time.Duration
	for attempt := 1; attempt <= queryFailureAlert+3; attempt++ {
		outcome := e.before("0.01")
		require.True(t, waiting(outcome))
		require.Contains(t, outcome.Waiting["account-1"], "稍后自动重试")
		require.GreaterOrEqual(t, outcome.RetryAfter, last, "退避只增不减")
		require.LessOrEqual(t, outcome.RetryAfter, maxBackoff)
		last = outcome.RetryAfter
		// 退避期内再来几轮：直接等待，不调交易所。
		queries := e.exchange.Queries()
		for range 3 {
			require.True(t, waiting(e.before("0.01")))
		}
		require.Equal(t, queries, e.exchange.Queries(), "退避期内不请求交易所")
		e.now = e.now.Add(outcome.RetryAfter)
	}
	require.Equal(t, maxBackoff, minDuration(last, maxBackoff))
	require.NotEmpty(t, telemetry.Recent(telemetry.AttentionTransferFailing, e.now, time.Minute), "连续失败要告警")
	e.exchange.QueryErr = nil
	outcome := e.before("0.01")
	require.True(t, waiting(outcome), "恢复后正常发起划转")
	require.Zero(t, outcome.RetryAfter)
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// 交易所划转记录查不出来：记录保持 PENDING、不重试划转，同样退避和告警。
func TestListTransfersFailureKeepsPendingAndBacksOff(t *testing.T) {
	e := newEnv(t, "10", "1000")
	require.True(t, waiting(e.before("0.01")))
	e.exchange.ListErr = errors.New("rate limited")
	e.now = e.now.Add(time.Second)
	outcome := e.before("0.01")
	require.True(t, waiting(outcome))
	require.Positive(t, outcome.RetryAfter)
	require.Len(t, e.exchange.Calls(), 1)
	require.Equal(t, store.TransferStatePending, e.transfers(store.TransferToSpot)[0].State)
}

// 没被额度封顶的划转完成后，价格上涨使现货钱包又差几十 U：再划一笔，不缩小买单，也不发“合约钱包可划转资金不足”告警
// （合约钱包还有大量资金）。
func TestUncappedTransferCompletedTransfersAgainWhenPriceRises(t *testing.T) {
	e := newEnv(t, "10", "90000")
	before := len(telemetry.Recent(telemetry.AttentionTransferShortfall, e.now, time.Minute))
	require.True(t, waiting(e.before("0.01")))
	require.Len(t, e.exchange.Calls(), 1)
	require.False(t, e.transfers(store.TransferToSpot)[0].Capped, "金额没有被额度封顶")
	e.exchange.ConfirmAll()
	completedAt := e.now
	e.now = e.now.Add(time.Second)
	e.markSynced(completedAt)

	// 价格上涨 1%：需要 515.1，现货钱包只有 510。
	e.exchange.Set(func(f *wallettest.Exchange) { f.Ask = "50500" })
	outcome := e.before("0.01")
	require.True(t, waiting(outcome), "缺的钱从合约钱包再划一笔")
	require.Len(t, e.exchange.Calls(), 2)
	require.Equal(t, "5.1", e.exchange.Calls()[1].Amount.String())
	require.Empty(t, outcome.BuyScale)
	require.Equal(t, before, len(telemetry.Recent(telemetry.AttentionTransferShortfall, e.now, time.Minute)), "合约钱包还有钱，不告警")
}

// 被额度封顶的划转在记录上有标记；它完成后这个目标不再追加，只按现有资金缩小；
// 没封顶的划转次数也有上限，余额口径异常时不会一直追加。
func TestCappedTransferIsMarkedAndStopsFurtherTransfers(t *testing.T) {
	e := newEnv(t, "10", "100")
	require.True(t, waiting(e.before("0.01")))
	rows := e.transfers(store.TransferToSpot)
	require.Len(t, rows, 1)
	require.True(t, rows[0].Capped, "金额小于需要的金额：被额度封顶")
	e.exchange.ConfirmAll()
	completedAt := e.now
	e.now = e.now.Add(time.Second)
	e.markSynced(completedAt)
	e.exchange.Set(func(f *wallettest.Exchange) { f.Futures = "5000" })
	outcome := e.before("0.01")
	require.False(t, waiting(outcome))
	require.Len(t, e.exchange.Calls(), 1, "封顶的划转完成后不再追加")
	require.NotEmpty(t, outcome.BuyScale["account-1"])
	require.Contains(t, outcome.Reasons["account-1"], "已经划完")
}

func TestTransfersPerTargetAreLimited(t *testing.T) {
	e := newEnv(t, "10", "90000")
	for round := 1; round <= maxTransfersPerTarget; round++ {
		e.exchange.Set(func(f *wallettest.Exchange) { f.Ask = fmt.Sprintf("%d", 50000*(round+1)) })
		require.True(t, waiting(e.before("0.01")), "第 %d 笔划转", round)
		require.Len(t, e.exchange.Calls(), round)
		e.exchange.ConfirmAll()
		completedAt := e.now
		e.now = e.now.Add(time.Second)
		e.markSynced(completedAt)
	}
	e.exchange.Set(func(f *wallettest.Exchange) { f.Ask = "500000" })
	outcome := e.before("0.01")
	require.False(t, waiting(outcome))
	require.Len(t, e.exchange.Calls(), maxTransfersPerTarget, "到上限不再划")
	require.Contains(t, outcome.Reasons["account-1"], "已经划转")
}

// 记录已经不是 PENDING（人工结束或另一轮对账抢先处理）时推进它不是错误，不能当成查询失败去退避、告警。
func TestFinishOnAlreadySettledRecordIsNotAnError(t *testing.T) {
	e := newEnv(t, "10", "1000")
	require.True(t, waiting(e.before("0.01")))
	row := e.transfers(store.TransferToSpot)[0]
	_, err := e.service.finish(context.Background(), "space-1", row, "tran-9", store.TransferStateCompleted, "", e.now.UnixMilli())
	require.NoError(t, err)
	stale, err := e.service.finish(context.Background(), "space-1", row, "", store.TransferStateFailed, "x", 0)
	require.NoError(t, err, "已经完成的记录再推进：原样返回，不报错")
	require.Equal(t, store.TransferStatePending, stale.State)
	require.Equal(t, store.TransferStateCompleted, e.transfers(store.TransferToSpot)[0].State)
}

func TestUnsupportedAdapterScalesToZeroWithReasonInsteadOfFailingTheRound(t *testing.T) {
	e := newEnv(t, "10", "1000")
	e.service.Adapters = wallettest.Adapters{"account-1": noTransferAdapter{}}
	outcome := e.before("0.01")
	require.False(t, waiting(outcome))
	require.True(t, outcome.BuyScale["account-1"].IsZero())
	require.Contains(t, outcome.Reasons["account-1"], "不可用")
}

type noTransferAdapter struct{ execution.ExecutionAdapter }

// 同一个组合有两个账户，其中一个出问题（被拒绝）：另一个账户照常。
func TestOneAccountFailingDoesNotAffectAnother(t *testing.T) {
	e := newEnv(t, "10", "1000")
	second := e.account
	second.TradingAccountID, second.Name, second.CredentialSecretID = "account-2", "second", "secret-2"
	require.NoError(t, e.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateTradingAccount(second)
	}))
	healthy := &wallettest.Exchange{Spot: "10", Futures: "1000", Ask: "50000", Clock: func() time.Time { return e.now }}
	broken := &wallettest.Exchange{Spot: "10", Futures: "1000", Ask: "50000", Clock: func() time.Time { return e.now },
		TransferErr: &exchange.Error{Kind: exchange.ErrorRejected, Err: errors.New("no")}}
	e.service.Adapters = wallettest.Adapters{"account-1": healthy, "account-2": broken}
	buys := append(e.buys("0.01"), Buy{Account: second, Instrument: e.buys("0.01")[0].Instrument, Quantity: shared.MustDecimal("0.01")})
	outcome, err := e.service.BeforeSpotBuy(context.Background(), "space-1", e.target, buys)
	require.NoError(t, err)
	require.True(t, waiting(outcome), "健康账户的划转已发出")
	require.Len(t, healthy.Calls(), 1)
	require.True(t, outcome.BuyScale["account-2"].IsZero())
	require.NotEmpty(t, outcome.Reasons["account-2"])
	_, hasScale := outcome.BuyScale["account-1"]
	require.False(t, hasScale)
	_, brokenWaits := outcome.Waiting["account-2"]
	require.False(t, brokenWaits, "只有还在划转的账户才等待，出问题的账户另有结论")
	_, healthyWaits := outcome.Waiting["account-1"]
	require.True(t, healthyWaits)
}

// 归集：现货钱包多出来的资金（留 1U）达到阈值才划回合约钱包；每个目标最多归集一次。
func TestCollectMovesExcessBackOncePerTarget(t *testing.T) {
	e := newEnv(t, "500", "50")
	ctx := context.Background()
	accounts := []store.TradingAccountRecord{e.account}

	pending, err := e.service.Collect(ctx, "space-1", e.target, accounts)
	require.NoError(t, err)
	require.True(t, pending)
	calls := e.exchange.Calls()
	require.Len(t, calls, 1)
	require.Equal(t, exchange.WalletSpot, calls[0].From)
	require.Equal(t, exchange.WalletFutures, calls[0].To)
	require.Equal(t, "499", calls[0].Amount.String(), "现货钱包留 1U")

	pending, err = e.service.Collect(ctx, "space-1", e.target, accounts)
	require.NoError(t, err)
	require.True(t, pending, "还没到账继续等")
	require.Len(t, e.exchange.Calls(), 1)

	e.exchange.ConfirmAll()
	e.now = e.now.Add(time.Second)
	for range 2 {
		pending, err = e.service.Collect(ctx, "space-1", e.target, accounts)
		require.NoError(t, err)
		require.False(t, pending)
	}
	require.Len(t, e.exchange.Calls(), 1, "同一个目标不会再归集")
	require.Equal(t, store.TransferStateCompleted, e.transfers(store.TransferToFutures)[0].State)
}

// 账户有别的目标留下的未结划转时：不发起新的归集，也不挡住本目标定终态。
func TestCollectSkipsWhileForeignTransferIsPendingWithoutBlockingTarget(t *testing.T) {
	e := newEnv(t, "500", "50")
	e.exchange.TransferErr = &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: errors.New("timeout")}
	e.exchange.TransferDelivers = true
	e.before("0.1") // 目标 1 发起一笔划转，结果不明
	e.exchange.TransferErr = nil
	e.target = store.LogicalAccountTargetRecord{SpaceID: "space-1", LogicalAccountID: "logical-1", TargetID: "target-2"}
	pending, err := e.service.Collect(context.Background(), "space-1", e.target, []store.TradingAccountRecord{e.account})
	require.NoError(t, err)
	require.False(t, pending, "别的目标的未结划转不挡本目标的终态")
	for _, call := range e.exchange.Calls() {
		require.NotEqual(t, exchange.WalletFutures, call.To, "不发起新的归集")
	}
}

func TestCollectSkipsExcessBelowThreshold(t *testing.T) {
	// 多出 49 < 100U 的下限，不划，避免 1 分钟级策略每期来回划转。
	e := newEnv(t, "50", "1000")
	pending, err := e.service.Collect(context.Background(), "space-1", e.target, []store.TradingAccountRecord{e.account})
	require.NoError(t, err)
	require.False(t, pending)
	require.Empty(t, e.exchange.Calls())
}

func TestCollectThresholdScalesWithEquity(t *testing.T) {
	e := newEnv(t, "500", "1000")
	// 最近一次权益采样 10 万：阈值 max(100, 1000) = 1000，多出 499 不归集。
	require.NoError(t, e.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpsertAccountEquityPoint(store.EquityPointRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", BucketTime: e.now.Truncate(time.Minute).UnixMilli(),
			Equity: "100000", AvailableFunds: "1000", UsedMargin: "0", SourceTime: e.now.UnixMilli(),
		})
	}))
	pending, err := e.service.Collect(context.Background(), "space-1", e.target, []store.TradingAccountRecord{e.account})
	require.NoError(t, err)
	require.False(t, pending)
	require.Empty(t, e.exchange.Calls())
}

// 开启 BNB 保留（金额 200）：现货钱包应持有 100U 的 BNB，当前 BNB 市值不够的部分折成 USDT 留下，而不是固定留一半。
func TestCollectKeepsOnlyWhatBNBTopUpNeeds(t *testing.T) {
	// 现货钱包没有 BNB：缺 100U，归集 500 − 1 − 100 = 399。
	e := newEnvWithFund(t, "500", "1000", `{"bnb_reserve_amount":"200"}`)
	e.exchange.BNBPrice = "600"
	_, err := e.service.Collect(context.Background(), "space-1", e.target, []store.TradingAccountRecord{e.account})
	require.NoError(t, err)
	calls := e.exchange.Calls()
	require.Len(t, calls, 1)
	require.Equal(t, "399", calls[0].Amount.String())

	// 现货钱包已经有 0.2 个 BNB（市值 120 ≥ 100）：补买不需要 USDT，只留 1U，归集 499。
	covered := newEnvWithFund(t, "500", "1000", `{"bnb_reserve_amount":"200"}`)
	covered.exchange.BNB, covered.exchange.BNBPrice = "0.2", "600"
	_, err = covered.service.Collect(context.Background(), "space-1", covered.target, []store.TradingAccountRecord{covered.account})
	require.NoError(t, err)
	require.Equal(t, "499", covered.exchange.Calls()[0].Amount.String())
}

// 资金设置读不出来：不知道该留多少补买资金，不归集并告警，不是静默只留 1U。
func TestCollectSkipsAndAlertsWhenFundSettingsAreUnreadable(t *testing.T) {
	for name, fund := range map[string]string{
		"排除资产设置读不出来":   `{"arbitrage_base_assets":"BTC"}`,
		"BNB 保留金额读不出来": `{"bnb_reserve_amount":"oops"}`,
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnvWithFund(t, "500", "1000", fund)
			pending, err := e.service.Collect(context.Background(), "space-1", e.target, []store.TradingAccountRecord{e.account})
			require.NoError(t, err)
			require.False(t, pending)
			require.Empty(t, e.exchange.Calls())
			require.NotEmpty(t, telemetry.Recent(telemetry.AttentionTransferFailing, e.now, time.Minute))
		})
	}
}

func TestMatchesRowChecksDirectionAssetAmountAndTime(t *testing.T) {
	requestedAt := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	row := store.WalletTransferRecord{Direction: store.TransferToSpot, Asset: "USDT", Amount: "500", RequestedAt: requestedAt.UnixMilli()}
	good := execution.TransferRecord{From: exchange.WalletFutures, To: exchange.WalletSpot, Asset: "USDT", Amount: shared.MustDecimal("500"), At: requestedAt.Add(time.Second)}
	require.True(t, matchesRow(good, row))
	wrongDirection := good
	wrongDirection.From, wrongDirection.To = exchange.WalletSpot, exchange.WalletFutures
	require.False(t, matchesRow(wrongDirection, row))
	wrongAmount := good
	wrongAmount.Amount = shared.MustDecimal("500.1")
	require.False(t, matchesRow(wrongAmount, row))
	wrongAsset := good
	wrongAsset.Asset = "BNB"
	require.False(t, matchesRow(wrongAsset, row))
	tooOld := good
	tooOld.At = requestedAt.Add(-10 * time.Minute)
	require.False(t, matchesRow(tooOld, row), "请求之前很久的同金额划转不是这一笔")
}

// 人工结束 PENDING 记录：太新的不让动；记 FAILED 带“未到账”软标记、不计入连续失败，这个目标可以重试；
// 记 COMPLETED 之后照常等快照；已经结束的原样返回。
func TestResolveStuckPendingTransferManually(t *testing.T) {
	e := newEnv(t, "10", "1000")
	e.exchange.TransferErr = &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: errors.New("timeout")}
	require.True(t, waiting(e.before("0.01")), "结果不明：记录保持 PENDING")
	row := e.transfers(store.TransferToSpot)[0]
	ctx := context.Background()

	_, err := Resolve(ctx, e.store, "space-1", row.TransferID, store.TransferStateFailed, "", e.now.Add(5*time.Minute))
	require.ErrorIs(t, err, store.ErrInvalidRecord)
	require.Contains(t, err.Error(), "先等自动对账")
	_, err = Resolve(ctx, e.store, "space-1", row.TransferID, "PENDING", "", e.now.Add(time.Hour))
	require.ErrorIs(t, err, store.ErrInvalidRecord)
	_, err = Resolve(ctx, e.store, "space-1", "no-such", store.TransferStateFailed, "", e.now.Add(time.Hour))
	require.Error(t, err)

	later := e.now.Add(11 * time.Minute)
	result, err := Resolve(ctx, e.store, "space-1", row.TransferID, store.TransferStateFailed, "交易所没有这笔", later)
	require.NoError(t, err)
	require.Equal(t, ResolveResult{State: store.TransferStateFailed}, result)
	stored := e.transfers(store.TransferToSpot)[0]
	require.Equal(t, store.TransferStateFailed, stored.State)
	require.True(t, strings.HasPrefix(stored.LastError, softNotArrive), "人工记没到账不计入连续失败")
	require.Contains(t, stored.LastError, "交易所没有这笔")
	failures, _ := trailingHardFailures(e.transfers(store.TransferToSpot))
	require.Zero(t, failures)

	again, err := Resolve(ctx, e.store, "space-1", row.TransferID, store.TransferStateCompleted, "", later)
	require.NoError(t, err)
	require.Equal(t, ResolveResult{State: store.TransferStateFailed, AlreadySettled: true}, again, "已经结束的原样返回")

	// 账户不再被这条记录挡住：目标可以重新发起划转。
	e.exchange.TransferErr = nil
	e.now = later
	require.True(t, waiting(e.before("0.01")))
	require.Len(t, e.exchange.Calls(), 2)
}

func TestResolveAsCompletedUnblocksAccountAndKeepsAmount(t *testing.T) {
	e := newEnv(t, "10", "1000")
	e.exchange.TransferErr = &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: errors.New("timeout")}
	require.True(t, waiting(e.before("0.01")))
	row := e.transfers(store.TransferToSpot)[0]
	later := e.now.Add(11 * time.Minute)
	result, err := Resolve(context.Background(), e.store, "space-1", row.TransferID, store.TransferStateCompleted, "", later)
	require.NoError(t, err)
	require.Equal(t, store.TransferStateCompleted, result.State)
	stored := e.transfers(store.TransferToSpot)[0]
	require.Equal(t, later.UnixMilli(), stored.CompletedAt)
	require.Empty(t, stored.LastError)
}

// 这个目标已经把现货钱包多出来的资金归集回合约钱包：不再往现货钱包划（买单因为非资金原因下不了单时，
// 归集了又划进来，钱会一直停在现货钱包），只按现有资金缩小；归集失败的记录不算。
func TestNoTransferToSpotAfterTheTargetAlreadyCollected(t *testing.T) {
	e := newEnv(t, "10", "1000")
	ctx := context.Background()
	require.NoError(t, e.store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.InsertWalletTransfer(store.WalletTransferRecord{
			SpaceID: "space-1", TransferID: "collect-1", TradingAccountID: "account-1", LogicalAccountID: "logical-1", TargetID: "target-1",
			Direction: store.TransferToFutures, Asset: "USDT", Amount: "509", ClientRef: "target-1:TO_FUTURES:1",
			State: store.TransferStateCompleted, RequestedAt: e.now.UnixMilli(), CompletedAt: e.now.UnixMilli(),
		})
	}))
	outcome := e.before("0.01")
	require.False(t, waiting(outcome))
	require.Empty(t, e.exchange.Calls(), "不再往现货钱包划")
	require.NotEmpty(t, outcome.BuyScale["account-1"])
	require.Contains(t, outcome.Reasons["account-1"], "归集")
	require.False(t, IsFundsShortage(outcome.Reasons["account-1"]), "不是资金不足")

	// 归集失败（没成功）的记录不算归集过。
	failed := newEnv(t, "10", "1000")
	require.NoError(t, failed.store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.InsertWalletTransfer(store.WalletTransferRecord{
			SpaceID: "space-1", TransferID: "collect-1", TradingAccountID: "account-1", LogicalAccountID: "logical-1", TargetID: "target-1",
			Direction: store.TransferToFutures, Asset: "USDT", Amount: "509", ClientRef: "target-1:TO_FUTURES:1",
			State: store.TransferStateFailed, LastError: "被交易所拒绝", RequestedAt: failed.now.UnixMilli(),
		})
	}))
	require.True(t, waiting(failed.before("0.01")))
	require.Len(t, failed.exchange.Calls(), 1)
}

// 缺口不到起划额（买 0.0002 BTC @ 50000 = 10U，需要 10.2U）：现货够付名义金额就不缩小；不够只缺一点，按起划额 1U 划转。
func TestTinyShortfallBelowMinimumTransfer(t *testing.T) {
	covered := newEnv(t, "10.1", "1000")
	outcome := covered.before("0.0002")
	require.False(t, waiting(outcome))
	require.Empty(t, outcome.BuyScale, "10.1 够付 10 的名义金额：不缩小")
	require.Empty(t, outcome.Reasons)
	require.Empty(t, covered.exchange.Calls())

	short := newEnv(t, "9.5", "1000")
	require.True(t, waiting(short.before("0.0002")), "9.5 不够付 10 的名义金额：按起划额划转，不把买单缩小")
	require.Equal(t, "1", short.exchange.Calls()[0].Amount.String())
}

// waiting 报告本轮有没有账户在等（划转、对账、等快照或退避）。
func waiting(outcome Outcome) bool { return len(outcome.Waiting) > 0 }
