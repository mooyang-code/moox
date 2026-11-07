package target

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
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
			SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "configure",
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
	f.target(t, []store.InstrumentTarget{{InstrumentID: "ETH-USDT-SPOT", Quantity: "10"}})
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
	beforeCalls, collectCalls     int
	buys                          []TransferBuy
}

func (f *fakeTransfer) BeforeSpotBuy(_ context.Context, _ string, _ store.LogicalAccountTargetRecord, buys []TransferBuy) (bool, error) {
	f.beforeCalls++
	f.buys = buys
	return f.beforePending, nil
}

func (f *fakeTransfer) Collect(_ context.Context, _ string, _ store.LogicalAccountTargetRecord, _ []store.TradingAccountRecord) (bool, error) {
	f.collectCalls++
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
