package target

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/wallet"
	"github.com/mooyang-code/moox/modules/trade/internal/application/wallet/wallettest"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// “持仓未反映已成交子单”是暂时状态，买单马上要继续：这一轮不能把现货钱包的钱归集回合约钱包，
// 否则同步追上后剩下的买单被“这个目标已经归集过，不再追加划转”拦住，整个目标期买不到。
func TestUnreflectedPositionHoldsCollectSoTheRestCanStillBeBought(t *testing.T) {
	f, fake, newExecutor := realWalletFixture(t, "50000", "900", "1000", "0.01")
	f.now = time.Now().UTC()
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", TradingAccountID: "account-a", OrderID: "filled-1", ClientOrderID: "filled-1",
		Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "BTCUSDT", InstrumentID: "BTC-USDT-SPOT",
		OrderType: "MARKET", Side: "BUY", Quantity: "0.002", ReferencePrice: "50000", State: "FILLED", FilledQuantity: "0.002", AveragePrice: "50000",
		PositionBefore: "0", SubmittedAt: f.now.Add(-time.Minute).UnixMilli(), FinishedAt: f.now.Add(-time.Minute).UnixMilli(),
		OwnerType: "TARGET", OwnerID: "target-current", LogicalAccountID: "logical-1", RunnerID: "runner-1", Version: 1,
	})
	ctx := context.Background()
	executor := newExecutor()
	for round := 1; round <= 3; round++ {
		_, err := executor.Converge(ctx, "space-1", "logical-1")
		require.NoError(t, err)
		fake.ConfirmAll()
		markSpotSnapshot(t, f, time.Now().Add(time.Duration(round)*time.Second))
	}
	require.Empty(t, fake.Calls(), "持仓未反映期间不归集")
	require.Empty(t, f.orders.specs)

	// 同步追上：持仓 0.002 反映出来，剩下的 0.008 能买。
	f.setSpotBalance("BTC", "0.002")
	result, err := executor.Converge(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "place", result.Action)
	require.Equal(t, "0.008", f.orders.specs[0].Quantity.String())
}

// 组合里有两个普通账户：首选成员 A 在本目标已被拒单，B 能下。试算只看要划转的 A：不为 A 划转，
// 下单照常（换成 B），而不是被整条阻塞。
func TestRejectedFirstMemberIsNotFundedButOtherMemberStillPlaces(t *testing.T) {
	f := liveStandardFixture(t)
	f.setOrderSettings(t, `{"order_mode":"MARKET","min_notional_spot":"10","min_notional_swap":"5"}`)
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.SetLogicalAccountAutomation("space-1", "logical-1", "PAUSED", "加成员"); err != nil {
			return err
		}
		second := store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "account-b", Name: "account-b", Exchange: "BINANCE",
			Markets: []string{"SPOT", "SWAP"}, ExecutionMode: "LIVE", AccountMode: "BINANCE_STANDARD", Environment: "PRODUCTION",
			CredentialSecretID: "secret-2", SettlementAsset: "USDT", MarginMode: "CROSS", Status: "ENABLED", Ready: true,
			LeverageSettings: store.LeverageSettings{"*": "5"},
			Snapshot: store.TradingAccountSnapshot{AvailableFunds: "100000", Balances: []store.AssetBalance{
				{Wallet: "SPOT", Asset: "USDT", Available: "100000", Total: "100000"},
			}},
			LastSyncAt: 1_900, SnapshotSourceTime: 1_900,
		}
		if err := tx.CreateTradingAccount(second); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-b", Enabled: true, Priority: 2,
		}); err != nil {
			return err
		}
		return tx.SetLogicalAccountAutomation("space-1", "logical-1", "ACTIVE", "")
	}))
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SPOT", Quantity: "0.01"}})
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", TradingAccountID: "account-a", OrderID: "rejected-child", ClientOrderID: "rejected-child",
		Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "BTCUSDT", InstrumentID: "BTC-USDT-SPOT",
		OrderType: "MARKET", Side: "BUY", Quantity: "0.01", ReferencePrice: "50000", State: "REJECTED",
		RejectReason: "not permitted", SubmittedAt: 1_900, OwnerType: "TARGET", OwnerID: "target-current",
		LogicalAccountID: "logical-1", RunnerID: "runner-1",
	})
	fakeA := &wallettest.Exchange{Spot: "10", Futures: "100000", Ask: "50000"}
	fakeB := &wallettest.Exchange{Spot: "100000", Futures: "0", Ask: "50000"}
	executor := f.executor()
	executor.Prices = targetPriceStub{price: shared.MustDecimal("50000")}
	executor.Transfers = NewWalletTransfer(&wallet.Service{Store: f.store, Adapters: wallettest.Adapters{"account-a": fakeA, "account-b": fakeB}})

	result, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "place", result.Action, "A 被拒，换成 B 下单")
	require.Equal(t, "account-b", f.orders.specs[0].TradingAccountID)
	require.Empty(t, fakeA.Calls(), "不为下不了单的 A 划转")
}

// 试算不吞目标级的错误：目标恰好在本轮失效（过期）时原样返回，不先划转。
func TestDryRunPassesTargetLevelErrorsThrough(t *testing.T) {
	f, fake, newExecutor := realWalletFixture(t, "50000", "10", "100000", "0.01")
	executor := newExecutor()
	ctx := context.Background()
	flattens, err := executor.runningDelistFlattens(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	members, err := executor.loadMembers(ctx, "space-1", "logical-1", flattens)
	require.NoError(t, err)
	record, err := f.store.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	desired, err := desiredTargets(record)
	require.NoError(t, err)
	logical, err := f.store.GetLogicalAccount(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	settings, err := loadOrderSettings(logical)
	require.NoError(t, err)
	env := executor.newLaneEnv(ctx, "space-1", "logical-1", settings, members, desired)
	action, complete, _, err := nextLaneAction("BTC-USDT-SPOT", desired["BTC-USDT-SPOT"], members)
	require.NoError(t, err)
	require.False(t, complete)

	// 目标的有效期早已结束（有效期必须晚于生效时间，所以整个窗口放到过去）。
	record.BarEndTime, record.EffectiveAt, record.ValidUntil = 1_000, 1_000, 2_000
	_, err = executor.dryRunPlacement(ctx, "space-1", record, action, members, env)
	require.True(t, errors.Is(err, ErrTargetExpired), "目标已过期：原样返回，实际是 %v", err)
	require.Empty(t, fake.Calls())
}
