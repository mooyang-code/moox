package wallet

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"github.com/stretchr/testify/require"
)

// fakeExchange 是假交易所：余额、划转记录都可以由测试控制，Transfer 的行为可以脚本化（成功、超时、被拒绝）。
type fakeExchange struct {
	execution.ExecutionAdapter
	mu               sync.Mutex
	spot, futures    string
	ask              string
	transferCalls    []fakeTransferCall
	records          []execution.TransferRecord
	transferErr      error
	transferDelivers bool // 请求报错时交易所是否实际执行了划转（超时但已到账）
	nextID           int
}

type fakeTransferCall struct {
	From, To  exchange.Wallet
	Asset     string
	Amount    shared.Decimal
	ClientRef string
}

func (f *fakeExchange) GetQuote(context.Context, exchange.SymbolKey) (execution.MarketQuote, error) {
	return execution.MarketQuote{Ask: shared.MustDecimal(f.ask), Last: shared.MustDecimal(f.ask)}, nil
}

func (f *fakeExchange) GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return exchange.AccountSnapshot{Balances: []exchange.AssetBalance{
		{Wallet: exchange.WalletSpot, Asset: "USDT", Available: shared.MustDecimal(f.spot), Total: shared.MustDecimal(f.spot)},
		{Wallet: exchange.WalletFutures, Asset: "USDT", Available: shared.MustDecimal(f.futures), Total: shared.MustDecimal(f.futures)},
	}}, nil
}

func (f *fakeExchange) Transfer(_ context.Context, from, to exchange.Wallet, asset string, amount shared.Decimal, clientRef string) (execution.TransferReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transferCalls = append(f.transferCalls, fakeTransferCall{from, to, asset, amount, clientRef})
	f.nextID++
	id := fmt.Sprintf("tran-%d", f.nextID)
	if f.transferErr != nil && !f.transferDelivers {
		return execution.TransferReceipt{}, f.transferErr
	}
	f.records = append(f.records, execution.TransferRecord{
		ExchangeTransferID: id, Asset: asset, Amount: amount, From: from, To: to, Status: execution.TransferPending, At: time.Now(),
	})
	if f.transferErr != nil {
		return execution.TransferReceipt{}, f.transferErr
	}
	return execution.TransferReceipt{ExchangeTransferID: id}, nil
}

func (f *fakeExchange) ListTransfers(context.Context, time.Time) ([]execution.TransferRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]execution.TransferRecord(nil), f.records...), nil
}

// confirmAll 让交易所把所有划转记录置为已到账，并按金额移动余额。
func (f *fakeExchange) confirmAll(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.records {
		if f.records[i].Status == execution.TransferConfirmed {
			continue
		}
		f.records[i].Status = execution.TransferConfirmed
		spot, futures := shared.MustDecimal(f.spot), shared.MustDecimal(f.futures)
		if f.records[i].To == exchange.WalletSpot {
			spot, futures = spot.Add(f.records[i].Amount), futures.Sub(f.records[i].Amount)
		} else {
			spot, futures = spot.Sub(f.records[i].Amount), futures.Add(f.records[i].Amount)
		}
		f.spot, f.futures = spot.String(), futures.String()
	}
}

func (f *fakeExchange) calls() []fakeTransferCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeTransferCall(nil), f.transferCalls...)
}

type fakeAdapters struct{ adapter execution.ExecutionAdapter }

func (f fakeAdapters) Adapter(string) (execution.ExecutionAdapter, error) { return f.adapter, nil }

type env struct {
	t        *testing.T
	store    *store.Store
	exchange *fakeExchange
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
		t: t, store: tradeStore, exchange: &fakeExchange{spot: spot, futures: futures, ask: "50000"},
		now: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC), account: account,
		target: store.LogicalAccountTargetRecord{SpaceID: "space-1", LogicalAccountID: "logical-1", TargetID: "target-1"},
	}
	e.service = e.newService()
	return e
}

// newService 创建一个服务实例；“重启”就是用同一个库再建一个。
func (e *env) newService() *Service {
	counter := 0
	return &Service{
		Store: e.store, Adapters: fakeAdapters{adapter: e.exchange},
		RequestSync: func(id string) { e.syncs = append(e.syncs, id) },
		Now:         func() time.Time { return e.now },
		NewID:       func() string { counter++; return fmt.Sprintf("t-%d-%d", e.now.UnixNano(), counter) },
	}
}

func (e *env) buys(quantity string) []target.TransferBuy {
	return []target.TransferBuy{{
		Account: e.account,
		Instrument: store.InstrumentRecord{
			InstrumentID: "BTC-USDT-SPOT", MarketType: "SPOT", ExchangeSymbol: "BTCUSDT",
		},
		Quantity: shared.MustDecimal(quantity),
	}}
}

func (e *env) markSynced(at time.Time) {
	e.t.Helper()
	require.NoError(e.t, e.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpdateTradingAccountSync("space-1", "account-1", store.TradingAccountSyncState{
			LastSyncAt: at.UnixMilli(), LeverageSettings: store.LeverageSettings{store.DefaultLeverageKey: "1"},
		})
	}))
}

func (e *env) transfers(direction string) []store.WalletTransferRecord {
	e.t.Helper()
	records, err := e.store.ListWalletTransfersForTarget(context.Background(), "space-1", "logical-1", "target-1", "account-1", direction)
	require.NoError(e.t, err)
	return records
}

// 现货钱包不够：从合约钱包划转 max(0, 买单名义 × 1.02 − 现货可用)；划转完成、现货快照追上之后才放行买入。
func TestBeforeSpotBuyTransfersShortfallAndWaitsForSnapshot(t *testing.T) {
	e := newEnv(t, "10", "1000")
	ctx := context.Background()
	buys := e.buys("0.01") // 0.01 × 50000 = 500，× 1.02 = 510

	outcome, err := e.service.BeforeSpotBuy(ctx, "space-1", e.target, buys)
	require.NoError(t, err)
	require.True(t, outcome.Pending)
	calls := e.exchange.calls()
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
		outcome, err = e.service.BeforeSpotBuy(ctx, "space-1", e.target, buys)
		require.NoError(t, err)
		require.True(t, outcome.Pending)
	}
	require.Len(t, e.exchange.calls(), 1, "没有幂等键，靠本地 PENDING 记录避免重复划转")

	// 到账了，但现货钱包的快照还没追上：继续等，并请求同步。
	e.exchange.confirmAll(t)
	e.now = e.now.Add(2 * time.Second)
	e.markSynced(e.now.Add(-time.Minute))
	outcome, err = e.service.BeforeSpotBuy(ctx, "space-1", e.target, buys)
	require.NoError(t, err)
	require.True(t, outcome.Pending)
	require.Equal(t, []string{"account-1"}, e.syncs)
	require.Equal(t, store.TransferStateCompleted, e.transfers(store.TransferToSpot)[0].State)

	// 快照追上后放行，按现在的余额不缩小。
	e.markSynced(e.now.Add(time.Second))
	outcome, err = e.service.BeforeSpotBuy(ctx, "space-1", e.target, buys)
	require.NoError(t, err)
	require.False(t, outcome.Pending)
	require.Empty(t, outcome.BuyScale)
	require.Len(t, e.exchange.calls(), 1)
}

// 划转刚完成就重启：状态在划转表里，新实例接着走，不重复划转，也不会漏等快照。
func TestRestartRightAfterTransferCompletedResumesWithoutDuplicate(t *testing.T) {
	e := newEnv(t, "10", "1000")
	ctx := context.Background()
	buys := e.buys("0.01")
	_, err := e.service.BeforeSpotBuy(ctx, "space-1", e.target, buys)
	require.NoError(t, err)
	e.exchange.confirmAll(t)
	e.now = e.now.Add(time.Second)
	e.markSynced(e.now.Add(-time.Hour))
	outcome, err := e.service.BeforeSpotBuy(ctx, "space-1", e.target, buys)
	require.NoError(t, err)
	require.True(t, outcome.Pending, "划转已完成，等快照")

	restarted := e.newService()
	outcome, err = restarted.BeforeSpotBuy(ctx, "space-1", e.target, buys)
	require.NoError(t, err)
	require.True(t, outcome.Pending, "重启后仍要等现货钱包的快照")
	e.now = e.now.Add(time.Second)
	e.markSynced(e.now)
	outcome, err = restarted.BeforeSpotBuy(ctx, "space-1", e.target, buys)
	require.NoError(t, err)
	require.False(t, outcome.Pending)
	require.Len(t, e.exchange.calls(), 1)
}

// 请求超时但交易所其实已经执行：下一轮靠交易所划转记录（时间窗口、金额、方向）认领，不会重复划转。
func TestTimeoutAfterDeliveryIsAdoptedFromExchangeRecordsWithoutRetry(t *testing.T) {
	e := newEnv(t, "10", "1000")
	ctx := context.Background()
	buys := e.buys("0.01")
	e.exchange.transferErr = &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: errors.New("timeout")}
	e.exchange.transferDelivers = true

	outcome, err := e.service.BeforeSpotBuy(ctx, "space-1", e.target, buys)
	require.Error(t, err, "结果不明要报错让上层知道")
	require.False(t, outcome.Pending)
	rows := e.transfers(store.TransferToSpot)
	require.Len(t, rows, 1)
	require.Equal(t, store.TransferStatePending, rows[0].State, "结果不明：记录保持 PENDING")
	require.Empty(t, rows[0].ExchangeTransferID)

	e.exchange.transferErr = nil
	e.now = e.now.Add(2 * time.Second)
	e.exchange.confirmAll(t)
	e.markSynced(e.now.Add(time.Minute))
	outcome, err = e.service.BeforeSpotBuy(ctx, "space-1", e.target, buys)
	require.NoError(t, err)
	require.False(t, outcome.Pending)
	require.Len(t, e.exchange.calls(), 1, "划转已经执行，不能再划一次")
	rows = e.transfers(store.TransferToSpot)
	require.Equal(t, store.TransferStateCompleted, rows[0].State)
	require.Equal(t, "tran-1", rows[0].ExchangeTransferID)
}

// 请求超时且交易所确实没执行：过了对账宽限期，交易所记录里仍没有，才作废这条记录并重试。
func TestTimeoutWithoutDeliveryRetriesOnlyAfterGracePeriod(t *testing.T) {
	e := newEnv(t, "10", "1000")
	ctx := context.Background()
	buys := e.buys("0.01")
	e.exchange.transferErr = &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: errors.New("timeout")}
	_, err := e.service.BeforeSpotBuy(ctx, "space-1", e.target, buys)
	require.Error(t, err)
	e.exchange.transferErr = nil

	e.now = e.now.Add(10 * time.Second)
	outcome, err := e.service.BeforeSpotBuy(ctx, "space-1", e.target, buys)
	require.NoError(t, err)
	require.True(t, outcome.Pending, "宽限期内记录里没有也不能下结论")
	require.Len(t, e.exchange.calls(), 1)

	e.now = e.now.Add(2 * confirmGrace)
	outcome, err = e.service.BeforeSpotBuy(ctx, "space-1", e.target, buys)
	require.NoError(t, err)
	require.True(t, outcome.Pending, "重试已发出，等到账")
	require.Len(t, e.exchange.calls(), 2)
	rows := e.transfers(store.TransferToSpot)
	require.Len(t, rows, 2)
	require.Equal(t, store.TransferStateFailed, rows[0].State)
	require.NotEqual(t, rows[0].ClientRef, rows[1].ClientRef)
}

func TestRejectedTransferIsFailedAndStopsAfterRepeatedFailures(t *testing.T) {
	e := newEnv(t, "10", "1000")
	ctx := context.Background()
	buys := e.buys("0.01")
	e.exchange.transferErr = &exchange.Error{Kind: exchange.ErrorRejected, Code: "-5013", Err: errors.New("asset not available")}
	for attempt := 1; attempt <= maxTransferFailures; attempt++ {
		e.now = e.now.Add(time.Second)
		_, err := e.service.BeforeSpotBuy(ctx, "space-1", e.target, buys)
		require.Error(t, err)
		require.Len(t, e.exchange.calls(), attempt)
	}
	e.now = e.now.Add(time.Second)
	_, err := e.service.BeforeSpotBuy(ctx, "space-1", e.target, buys)
	require.ErrorContains(t, err, "不再重试")
	require.Len(t, e.exchange.calls(), maxTransferFailures)
	for _, row := range e.transfers(store.TransferToSpot) {
		require.Equal(t, store.TransferStateFailed, row.State)
	}
}

// 额度不足：合约钱包只能划出 99（100 × 0.99），告警，划转完成后现货买单按“能用的 ÷ 需要的”等比缩小。
func TestInsufficientFuturesCapacityScalesSpotBuysDown(t *testing.T) {
	e := newEnv(t, "10", "100")
	ctx := context.Background()
	buys := e.buys("0.01") // 需要 510
	before := len(telemetry.Recent(telemetry.AttentionTransferShortfall, e.now, time.Minute))

	outcome, err := e.service.BeforeSpotBuy(ctx, "space-1", e.target, buys)
	require.NoError(t, err)
	require.True(t, outcome.Pending)
	calls := e.exchange.calls()
	require.Len(t, calls, 1)
	require.Equal(t, "99", calls[0].Amount.String(), "不超过合约钱包可划额度 × 0.99")
	require.Greater(t, len(telemetry.Recent(telemetry.AttentionTransferShortfall, e.now, time.Minute)), before, "额度不足要告警")

	e.exchange.confirmAll(t)
	e.now = e.now.Add(time.Second)
	e.markSynced(e.now.Add(time.Second))
	outcome, err = e.service.BeforeSpotBuy(ctx, "space-1", e.target, buys)
	require.NoError(t, err)
	// 到账后现货 109，合约 1 已经划不出第二笔（低于最小划转额）：缩放 109 / 510。
	require.False(t, outcome.Pending)
	scale, ok := outcome.BuyScale["account-1"]
	require.True(t, ok)
	require.Equal(t, shared.MustDecimal("109").Div(shared.MustDecimal("510")).String(), scale.String())
	// 比例按本轮算：数量减半后需求 255，现货 109 → 109 / 255，而不是在上一轮比例上累计。
	outcome, err = e.service.BeforeSpotBuy(ctx, "space-1", e.target, e.buys("0.005"))
	require.NoError(t, err)
	require.Equal(t, shared.MustDecimal("109").Div(shared.MustDecimal("255")).String(), outcome.BuyScale["account-1"].String())
}

func TestNoFuturesFundsScalesBuysToAvailableSpotWithoutTransfer(t *testing.T) {
	e := newEnv(t, "0", "0")
	outcome, err := e.service.BeforeSpotBuy(context.Background(), "space-1", e.target, e.buys("0.01"))
	require.NoError(t, err)
	require.False(t, outcome.Pending)
	require.Empty(t, e.exchange.calls())
	scale, ok := outcome.BuyScale["account-1"]
	require.True(t, ok)
	require.True(t, scale.IsZero(), "没有任何资金：这轮买不了")
}

func TestEnoughSpotFundsDoesNotTransfer(t *testing.T) {
	e := newEnv(t, "600", "1000")
	outcome, err := e.service.BeforeSpotBuy(context.Background(), "space-1", e.target, e.buys("0.01"))
	require.NoError(t, err)
	require.False(t, outcome.Pending)
	require.Empty(t, outcome.BuyScale)
	require.Empty(t, e.exchange.calls())
}

// 归集：现货钱包多出来的资金（留 1U）达到阈值才划回合约钱包；每个目标最多归集一次。
func TestCollectMovesExcessBackOncePerTarget(t *testing.T) {
	e := newEnv(t, "500", "50")
	ctx := context.Background()
	accounts := []store.TradingAccountRecord{e.account}

	pending, err := e.service.Collect(ctx, "space-1", e.target, accounts)
	require.NoError(t, err)
	require.True(t, pending)
	calls := e.exchange.calls()
	require.Len(t, calls, 1)
	require.Equal(t, exchange.WalletSpot, calls[0].From)
	require.Equal(t, exchange.WalletFutures, calls[0].To)
	require.Equal(t, "499", calls[0].Amount.String(), "现货钱包留 1U")

	pending, err = e.service.Collect(ctx, "space-1", e.target, accounts)
	require.NoError(t, err)
	require.True(t, pending, "还没到账继续等")
	require.Len(t, e.exchange.calls(), 1)

	e.exchange.confirmAll(t)
	e.now = e.now.Add(time.Second)
	pending, err = e.service.Collect(ctx, "space-1", e.target, accounts)
	require.NoError(t, err)
	require.False(t, pending)
	pending, err = e.service.Collect(ctx, "space-1", e.target, accounts)
	require.NoError(t, err)
	require.False(t, pending)
	require.Len(t, e.exchange.calls(), 1, "同一个目标不会再归集")
	require.Equal(t, store.TransferStateCompleted, e.transfers(store.TransferToFutures)[0].State)
}

func TestCollectSkipsExcessBelowThreshold(t *testing.T) {
	// 多出 49 < 100U 的下限，不划，避免 1 分钟级策略每期来回划转。
	e := newEnv(t, "50", "1000")
	pending, err := e.service.Collect(context.Background(), "space-1", e.target, []store.TradingAccountRecord{e.account})
	require.NoError(t, err)
	require.False(t, pending)
	require.Empty(t, e.exchange.calls())
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
	require.Empty(t, e.exchange.calls())
}

func TestCollectKeepsHalfOfBNBReserveInSpotWallet(t *testing.T) {
	// 开启 BNB 保留 200：现货钱包再多留 100U 给 BNB 补买，归集金额 500 − 1 − 100 = 399。
	e := newEnvWithFund(t, "500", "1000", `{"bnb_reserve_amount":"200"}`)
	_, err := e.service.Collect(context.Background(), "space-1", e.target, []store.TradingAccountRecord{e.account})
	require.NoError(t, err)
	calls := e.exchange.calls()
	require.Len(t, calls, 1)
	require.Equal(t, "399", calls[0].Amount.String())
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
