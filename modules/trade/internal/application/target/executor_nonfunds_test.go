package target

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/wallet"
	"github.com/mooyang-code/moox/modules/trade/internal/application/wallet/wallettest"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// realWalletFixture 建普通账户组合，用真实 wallet.Service + 假交易所；执行器价格 = 假交易所卖一价 = price。
func realWalletFixture(t *testing.T, price, spot, futures, targetQty string) (*targetFixture, *wallettest.Exchange, func() *Executor) {
	t.Helper()
	f := liveStandardFixture(t)
	f.setOrderSettings(t, `{"order_mode":"MARKET","min_notional_spot":"10","min_notional_swap":"5"}`)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SPOT", Quantity: targetQty}})
	fake := &wallettest.Exchange{Spot: spot, Futures: futures, Ask: price}
	newExecutor := func() *Executor {
		executor := f.executor()
		executor.Prices = targetPriceStub{price: shared.MustDecimal(price)}
		executor.Transfers = NewWalletTransfer(&wallet.Service{Store: f.store, Adapters: wallettest.Adapters{"account-a": fake}})
		return executor
	}
	return f, fake, newExecutor
}

// 非资金原因下不了的买单（这里是交易所最小名义金额挡住新仓）：调钱包前就记 BLOCKED，不划转。
// 否则会先划进现货钱包、再归集回合约钱包、再划进来，最后钱停在现货钱包。
func TestNonFundsUnplaceableBuyNeverTransfers(t *testing.T) {
	f, fake, newExecutor := realWalletFixture(t, "50000", "10", "600", "0.01")
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_trade_instruments SET c_min_notional = '100000' WHERE c_instrument_id = 'BTC-USDT-SPOT'").Error)
	ctx := context.Background()
	executor := newExecutor()
	for round := 1; round <= 6; round++ {
		result, err := executor.Converge(ctx, "space-1", "logical-1")
		require.NoError(t, err)
		require.Equal(t, StatusBlocked, result.Status, "第 %d 轮", round)
		fake.ConfirmAll()
		markSpotSnapshot(t, f, time.Now().Add(time.Duration(round)*time.Second))
	}
	require.Empty(t, fake.Calls(), "注定下不了的单不划转，也就没有归集")
	require.Equal(t, "10", fake.Spot)
	stored, err := f.store.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Len(t, stored.BlockedTargets, 1)
	require.Equal(t, "0.01", stored.BlockedTargets[0].Quantity)
	require.False(t, wallet.IsFundsShortage(stored.BlockedTargets[0].Reason), "是非资金原因："+stored.BlockedTargets[0].Reason)
}

// 本目标里这个账户已经拒过这笔买单（单成员）：已知下不了的单也不先划转。
func TestRejectedAccountBuyNeverTransfers(t *testing.T) {
	f, fake, newExecutor := realWalletFixture(t, "50000", "10", "600", "0.01")
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", TradingAccountID: "account-a", OrderID: "rejected-child", ClientOrderID: "rejected-child",
		Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "BTCUSDT", InstrumentID: "BTC-USDT-SPOT",
		OrderType: "MARKET", Side: "BUY", Quantity: "0.01", ReferencePrice: "50000", State: "REJECTED",
		RejectReason: "This symbol is not permitted for this account", SubmittedAt: 1_900, OwnerType: "TARGET", OwnerID: "target-current",
		LogicalAccountID: "logical-1", RunnerID: "runner-1",
	})
	ctx := context.Background()
	executor := newExecutor()
	for round := 1; round <= 5; round++ {
		result, err := executor.Converge(ctx, "space-1", "logical-1")
		require.NoError(t, err)
		require.Equal(t, StatusBlocked, result.Status)
		fake.ConfirmAll()
		markSpotSnapshot(t, f, time.Now().Add(time.Duration(round)*time.Second))
	}
	require.Empty(t, fake.Calls())
	stored, err := f.store.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Len(t, stored.BlockedTargets, 1)
	require.Contains(t, stored.BlockedTargets[0].Reason, "not permitted")
}

// 缺口不到起划额（需要 0.7U）：不把买单缩得下不了单，按起划额 1U 划转；到账后整笔买入。
func TestTinyShortfallTransfersMinimumInsteadOfShrinkingBuy(t *testing.T) {
	f, fake, newExecutor := realWalletFixture(t, "2000", "9.5", "100000", "0.005")
	ctx := context.Background()
	executor := newExecutor()
	result, err := executor.Converge(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "transfer", result.Action)
	require.Len(t, fake.Calls(), 1)
	require.Equal(t, "1", fake.Calls()[0].Amount.String())
	fake.ConfirmAll()
	markSpotSnapshot(t, f, time.Now().Add(time.Second))
	result, err = executor.Converge(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "place", result.Action)
	require.Equal(t, "0.005", f.orders.specs[0].Quantity.String(), "整笔买入，没有被缩小")
}

// 现货钱包已经够付买单名义金额（不含 2% 余量）：不划转也不缩小。
func TestSpotCoveringNotionalNeitherTransfersNorShrinks(t *testing.T) {
	f, fake, newExecutor := realWalletFixture(t, "2000", "10.1", "100000", "0.005")
	result, err := newExecutor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "place", result.Action)
	require.Empty(t, fake.Calls())
	require.Equal(t, "0.005", f.orders.specs[0].Quantity.String())
}

var _ = exchange.WalletSpot
