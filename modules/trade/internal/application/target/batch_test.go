package target

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/ordersettings"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func (f *targetFixture) addETH(t *testing.T) {
	t.Helper()
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		for _, instrument := range f.instruments() {
			instrument.InstrumentID = "ETH-USDT-" + string(f.market)
			instrument.BaseAsset = "ETH"
			if instrument.ContractValueAsset != "" {
				instrument.ContractValueAsset = "ETH"
			}
			if instrument.Exchange == "BINANCE" {
				instrument.ExchangeSymbol = "ETHUSDT"
			} else {
				instrument.ExchangeSymbol = "ETH-USDT-SWAP"
			}
			if err := tx.UpsertInstrument(instrument); err != nil {
				return err
			}
		}
		return nil
	}))
}

func batchTargets() []store.InstrumentTarget {
	return []store.InstrumentTarget{
		{InstrumentID: "BTC-USDT-SWAP", Quantity: "2"},
		{InstrumentID: "ETH-USDT-SWAP", Quantity: "3"},
	}
}

func TestBatchPlacesAllLanesInOneRound(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	f.addETH(t)
	f.target(t, batchTargets())
	result, err := f.executor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "place", result.Action)
	require.Len(t, f.orders.specs, 2)
	require.Len(t, f.orders.submitted, 2)
}

// 先减后加：本批有减仓时不加仓。
func TestBatchReducesBeforeAdding(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	f.addETH(t)
	f.position(t, "account-a", "BTCUSDT", "5")
	f.target(t, batchTargets())
	_, err := f.executor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Len(t, f.orders.specs, 1)
	require.Equal(t, "BTC-USDT-SWAP", f.orders.specs[0].InstrumentID)
	require.True(t, f.orders.specs[0].ReducePositionOnly)
}

// 减仓全部下不了时照常加仓。
func TestBatchAddsWhenAllReductionsAreBlocked(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	f.addETH(t)
	f.position(t, "account-a", "BTCUSDT", "5")
	// BTC 卖单已成交但持仓没反映：减仓被下单前持仓校验挡住。
	f.order(t, filledChild("old-sell", "target-old", "TARGET", "SELL", "5", 1_500))
	f.target(t, batchTargets())
	_, err := f.executor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Len(t, f.orders.specs, 1)
	require.Equal(t, "ETH-USDT-SWAP", f.orders.specs[0].InstrumentID)
}

// 额度按执行账户计：用完算本轮跳过，不换到下一个成员。
func TestBatchQuotaDefersWithoutRerouting(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	f.addETH(t)
	f.target(t, batchTargets())
	executor := f.executor()
	executor.BatchSize = func(string) int { return 1 }
	result, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "place", result.Action)
	require.Len(t, f.orders.specs, 1)
	require.Equal(t, "account-a", f.orders.specs[0].TradingAccountID)
}

func TestBatchIntervalPacesNextBatch(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "2"}})
	executor := f.executor()
	executor.MinBatchInterval = time.Hour
	_, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Len(t, f.orders.specs, 1)
	result, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "pace", result.Action)
	require.Equal(t, StatusConverging, result.Status)
	require.Len(t, f.orders.specs, 1)
	// 只因批间隔未到时带回还要等的时间，worker 到期直接唤醒。
	require.Greater(t, result.RetryAfter, time.Duration(0))
	require.LessOrEqual(t, result.RetryAfter, time.Hour)
}

// 成员全部是模拟盘时用模拟盘批间隔，混有实盘成员时用实盘间隔。
func TestBatchIntervalForPaperMembers(t *testing.T) {
	executor := &Executor{MinBatchInterval: time.Second, PaperMinBatchInterval: 300 * time.Millisecond}
	paper := memberState{account: store.TradingAccountRecord{ExecutionMode: string(exchange.ExecutionModePaper)}}
	live := memberState{account: store.TradingAccountRecord{ExecutionMode: string(exchange.ExecutionModeLive)}}
	defaults := ordersettings.Default()
	require.Equal(t, 300*time.Millisecond, executor.batchInterval([]memberState{paper, paper}, defaults))
	require.Equal(t, time.Second, executor.batchInterval([]memberState{paper, live}, defaults))
	executor.PaperMinBatchInterval = 0
	require.Equal(t, time.Second, executor.batchInterval([]memberState{paper}, defaults))
	// 下单设置里写了批间隔就用设置的，实盘、模拟盘都一样。
	configured, err := ordersettings.Parse(`{"batch_interval_seconds":5}`)
	require.NoError(t, err)
	require.Equal(t, 5*time.Second, executor.batchInterval([]memberState{paper}, configured))
	require.Equal(t, 5*time.Second, executor.batchInterval([]memberState{live}, configured))
}

// 权重用到 90% 或接口暂停时不发新批，也不算阻塞。
func TestBatchStopsWhenRateLimited(t *testing.T) {
	for name, status := range map[string]exchange.RateStatus{
		"usage":  {UsedWeight: 2200, WeightLimit: 2400},
		"paused": {PausedUntil: time.UnixMilli(2_000).Add(time.Minute)},
		"orders": {OrdersPausedUntil: time.UnixMilli(2_000).Add(time.Minute)},
	} {
		t.Run(name, func(t *testing.T) {
			f := newTargetFixture(t, exchange.MarketTypeSwap)
			f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "2"}})
			executor := f.executor()
			executor.RateStatus = func(string) (exchange.RateStatus, bool) { return status, true }
			result, err := executor.Converge(context.Background(), "space-1", "logical-1")
			require.NoError(t, err)
			require.Equal(t, "pace", result.Action)
			require.Empty(t, f.orders.specs)
			require.Empty(t, latestBlocked(t, f))
		})
	}
}

// 有在途子单时本轮只等，不发新批。
func TestInFlightChildBlocksNewBatch(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	f.addETH(t)
	f.target(t, batchTargets())
	open := filledChild("open-child", "target-current", "TARGET", "BUY", "0", 0)
	open.State, open.FilledQuantity, open.FinishedAt, open.SubmittedAt = "OPEN", "0", 0, 1_000
	f.order(t, open)
	result, err := f.executor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "wait", result.Action)
	require.Empty(t, f.orders.specs)
	require.Empty(t, f.orders.canceled)
}

// 从未发出的待提交子单是本地残留：丢弃后按最新事实重算。
func TestResidualPendingChildIsDiscarded(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	f.orders.store = f.store
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "2"}})
	pending := filledChild("residual", "target-current", "TARGET", "BUY", "0", 0)
	pending.State, pending.FilledQuantity, pending.FinishedAt, pending.SubmittedAt = "PENDING", "0", 0, 0
	f.order(t, pending)
	_, err := f.executor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, []string{"residual"}, f.orders.discarded)
	require.Len(t, f.orders.specs, 1)
}
