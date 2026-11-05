package test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/eventconsumer"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"github.com/stretchr/testify/require"
)

const (
	fakeExchangeName = exchange.Exchange("FAKE_EXCHANGE")
	fakeInstrument   = "600519.XSHG"
)

// fakeCore 是 A 股假券商：市价单立即成交，当日买入的数量计入 Locked，次日才可卖。
type fakeCore struct {
	*fakeExchange
	snapshots int
}

func newFakeCore() *fakeCore {
	fake := newFakeExchange(exchange.MarketTypeStock)
	fake.instrument = exchange.Instrument{
		Exchange: fakeExchangeName, MarketType: exchange.MarketTypeStock,
		ExchangeSymbol: fakeInstrument, InstrumentID: fakeInstrument,
		BaseAsset: fakeInstrument, QuoteAsset: "CNY", SettlementAsset: "CNY",
		ExchangeQuantityStep: shared.MustDecimal("100"),
		MinExchangeQuantity:  shared.MustDecimal("100"),
		PriceTick:            shared.MustDecimal("0.01"), MinNotional: shared.MustDecimal("0"),
		Status: "TRADING", ExchangeUpdatedAt: testNow,
	}
	fake.reference.Price = shared.MustDecimal("1500")
	fake.account.Balances = []exchange.AssetBalance{{Wallet: exchange.WalletUnified,
		Asset: "CNY", Available: shared.MustDecimal("1000000"), Total: shared.MustDecimal("1000000"),
	}}
	fake.account.Equity = shared.MustDecimal("1000000")
	fake.account.AvailableFunds = shared.MustDecimal("1000000")
	return &fakeCore{fakeExchange: fake}
}

func (b *fakeCore) Exchange() exchange.Exchange { return fakeExchangeName }

func (b *fakeCore) PlaceOrder(ctx context.Context, request exchange.OrderRequest) (exchange.Order, error) {
	if _, err := b.fakeExchange.PlaceOrder(ctx, request); err != nil {
		return exchange.Order{}, err
	}
	b.emitFill(request.ClientOrderID, "trade-"+request.ClientOrderID, request.Quantity.String(), "1500", "0")
	b.mu.Lock()
	defer b.mu.Unlock()
	last := &b.fills[len(b.fills)-1]
	last.FeeAsset, last.SettlementAsset = "CNY", "CNY"
	notional := request.Quantity.Mul(shared.MustDecimal("1500"))
	if request.Side == exchange.SideBuy {
		b.moveBalances(notional.Neg(), request.Quantity, shared.Zero())
	} else {
		b.moveBalances(notional, request.Quantity.Neg(), request.Quantity.Neg())
	}
	return b.orders[request.ClientOrderID], nil
}

// moveBalances 调整现金和持仓；买入的数量计入 Locked，所以当日不可卖。调用方持有 b.mu。
func (b *fakeCore) moveBalances(cash, total, available shared.Decimal) {
	b.touch()
	balances := b.account.Balances
	balances[0].Total = balances[0].Total.Add(cash)
	balances[0].Available = balances[0].Available.Add(cash)
	b.account.AvailableFunds = balances[0].Available
	if len(balances) == 1 {
		b.account.Balances = append(balances, exchange.AssetBalance{Wallet: exchange.WalletUnified,
			Asset: fakeInstrument, Available: available, Locked: total.Sub(available), Total: total,
		})
		return
	}
	balances[1].Total = balances[1].Total.Add(total)
	balances[1].Available = balances[1].Available.Add(available)
	balances[1].Locked = balances[1].Total.Sub(balances[1].Available)
}

// touch 让快照时间晚于成交时间，同步才认为余额已覆盖这笔成交。
func (b *fakeCore) touch() {
	b.snapshots++
	b.account.ExchangeUpdatedAt = testNow.Add(time.Hour + time.Duration(b.snapshots)*time.Second)
}

// settle 模拟次日开盘：昨天买入的数量变为可卖。
func (b *fakeCore) settle() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.touch()
	b.account.Balances[1].Available = b.account.Balances[1].Total
	b.account.Balances[1].Locked = shared.Zero()
}

func (b *fakeCore) requestCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.requests)
}

// fakeAdapter 只暴露必须实现的接口：没有私有流，也没有杠杆配置。
type fakeAdapter struct {
	execution.ExecutionAdapter
}

func (fakeAdapter) Exchange() exchange.Exchange { return fakeExchangeName }

// fakeWeightResolver 把权重按 1000 股换算：0.2 对应 200 股。
type fakeWeightResolver struct{ testTargetWeightResolver }

func (r fakeWeightResolver) Resolve(
	ctx context.Context,
	signalTime int64,
	request *tradeeventpb.LogicalAccountTargetWeightRequested,
	spaceID string,
) (targetapp.WeightConversion, error) {
	conversion, err := r.testTargetWeightResolver.Resolve(ctx, signalTime, request, spaceID)
	for index := range conversion.QuantityTargets {
		target := &conversion.QuantityTargets[index]
		target.Quantity = shared.MustDecimal(target.Quantity).Mul(shared.MustDecimal("1000")).String()
	}
	return conversion, err
}

func seedFakeAccount(t *testing.T, tradeStore *store.Store, instrument exchange.Instrument) {
	t.Helper()
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: testSpace, TradingAccountID: testAccount, Name: "A 股",
			Exchange: string(fakeExchangeName), Markets: []string{string(exchange.MarketTypeStock)},
			ExecutionMode: string(exchange.ExecutionModeLive), Environment: string(exchange.AccountEnvironmentTestnet),
			CredentialSecretID: "fake-exchange-secret", SettlementAsset: "CNY",
			Status: string(exchange.AccountStatusEnabled), Ready: true,
			Snapshot: store.TradingAccountSnapshot{
				Balances: []store.AssetBalance{{Wallet: "UNIFIED", Asset: "CNY", Available: "1000000", Total: "1000000"}},
				Equity:   "1000000", AvailableFunds: "1000000", ExchangeUpdatedAt: testNow.UnixMilli(),
			},
			SnapshotSourceTime: testNow.UnixMilli(), LastSyncAt: testNow.UnixMilli(),
		}); err != nil {
			return err
		}
		if err := tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: string(fakeExchangeName), MarketType: string(exchange.MarketTypeStock),
			Environment:    string(exchange.AccountEnvironmentTestnet),
			ExchangeSymbol: instrument.ExchangeSymbol, InstrumentID: instrument.InstrumentID,
			BaseAsset: instrument.BaseAsset, QuoteAsset: instrument.QuoteAsset,
			SettlementAsset:      instrument.SettlementAsset,
			ContractValue:        "0",
			ExchangeQuantityStep: instrument.ExchangeQuantityStep.String(),
			MinExchangeQuantity:  instrument.MinExchangeQuantity.String(),
			PriceTick:            instrument.PriceTick.String(), MinNotional: "0",
			Status: instrument.Status, ExchangeUpdatedAt: testNow.UnixMilli(),
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: testSpace, LogicalAccountID: testLogicalAccount, Name: "A 股组合",
			OwnerInstanceID: testRunner, OwnerSessionID: "session-e2e",
			ExecutionMode:   string(exchange.ExecutionModeLive),
			SettlementAsset: "CNY", AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: testSpace, LogicalAccountID: testLogicalAccount,
			TradingAccountID: testAccount, Enabled: true, Priority: 1,
		}); err != nil {
			return err
		}
		return tx.SetLogicalAccountAutomation(testSpace, testLogicalAccount, "ACTIVE", "")
	}))
}

// 新增场所只注册适配器和描述信息：核心流程按规则跑通买入、T+1、交易时段和做空拒绝。
func TestFakeExchangeRunsFullTargetLifecycle(t *testing.T) {
	ctx := context.Background()
	core := newFakeCore()
	var open atomic.Bool
	registry := execution.NewRegistry()
	registry.Register(execution.ExchangeDescriptor{
		Exchange:       fakeExchangeName,
		OrderTypes:     []exchange.OrderType{exchange.OrderTypeMarket},
		TradingSession: func(time.Time) bool { return open.Load() },
	}, func(_ exchange.AccountConfig, credential exchange.Credential) (execution.ExecutionAdapter, error) {
		if credential.Extra["gateway"] == "" {
			return nil, fmt.Errorf("缺少券商网关地址")
		}
		return fakeAdapter{ExecutionAdapter: core}, nil
	})
	adapter, err := registry.Bind(exchange.AccountConfig{
		TradingAccountID: testAccount, Exchange: fakeExchangeName, MarketType: exchange.MarketTypeStock,
		ExecutionMode: exchange.ExecutionModeLive, SettlementAsset: "CNY",
	}, exchange.Credential{Extra: map[string]string{"gateway": "127.0.0.1:9000"}})
	require.NoError(t, err)
	_, hasStream := adapter.(execution.AccountEventSource)
	_, hasConfigurer := adapter.(execution.SymbolConfigurer)
	require.False(t, hasStream)
	require.False(t, hasConfigurer)

	path := filepath.Join(t.TempDir(), "trade.db")
	tradeStore, err := store.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tradeStore.Close() })
	seedFakeAccount(t, tradeStore, core.instrument)
	f := buildFixture(tradeStore, path, core.fakeExchange, adapter)

	now := time.Now().UTC()
	core.reference.UpdatedAt = now
	f.orders.Validator.Now = func() time.Time { return now }
	f.orders.Now = func() time.Time { return now }
	f.sync.Now = func() time.Time { return testNow.Add(2 * time.Hour) }
	options := eventconsumer.TargetOptions{
		Store: tradeStore, Now: func() time.Time { return now }, WeightResolver: fakeWeightResolver{},
	}
	executor := &targetapp.Executor{
		Store: tradeStore, Orders: f.orders, Exchanges: registry,
		Prices:           targetapp.ExchangePriceSource{Adapters: adapterSource{adapter: adapter}},
		Now:              func() time.Time { return now },
		MaxChildNotional: shared.MustDecimal("100000000"),
	}
	deliver := func(id string, sequence int64, quantity string) jetstream.HandlerDecision {
		result := eventconsumer.HandleTarget(ctx, targetDeliveryWithTargets(t, now, id, sequence,
			[]*tradeeventpb.InstrumentWeightTarget{{InstrumentId: fakeInstrument, MarketType: "STOCK", TargetWeight: quantity}},
		), options)
		return result.Decision
	}
	converge := func() store.LogicalAccountTargetRecord {
		_, err := executor.Converge(ctx, testSpace, testLogicalAccount)
		require.NoError(t, err)
		_, err = f.sync.SyncAccount(ctx, testAccount)
		require.NoError(t, err)
		current, err := tradeStore.GetLogicalAccountTarget(ctx, testSpace, testLogicalAccount)
		require.NoError(t, err)
		return current
	}

	// 不能做空：负数目标不进入执行。
	require.NotEqual(t, jetstream.ACK, deliver("target-short", 1, "-0.1"))

	// 收盘时目标保持等待，不下单。
	require.Equal(t, jetstream.ACK, deliver("target-buy", 2, "0.25"))
	current := converge()
	require.Equal(t, targetapp.StatusPending, current.Status)
	require.Equal(t, "当前不在交易时段", current.LastError)
	require.Zero(t, core.requestCount())

	// 开盘后买入并收敛，当日持仓按 Total 计算，不会重复买入。
	open.Store(true)
	converge()
	require.Equal(t, 1, core.requestCount())
	require.Equal(t, exchange.SideBuy, core.requests[0].Side)
	require.Equal(t, "200", core.requests[0].Quantity.String())
	// 目标 250 股，不足一手的 50 股算完成。
	current = converge()
	require.Equal(t, targetapp.StatusConverged, current.Status)
	require.Equal(t, 1, core.requestCount())

	// 当日买入的不能卖。
	require.Equal(t, jetstream.ACK, deliver("target-sell", 3, "0"))
	current = converge()
	require.Equal(t, targetapp.StatusBlocked, current.Status)
	require.Len(t, current.BlockedTargets, 1)
	require.Contains(t, current.BlockedTargets[0].Reason, "可卖量不足")
	require.Equal(t, 1, core.requestCount())

	// 次日可以卖出。
	core.settle()
	_, err = f.sync.SyncAccount(ctx, testAccount)
	require.NoError(t, err)
	converge()
	require.Equal(t, 2, core.requestCount())
	require.Equal(t, exchange.SideSell, core.requests[1].Side)
	require.Equal(t, "200", core.requests[1].Quantity.String())
	current = converge()
	require.Equal(t, targetapp.StatusConverged, current.Status)
}
