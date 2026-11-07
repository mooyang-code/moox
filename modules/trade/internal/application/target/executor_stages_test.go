package target

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newMixedExecutorFixture 建一个现货、合约都开通的模拟账户：现货钱包里有 10 个 ETH。
func newMixedExecutorFixture(t *testing.T) *targetFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trade.db")
	tradeStore, err := store.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tradeStore.Close()) })
	fixture := &targetFixture{
		t: t, path: path, store: tradeStore, orders: &targetOrderServiceStub{},
		market: exchange.MarketTypeSwap, now: time.UnixMilli(2_000).UTC(),
	}
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "account-a", Name: "account-a", Exchange: "BINANCE",
			Markets: []string{"SPOT", "SWAP"}, ExecutionMode: "PAPER", Environment: "PAPER", SettlementAsset: "USDT",
			MarginMode: "CROSS", Status: "ENABLED", Ready: true,
			LeverageSettings: store.LeverageSettings{"*": "5"},
			Snapshot: store.TradingAccountSnapshot{
				AvailableFunds: "100000",
				Balances: []store.AssetBalance{
					{Wallet: "UNIFIED", Asset: "USDT", Available: "100000", Total: "100000"},
					{Wallet: "UNIFIED", Asset: "ETH", Available: "10", Total: "10"},
				},
			},
			LastSyncAt: 1_900, SnapshotSourceTime: 1_900,
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical",
			OwnerInstanceID: "runner-1", OwnerSessionID: "session-1", ExecutionMode: "PAPER",
			SettlementAsset: "USDT", OrderSettingsJSON: legacyOrderSettings, AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-a", Enabled: true, Priority: 1,
		}); err != nil {
			return err
		}
		for _, item := range []struct{ market, base string }{{"SPOT", "BTC"}, {"SWAP", "BTC"}, {"SPOT", "ETH"}} {
			record := store.InstrumentRecord{
				Exchange: "BINANCE", MarketType: item.market, ExchangeSymbol: item.base + "USDT",
				InstrumentID: item.base + "-USDT-" + item.market, BaseAsset: item.base, QuoteAsset: "USDT", SettlementAsset: "USDT",
				ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
			}
			if item.market == "SWAP" {
				record.Linear, record.ContractValue, record.ContractValueAsset = true, "1", item.base
			}
			if err := tx.UpsertInstrument(record); err != nil {
				return err
			}
		}
		return tx.SetLogicalAccountAutomation("space-1", "logical-1", "ACTIVE", "")
	}))
	return fixture
}

func (f *targetFixture) setSpotBalance(asset, total string) {
	f.t.Helper()
	account, err := f.store.GetTradingAccountByID(context.Background(), "account-a")
	require.NoError(f.t, err)
	balances := account.Snapshot.Balances[:0]
	for _, balance := range account.Snapshot.Balances {
		if balance.Asset != asset {
			balances = append(balances, balance)
		}
	}
	balances = append(balances, store.AssetBalance{Wallet: string(account.WalletFor("SPOT")), Asset: asset, Available: total, Total: total})
	account.Snapshot.Balances = balances
	require.NoError(f.t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpdateTradingAccountSync("space-1", "account-a", store.TradingAccountSyncState{
			Ready: true, Snapshot: account.Snapshot, SnapshotSourceTime: 1_900, LastSyncAt: 1_900,
		})
	}))
}

func (f *targetFixture) setSwapPosition(symbol, quantity string) {
	f.t.Helper()
	require.NoError(f.t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpsertPosition(store.PositionRecord{
			SpaceID: "space-1", TradingAccountID: "account-a", MarketType: "SWAP",
			ExchangeSymbol: symbol, PositionSide: "NET", SignedQuantity: quantity,
			EntryPrice: "100", MarkPrice: "100", Leverage: "5", MarginMode: "CROSS", ExchangeUpdatedAt: 1_900,
		})
	}))
}

func placedInstruments(f *targetFixture) []string {
	var ids []string
	for _, spec := range f.orders.specs {
		ids = append(ids, spec.InstrumentID+":"+string(spec.Side))
	}
	return ids
}

// 阶段顺序：先减仓（现货卖出），再合约加仓，最后现货买入；每个阶段做完才进下一个，中间阶段保持 CONVERGING，
// 最后阶段处理完才定为 CONVERGED。阶段不存储，每轮从当前持仓推出。
func TestStagesRunReduceThenSwapAddThenSpotBuy(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.target(t, []store.InstrumentTarget{
		{InstrumentID: "ETH-USDT-SPOT", Quantity: "0"},
		{InstrumentID: "BTC-USDT-SWAP", Quantity: "5"},
		{InstrumentID: "BTC-USDT-SPOT", Quantity: "3"},
	})
	f.now = time.Now().UTC()
	executor := func() *Executor { e := f.executor(); e.MinBatchInterval = 0; return e }

	// 阶段 2：只卖出现货 ETH，合约加仓和现货买入都等着。
	result, err := executor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusConverging, result.Status)
	require.Equal(t, []string{"ETH-USDT-SPOT:SELL"}, placedInstruments(f))

	// 卖出成交后：阶段 3 合约加仓，现货买入仍等着。
	f.orders.specs = nil
	f.setSpotBalance("ETH", "0")
	result, err = executor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusConverging, result.Status)
	require.Equal(t, []string{"BTC-USDT-SWAP:BUY"}, placedInstruments(f))

	// 合约加仓成交后：阶段 5 现货买入。
	f.orders.specs = nil
	f.setSwapPosition("BTCUSDT", "5")
	result, err = executor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusConverging, result.Status)
	require.Equal(t, []string{"BTC-USDT-SPOT:BUY"}, placedInstruments(f))

	// 全部到位：最后阶段处理完才定终态。
	f.orders.specs = nil
	f.setSpotBalance("BTC", "3")
	result, err = executor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusConverged, result.Status)
	require.Empty(t, f.orders.specs)
}

// 前一阶段的标的被阻塞（下不了单）时不卡住后面的阶段：本轮直接进入下一阶段，最后按现有规则定为 BLOCKED。
func TestBlockedReduceDoesNotStallLaterStages(t *testing.T) {
	f := newMixedExecutorFixture(t)
	// ETH 的现货卖出金额低于最小名义金额，下不了单；合约加仓照常下。
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_trade_instruments SET c_min_notional = '100000' WHERE c_instrument_id = 'ETH-USDT-SPOT'").Error)
	f.target(t, []store.InstrumentTarget{
		{InstrumentID: "ETH-USDT-SPOT", Quantity: "0"},
		{InstrumentID: "BTC-USDT-SWAP", Quantity: "5"},
	})
	f.now = time.Now().UTC()

	result, err := f.executor().Converge(context.Background(), "space-1", "logical-1")

	require.NoError(t, err)
	require.Equal(t, StatusConverging, result.Status)
	require.Equal(t, []string{"BTC-USDT-SWAP:BUY"}, placedInstruments(f), "ETH 阻塞不影响后面的合约加仓")
}

// 本目标只剩簿上的挂单：定为 CONVERGED，唤醒时间是距有效期末的剩余时间，不再每秒重算。
func TestOnlyRestingOrdersConvergeWithRetryAfterValidUntil(t *testing.T) {
	f := newMixedExecutorFixture(t)
	// 持仓 10、目标 11：差额 1，簿上的挂单买 1 正好覆盖。
	f.target(t, []store.InstrumentTarget{{InstrumentID: "ETH-USDT-SPOT", Quantity: "11"}})
	f.now = time.Now().UTC()
	target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "resting-1", TradingAccountID: "account-a", LogicalAccountID: "logical-1", RunnerID: target.RunnerID,
		ClientOrderID: "resting-1", Exchange: "BINANCE", MarketType: "SPOT", InstrumentID: "ETH-USDT-SPOT", ExchangeSymbol: "ETHUSDT",
		OrderType: "LIMIT", TimeInForce: "GTC", Side: "BUY", Quantity: "1", LimitPrice: strPtr("90"), ReferencePrice: "100",
		OwnerType: "TARGET", OwnerID: target.TargetID, State: "OPEN", Version: 1,
		ReservedAsset: "USDT", ReservedQuantity: "90", RemainingReservedQuantity: "90",
	})

	result, err := f.executor().Converge(context.Background(), "space-1", "logical-1")

	require.NoError(t, err)
	require.Equal(t, StatusConverged, result.Status)
	require.Greater(t, result.RetryAfter, 30*time.Minute, "唤醒时间取到有效期末")
	require.LessOrEqual(t, result.RetryAfter, time.Until(time.UnixMilli(target.ValidUntil))+time.Second)
	require.Empty(t, f.orders.specs, "挂单还在，不再追加新单")
}

// 目标过期时撤掉本目标簿上的挂单；上一份目标留下的挂单在新目标的阶段 1 撤掉。
func TestExpiryAndNewTargetCancelRestingOrders(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "ETH-USDT-SPOT", Quantity: "10"}})
	target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	record := store.OrderRecord{
		SpaceID: "space-1", OrderID: "resting-1", TradingAccountID: "account-a", LogicalAccountID: "logical-1", RunnerID: target.RunnerID,
		ClientOrderID: "resting-1", Exchange: "BINANCE", MarketType: "SPOT", InstrumentID: "ETH-USDT-SPOT", ExchangeSymbol: "ETHUSDT",
		OrderType: "LIMIT", TimeInForce: "GTC", Side: "BUY", Quantity: "1", LimitPrice: strPtr("90"), ReferencePrice: "100",
		OwnerType: "TARGET", OwnerID: target.TargetID, State: string(orderdomain.Open), Version: 1,
	}
	f.order(t, record)

	// 到有效期末：过期分支撤掉挂单。
	f.now = time.UnixMilli(target.ValidUntil).Add(time.Second).UTC()
	result, err := f.executor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusExpired, result.Status)
	require.Equal(t, []string{"resting-1"}, f.orders.canceled)
}

func strPtr(value string) *string { return &value }

type fakeTransfer struct {
	beforePending, collectPending bool
	// waitAccounts 只让这些账户等划转（beforePending 为真时所有账户都等）。
	waitAccounts              map[string]bool
	beforeCalls, collectCalls int
	buys                      []TransferBuy
	scale                     map[string]shared.Decimal
	collected                 []string
}

func (f *fakeTransfer) BeforeSpotBuy(_ context.Context, _ string, _ store.LogicalAccountTargetRecord, buys []TransferBuy) (TransferOutcome, error) {
	f.beforeCalls++
	f.buys = buys
	outcome := TransferOutcome{BuyScale: f.scale}
	outcome.Waiting = map[string]string{}
	for _, buy := range buys {
		if f.beforePending || f.waitAccounts[buy.Account.TradingAccountID] {
			outcome.Waiting[buy.Account.TradingAccountID] = ""
		}
	}
	return outcome, nil
}

func (f *fakeTransfer) Collect(_ context.Context, _ string, _ store.LogicalAccountTargetRecord, accounts []store.TradingAccountRecord) (bool, error) {
	f.collectCalls++
	for _, account := range accounts {
		f.collected = append(f.collected, account.TradingAccountID)
	}
	return f.collectPending, nil
}

// 普通账户：现货买入前先划转（阶段 4），划转没完成就不买；买完后归集（阶段 6），归集没完成不定终态。
func TestStandardAccountTransfersBeforeSpotBuyAndCollectsAfter(t *testing.T) {
	f := newMixedExecutorFixture(t)
	require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_trading_accounts SET c_execution_mode = 'LIVE', c_live_environment = 'PRODUCTION',
		c_credential_secret_id = 'secret-1', c_account_mode = 'BINANCE_STANDARD'`).Error)
	f.setSpotBalance("ETH", "0")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SPOT", Quantity: "3"}})
	f.now = time.Now().UTC()
	transfer := &fakeTransfer{beforePending: true}
	executor := f.executor()
	executor.Transfers = transfer

	result, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusConverging, result.Status)
	require.Equal(t, "transfer", result.Action)
	require.Positive(t, result.RetryAfter)
	require.Empty(t, f.orders.specs, "划转没完成不买入")
	require.Len(t, transfer.buys, 1)
	require.Equal(t, "BTC-USDT-SPOT", transfer.buys[0].Instrument.InstrumentID)

	transfer.beforePending = false
	result, err = executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "place", result.Action)
	require.Equal(t, []string{"BTC-USDT-SPOT:BUY"}, placedInstruments(f))

	f.orders.specs = nil
	f.setSpotBalance("BTC", "3")
	transfer.collectPending = true
	result, err = executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusConverging, result.Status)
	require.Equal(t, "collect", result.Action, "归集没完成不定终态")

	transfer.collectPending = false
	result, err = executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusConverged, result.Status)
}

// 模拟盘（和统一账户、OKX）没有划转：只走阶段 1、2、3、5，不会调用划转。
func TestPaperAccountSkipsTransferStages(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.setSpotBalance("ETH", "0")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SPOT", Quantity: "3"}})
	f.now = time.Now().UTC()
	transfer := &fakeTransfer{beforePending: true, collectPending: true}
	executor := f.executor()
	executor.Transfers = transfer

	result, err := executor.Converge(context.Background(), "space-1", "logical-1")

	require.NoError(t, err)
	require.Equal(t, "place", result.Action)
	require.Zero(t, transfer.beforeCalls)
	f.setSpotBalance("BTC", "3")
	result, err = executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusConverged, result.Status)
	require.Zero(t, transfer.collectCalls)
}

// 上一份目标留下的挂单在新目标的阶段 1 撤掉，不带进新目标的差额。
func TestNewTargetCancelsPreviousTargetRestingOrder(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "ETH-USDT-SPOT", Quantity: "10"}})
	f.now = time.Now().UTC()
	target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "old-resting", TradingAccountID: "account-a", LogicalAccountID: "logical-1", RunnerID: target.RunnerID,
		ClientOrderID: "old-resting", Exchange: "BINANCE", MarketType: "SPOT", InstrumentID: "ETH-USDT-SPOT", ExchangeSymbol: "ETHUSDT",
		OrderType: "LIMIT", TimeInForce: "GTC", Side: "BUY", Quantity: "1", LimitPrice: strPtr("90"), ReferencePrice: "100",
		OwnerType: "TARGET", OwnerID: "previous-target", State: "OPEN", Version: 1,
	})

	result, err := f.executor().Converge(context.Background(), "space-1", "logical-1")

	require.NoError(t, err)
	require.Equal(t, "stop", result.Action)
	require.Equal(t, []string{"old-resting"}, f.orders.canceled)
}

// 套利底仓的对冲腿（永续）不参与策略持仓：不在目标里也不会被当作“按 0 平掉”。
func TestArbitrageBaseSwapLegIsNotFlattenedToZero(t *testing.T) {
	f := newMixedExecutorFixture(t)
	require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_logical_accounts SET c_fund_settings_json = '{"arbitrage_base_assets":["BTC"]}'`).Error)
	f.setSwapPosition("BTCUSDT", "-5")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "ETH-USDT-SPOT", Quantity: "10"}})
	f.now = time.Now().UTC()

	result, err := f.executor().Converge(context.Background(), "space-1", "logical-1")

	require.NoError(t, err)
	require.Equal(t, StatusConverged, result.Status)
	require.Empty(t, f.orders.specs, "对冲腿不动")
}

func restingEthOrder(target store.LogicalAccountTargetRecord) store.OrderRecord {
	return store.OrderRecord{
		SpaceID: "space-1", OrderID: "resting-eth", TradingAccountID: "account-a", LogicalAccountID: "logical-1", RunnerID: target.RunnerID,
		ClientOrderID: "resting-eth", Exchange: "BINANCE", MarketType: "SPOT", InstrumentID: "ETH-USDT-SPOT", ExchangeSymbol: "ETHUSDT",
		OrderType: "LIMIT", TimeInForce: "GTC", Side: "BUY", Quantity: "2", LimitPrice: strPtr("90"), ReferencePrice: "100",
		OwnerType: "TARGET", OwnerID: target.TargetID, State: "OPEN", Version: 1,
	}
}

// ETH 挂单还没成交时，不阻塞其他标的：BTC 合约照常加仓，ETH 不再追加新单。
func TestRestingOrderDoesNotBlockOtherInstruments(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "ETH-USDT-SPOT", Quantity: "12"}, {InstrumentID: "BTC-USDT-SWAP", Quantity: "5"}})
	f.now = time.Now().UTC()
	target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	f.order(t, restingEthOrder(target))

	result, err := f.executor().Converge(context.Background(), "space-1", "logical-1")

	require.NoError(t, err)
	require.Equal(t, "place", result.Action)
	require.Equal(t, []string{"BTC-USDT-SWAP:BUY"}, placedInstruments(f))
}

// BTC 到位后只剩 ETH 挂单：定为已收敛并设唤醒时间（到有效期末），不再每秒重算。
func TestOnlyRestingLaneLeftConvergesWithWakeUp(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "ETH-USDT-SPOT", Quantity: "12"}, {InstrumentID: "BTC-USDT-SWAP", Quantity: "5"}})
	f.now = time.Now().UTC()
	f.setSwapPosition("BTCUSDT", "5")
	target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	f.order(t, restingEthOrder(target))

	result, err := f.executor().Converge(context.Background(), "space-1", "logical-1")

	require.NoError(t, err)
	require.Equal(t, StatusConverged, result.Status)
	require.Greater(t, result.RetryAfter, 30*time.Minute)
	require.Empty(t, f.orders.specs)
}

// 有标的被阻塞又有挂单：定为已阻塞也要设唤醒（定时器不扫已阻塞的目标），到期时撤掉挂单。
func TestBlockedWithRestingOrderWakesUpAndCancelsAtExpiry(t *testing.T) {
	f := newMixedExecutorFixture(t)
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_trade_instruments SET c_min_notional = '100000' WHERE c_instrument_id = 'BTC-USDT-SPOT'").Error)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "ETH-USDT-SPOT", Quantity: "12"}, {InstrumentID: "BTC-USDT-SPOT", Quantity: "3"}})
	f.now = time.Now().UTC()
	target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	f.order(t, restingEthOrder(target))

	result, err := f.executor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusBlocked, result.Status)
	require.Greater(t, result.RetryAfter, 30*time.Minute, "已阻塞的目标定时器不扫，必须设唤醒")

	f.now = time.UnixMilli(target.ValidUntil).Add(time.Second).UTC()
	result, err = f.executor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusExpired, result.Status)
	require.Equal(t, []string{"resting-eth"}, f.orders.canceled)
}

// 合约钱包可划额度不够：划转实现用 BuyScale 把现货买单按比例缩小，不会按全额下单。
func TestTransferScaleShrinksSpotBuy(t *testing.T) {
	f := newMixedExecutorFixture(t)
	require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_trading_accounts SET c_execution_mode = 'LIVE', c_live_environment = 'PRODUCTION',
		c_credential_secret_id = 'secret-1', c_account_mode = 'BINANCE_STANDARD'`).Error)
	f.setSpotBalance("ETH", "0")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SPOT", Quantity: "4"}})
	f.now = time.Now().UTC()
	executor := f.executor()
	executor.Transfers = &fakeTransfer{scale: map[string]shared.Decimal{"account-a": shared.MustDecimal("0.5")}}

	_, err := executor.Converge(context.Background(), "space-1", "logical-1")

	require.NoError(t, err)
	require.Len(t, f.orders.specs, 1)
	require.Equal(t, "2", f.orders.specs[0].Quantity.String(), "额度只够一半，买入量从 4 缩到 2")
}

// 资金设置坏了不能挡住清仓：空目标照常收敛（卖掉现货 ETH）。
func TestBrokenFundSettingsDoNotBlockFlatteningTarget(t *testing.T) {
	f := newMixedExecutorFixture(t)
	require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_logical_accounts SET c_fund_settings_json = '{"reserve_ratio":"oops"}'`).Error)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "ETH-USDT-SPOT", Quantity: "0"}})
	f.now = time.Now().UTC()

	result, err := f.executor().Converge(context.Background(), "space-1", "logical-1")

	require.NoError(t, err)
	require.Equal(t, "place", result.Action)
	require.Equal(t, []string{"ETH-USDT-SPOT:SELL"}, placedInstruments(f))
}

// 到期撤挂单失败：目标保持原状并设重试唤醒（已收敛、已阻塞的目标不会被定时器扫到）。
func TestExpiryCancelFailureSchedulesRetry(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "ETH-USDT-SPOT", Quantity: "12"}})
	target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	f.order(t, restingEthOrder(target))
	f.orders.cancelErr = errors.New("交易所暂时不可用")
	f.now = time.UnixMilli(target.ValidUntil).Add(time.Second).UTC()

	result, err := f.executor().Converge(context.Background(), "space-1", "logical-1")

	require.Error(t, err)
	require.Equal(t, expiryRetryDelay, result.RetryAfter)
	current, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.NotEqual(t, StatusExpired, current.Status, "撤单没成功，目标不能先置为过期")
}

// 资金设置的数值字段坏了而底仓设置还在：执行器对空目标和已接收的目标都不平掉底仓的永续对冲腿。
func TestBrokenNumericSettingStillKeepsArbitrageHedgeLeg(t *testing.T) {
	for name, targets := range map[string][]store.InstrumentTarget{
		"空目标（清仓）": {},
		"非空目标":    {{InstrumentID: "ETH-USDT-SPOT", Quantity: "10"}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newMixedExecutorFixture(t)
			require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_logical_accounts SET c_fund_settings_json = '{"arbitrage_base_assets":["BTC"],"reserve_ratio":"oops"}'`).Error)
			f.setSwapPosition("BTCUSDT", "-5")
			f.target(t, targets)
			f.now = time.Now().UTC()

			_, err := f.executor().Converge(context.Background(), "space-1", "logical-1")

			require.NoError(t, err)
			require.NotContains(t, placedInstruments(f), "BTC-USDT-SWAP:BUY", "对冲腿不能被平掉")
		})
	}
}

// 排除字段坏了：执行器报错、不下单，不能把排除资产当普通资产交易。
func TestUnreadableExclusionSettingStopsExecutorFromTrading(t *testing.T) {
	f := newMixedExecutorFixture(t)
	require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_logical_accounts SET c_fund_settings_json = '{"margin_coins":["ETH"]}'`).Error)
	f.setSwapPosition("BTCUSDT", "-5")
	f.target(t, []store.InstrumentTarget{})
	f.now = time.Now().UTC()

	result, err := f.executor().Converge(context.Background(), "space-1", "logical-1")

	require.NoError(t, err, "设置坏了不是模块级故障，不能让整个 trade 模块显示未就绪")
	require.Equal(t, StatusPaused, result.Status)
	require.Empty(t, f.orders.specs)
	target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Contains(t, target.LastError, "margin_coins", "原因写进这个目标的 LastError")
}

// 到期撤单成功后写“已过期”失败：设重试唤醒，已收敛、已阻塞的目标不被定时扫描，不设会一直卡住。
func TestExpiryStatusWriteFailureSchedulesRetry(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "ETH-USDT-SPOT", Quantity: "12"}})
	target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	f.order(t, restingEthOrder(target))
	f.now = time.UnixMilli(target.ValidUntil).Add(time.Second).UTC()
	callback := "test:expiry_status_write_failure"
	require.NoError(t, f.store.DBForTest().Callback().Raw().Before("gorm:raw").Register(callback, func(db *gorm.DB) {
		if strings.Contains(db.Statement.SQL.String(), "t_logical_account_targets") && strings.HasPrefix(strings.TrimSpace(db.Statement.SQL.String()), "UPDATE") {
			db.AddError(errors.New("状态写入失败"))
		}
	}))
	defer f.store.DBForTest().Callback().Raw().Remove(callback)

	result, err := f.executor().Converge(context.Background(), "space-1", "logical-1")

	require.Error(t, err)
	require.Equal(t, []string{"resting-eth"}, f.orders.canceled, "挂单已经撤掉")
	require.Equal(t, expiryRetryDelay, result.RetryAfter)
}
