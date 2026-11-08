package target

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/wallet"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// liveStandardFixture 是账户 account-a 为币安普通账户（现货、合约分钱包）的混合市场组合。
func liveStandardFixture(t *testing.T) *targetFixture {
	t.Helper()
	f := newMixedExecutorFixture(t)
	require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_trading_accounts SET c_execution_mode = 'LIVE', c_live_environment = 'PRODUCTION',
		c_credential_secret_id = 'secret-1', c_account_mode = 'BINANCE_STANDARD'`).Error)
	require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_logical_accounts SET c_execution_mode = 'LIVE'`).Error)
	f.setSpotBalance("ETH", "0")
	f.now = time.Now().UTC()
	return f
}

// addOKXSpotMember 给组合加一个 OKX 统一账户（只有它能买 SOL-USDT-SPOT），用来验证账户之间互不影响。
func addOKXSpotMember(t *testing.T, f *targetFixture) {
	t.Helper()
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.SetLogicalAccountAutomation("space-1", "logical-1", "PAUSED", "加成员"); err != nil {
			return err
		}
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "account-okx", Name: "okx", Exchange: "OKX",
			Markets: []string{"SPOT"}, ExecutionMode: "LIVE", AccountMode: "OKX_UNIFIED", Environment: "PRODUCTION",
			CredentialSecretID: "secret-2", SettlementAsset: "USDT", Status: "ENABLED", Ready: true,
			LeverageSettings: store.LeverageSettings{"*": "1"},
			Snapshot: store.TradingAccountSnapshot{AvailableFunds: "100000", Balances: []store.AssetBalance{
				{Wallet: "UNIFIED", Asset: "USDT", Available: "100000", Total: "100000"},
			}},
			LastSyncAt: 1_900, SnapshotSourceTime: 1_900,
		}); err != nil {
			return err
		}
		if err := tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "OKX", MarketType: "SPOT", ExchangeSymbol: "SOL-USDT", InstrumentID: "SOL-USDT-SPOT",
			BaseAsset: "SOL", QuoteAsset: "USDT", SettlementAsset: "USDT",
			ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-okx", Enabled: true, Priority: 2,
		}); err != nil {
			return err
		}
		return tx.SetLogicalAccountAutomation("space-1", "logical-1", "ACTIVE", "")
	}))
}

// 一个账户在等划转时，同组合其他账户（OKX、统一账户没有划转）的现货买单照常下；等的账户这轮不买，也不算阻塞。
// 只要还有账户在等，目标就保持收敛中，不归集、不定终态。
func TestWaitingAccountOnlyBlocksItsOwnSpotBuys(t *testing.T) {
	f := liveStandardFixture(t)
	addOKXSpotMember(t, f)
	f.target(t, []store.InstrumentTarget{
		{InstrumentID: "BTC-USDT-SPOT", Quantity: "3"},
		{InstrumentID: "SOL-USDT-SPOT", Quantity: "5"},
	})
	transfer := &fakeTransfer{waitAccounts: map[string]bool{"account-a": true}}
	executor := f.executor()
	executor.Transfers = transfer

	result, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "place", result.Action)
	require.Equal(t, []string{"SOL-USDT-SPOT:BUY"}, placedInstruments(f), "OKX 账户的买单不受影响")
	require.Len(t, transfer.buys, 1, "OKX 账户没有划转，不交给钱包")
	require.Equal(t, "account-a", transfer.buys[0].Account.TradingAccountID)

	// OKX 账户的买单成交后，只剩 account-a 在等：目标保持收敛中，不归集。
	f.orders.specs = nil
	f.setSpotBalanceOf(t, "account-okx", "SOL", "5")
	result, err = executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusConverging, result.Status)
	require.Equal(t, "transfer", result.Action)
	require.Empty(t, f.orders.specs)
	require.Zero(t, transfer.collectCalls, "有账户在等划转，不归集")
	stored, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Empty(t, stored.BlockedTargets, "等划转不是阻塞")
}

// 因为被拒单、数量太小等非资金原因被挡住的买单不阻止归集：这些买单用不上资金，资金一直留在现货钱包没有道理。
func TestCollectNotSkippedWhileSpotBuyIsBlockedForNonFundsReason(t *testing.T) {
	f := liveStandardFixture(t)
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_trade_instruments SET c_min_notional = '100000' WHERE c_instrument_id = 'BTC-USDT-SPOT'").Error)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SPOT", Quantity: "3"}})
	transfer := &fakeTransfer{}
	executor := f.executor()
	executor.Transfers = transfer

	result, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusBlocked, result.Status)
	require.Empty(t, f.orders.specs)
	require.Equal(t, []string{"account-a"}, transfer.collected, "非资金原因的阻塞不拦归集")
}

// 持有同向仓位（0.005 BTC，目标 0.01）、资金缺口把买单缩得下不了单：记 BLOCKED 和原始差额，不再因为“已有同向持仓”静默跳过；
// 同时账户不归集（资金还要等）。
func TestScaledBuyTooSmallWithExistingPositionIsBlockedNotSkipped(t *testing.T) {
	f := liveStandardFixture(t)
	f.setSpotBalance("BTC", "0.005")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SPOT", Quantity: "0.01"}})
	transfer := &fakeTransfer{
		scale:   map[string]shared.Decimal{"account-a": mustDecimal("0.01")},
		reasons: map[string]string{"account-a": wallet.ReasonShortfallScaled},
	}
	executor := f.executor()
	executor.Transfers = transfer

	result, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusBlocked, result.Status, "缺钱：真实的资金缺口，不是已满足")
	require.Empty(t, f.orders.specs)
	stored, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Len(t, stored.BlockedTargets, 1)
	require.Equal(t, "0.005", stored.BlockedTargets[0].Quantity)
	require.True(t, wallet.IsFundsShortage(stored.BlockedTargets[0].Reason))
	require.Zero(t, transfer.collectCalls, "买单在等资金，不归集")
}

// 差额（8U）低于设置的最小下单额（10U）：本来就视为已满足。缺钱时也一样不阻塞；
// 不为这笔不会下的单向钱包要资金（买单不交给划转）。
func TestBelowConfiguredMinimumBuyIsSatisfiedAndNeverTransfersOrBlocks(t *testing.T) {
	f := liveStandardFixture(t)
	f.setOrderSettings(t, `{"order_mode":"MARKET","min_notional_spot":"10","min_notional_swap":"0"}`)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SPOT", Quantity: "0.08"}})
	transfer := &fakeTransfer{
		scale:   map[string]shared.Decimal{"account-a": shared.Zero()},
		reasons: map[string]string{"account-a": wallet.ReasonShortfallNoBuy},
	}
	executor := f.executor()
	executor.Transfers = transfer

	result, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusConverged, result.Status, "缺钱也不判 BLOCKED：差额本来就不会下单")
	require.Empty(t, f.orders.specs)
	require.Empty(t, transfer.buys, "不会下的买单不计入划转需求")
	stored, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Empty(t, stored.BlockedTargets)
}

// 每小时上限把现货买单顺延到下一个整点：整点之前不归集，顺延的买单到整点还要用这笔资金。
func TestCollectSkippedWhileSpotBuyIsDeferredByHourlyCap(t *testing.T) {
	f := liveStandardFixture(t)
	f.setOrderSettings(t, `{"order_mode":"MARKET","min_notional_spot":"0","min_notional_swap":"0","hourly_cap_spot":{"BTC-USDT-SPOT":"1000"}}`)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SPOT", Quantity: "30"}})
	at := f.now.Truncate(time.Hour).Add(time.Second).UnixMilli()
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "used", TradingAccountID: "account-a", LogicalAccountID: "logical-1", RunnerID: "runner-1",
		ClientOrderID: "used", Exchange: "BINANCE", MarketType: "SPOT", InstrumentID: "BTC-USDT-SPOT", ExchangeSymbol: "BTCUSDT",
		OrderType: "MARKET", Side: "BUY", Quantity: "10", ReferencePrice: "100", AveragePrice: "100", FilledQuantity: "10",
		OwnerType: "TARGET", OwnerID: "target-current", State: "FILLED", SubmittedAt: at, FinishedAt: at, Version: 1,
	})
	transfer := &fakeTransfer{}
	executor := f.executor()
	executor.Transfers = transfer

	result, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusConverged, result.Status, "全是顺延：已收敛，到整点自己唤醒")
	require.Empty(t, f.orders.specs)
	require.Zero(t, transfer.collectCalls, "顺延的买单到整点还要用这笔资金，不归集")
	stored, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Len(t, stored.BlockedTargets, 1)
	require.True(t, stored.BlockedTargets[0].Deferred)
}

// listedBlocked 同一个标的只列一条：已经有真实阻塞的标的不再列它的顺延。
func TestListedBlockedKeepsOneEntryPerInstrument(t *testing.T) {
	blocked := []store.BlockedTarget{{InstrumentID: "BTC-USDT-SPOT", Quantity: "1", Reason: "真实阻塞"}}
	deferred := []store.BlockedTarget{
		{InstrumentID: "BTC-USDT-SPOT", Quantity: "1", Reason: "顺延", Deferred: true},
		{InstrumentID: "ETH-USDT-SPOT", Quantity: "1", Reason: "顺延", Deferred: true},
	}
	listed := listedBlocked(blocked, deferred)
	require.Len(t, listed, 2)
	require.Equal(t, "真实阻塞", listed[0].Reason)
	require.Equal(t, "ETH-USDT-SPOT", listed[1].InstrumentID)
}

// setSpotBalanceOf 设置指定执行账户现货钱包里某个币的数量。
func (f *targetFixture) setSpotBalanceOf(t *testing.T, accountID, asset, total string) {
	t.Helper()
	account, err := f.store.GetTradingAccountByID(context.Background(), accountID)
	require.NoError(t, err)
	balances := account.Snapshot.Balances[:0]
	for _, balance := range account.Snapshot.Balances {
		if balance.Asset != asset {
			balances = append(balances, balance)
		}
	}
	balances = append(balances, store.AssetBalance{Wallet: string(account.WalletFor("SPOT")), Asset: asset, Available: total, Total: total})
	account.Snapshot.Balances = balances
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpdateTradingAccountSync("space-1", accountID, store.TradingAccountSyncState{
			Ready: true, Snapshot: account.Snapshot, SnapshotSourceTime: 1_900, LastSyncAt: 1_900,
		})
	}))
}
