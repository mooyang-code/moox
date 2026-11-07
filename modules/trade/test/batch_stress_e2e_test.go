package test

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	"github.com/mooyang-code/moox/modules/trade/internal/application/accountsync"
	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/execution/adaptertest"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// stressExchange 是多标的的假永续交易所：市价单立即成交，按比例随机拒单、限流（不受理）和结果未知（实际已受理）。
type stressExchange struct {
	adaptertest.Stub
	mu        sync.Mutex
	rng       *rand.Rand
	symbols   []string
	positions map[string]int64
	maxAbs    map[string]int64
	fills     map[string][]exchange.Fill
	accepted  map[string]exchange.Order
	trade     int
	calls     map[string]int
}

func newStressExchange(symbols []string, seed int64) *stressExchange {
	return &stressExchange{
		rng: rand.New(rand.NewSource(seed)), symbols: symbols,
		positions: map[string]int64{}, maxAbs: map[string]int64{},
		fills: map[string][]exchange.Fill{}, accepted: map[string]exchange.Order{}, calls: map[string]int{},
	}
}

func (*stressExchange) Exchange() exchange.Exchange { return exchange.ExchangeBinance }

func (x *stressExchange) GetReferencePrice(context.Context, exchange.SymbolKey) (exchange.ReferencePrice, error) {
	return exchange.ReferencePrice{Price: shared.MustDecimal("100"), UpdatedAt: testNow}, nil
}

func (x *stressExchange) GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error) {
	x.mu.Lock()
	x.calls["account"]++
	x.mu.Unlock()
	return exchange.AccountSnapshot{
		Balances:       []exchange.AssetBalance{{Wallet: exchange.WalletUnified, Asset: "USDT", Available: shared.MustDecimal("100000000"), Total: shared.MustDecimal("100000000")}},
		Equity:         shared.MustDecimal("100000000"),
		AvailableFunds: shared.MustDecimal("100000000"), ExchangeUpdatedAt: testNow.Add(time.Minute),
		Present: exchange.AccountSnapshotPresence{Balances: true, Equity: true, AvailableFunds: true, UsedMargin: true, UnrealizedPnL: true},
	}, nil
}

func (x *stressExchange) ListPositionSnapshots(context.Context) ([]exchange.Position, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.calls["positions"]++
	positions := make([]exchange.Position, 0, len(x.positions))
	for symbol, quantity := range x.positions {
		if quantity == 0 {
			continue
		}
		positions = append(positions, exchange.Position{Market: exchange.MarketTypeSwap,
			ExchangeSymbol: symbol, PositionSide: exchange.PositionSideNet,
			SignedQuantity: shared.MustDecimal(strconv.FormatInt(quantity, 10)),
			EntryPrice:     shared.MustDecimal("100"), MarkPrice: shared.MustDecimal("100"),
			Leverage: shared.MustDecimal("10"), MarginMode: exchange.MarginModeCross,
			ExchangeUpdatedAt: testNow,
		})
	}
	return positions, nil
}

func (x *stressExchange) ListOpenOrders(context.Context) ([]exchange.Order, error) {
	x.mu.Lock()
	x.calls["open_orders"]++
	x.mu.Unlock()
	return nil, nil
}

func (x *stressExchange) ListRecentFills(_ context.Context, symbol exchange.SymbolKey, cursor string) ([]exchange.Fill, string, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.calls["fills"]++
	all := x.fills[symbol.Symbol]
	start, _ := strconv.Atoi(cursor)
	if start > len(all) {
		start = len(all)
	}
	return append([]exchange.Fill(nil), all[start:]...), strconv.Itoa(len(all)), nil
}

func (x *stressExchange) GetOrder(_ context.Context, _ exchange.SymbolKey, clientOrderID string) (exchange.Order, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.calls["order"]++
	order, ok := x.accepted[clientOrderID]
	if !ok {
		return exchange.Order{}, &exchange.Error{Kind: exchange.ErrorOrderNotFound, Code: "-2013"}
	}
	return order, nil
}

func (x *stressExchange) PlaceOrder(_ context.Context, request exchange.OrderRequest) (exchange.Order, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.calls["place"]++
	roll := x.rng.Intn(100)
	switch {
	case roll < 4:
		return exchange.Order{}, &exchange.Error{Kind: exchange.ErrorRejected, Code: "-2010"}
	case roll < 8:
		return exchange.Order{}, &exchange.Error{Kind: exchange.ErrorOrderRateLimited, Code: "-1015"}
	}
	quantity, _ := strconv.ParseInt(request.Quantity.String(), 10, 64)
	if request.Side == exchange.SideSell {
		quantity = -quantity
	}
	x.positions[request.ExchangeSymbol] += quantity
	if abs := absInt(x.positions[request.ExchangeSymbol]); abs > x.maxAbs[request.ExchangeSymbol] {
		x.maxAbs[request.ExchangeSymbol] = abs
	}
	x.trade++
	exchangeOrderID := fmt.Sprintf("ex-%d", x.trade)
	x.fills[request.ExchangeSymbol] = append(x.fills[request.ExchangeSymbol], exchange.Fill{Market: exchange.MarketTypeSwap,
		ExchangeTradeID: fmt.Sprintf("trade-%d", x.trade), ExchangeOrderID: exchangeOrderID,
		ClientOrderID: request.ClientOrderID, ExchangeSymbol: request.ExchangeSymbol,
		Side: request.Side, PositionSide: exchange.PositionSideNet,
		Quantity: request.Quantity, Price: shared.MustDecimal("100"),
		SettlementAsset: "USDT", FeeAsset: "USDT", TradedAt: testNow,
	})
	order := exchange.Order{Market: exchange.MarketTypeSwap,
		ExchangeOrderID: exchangeOrderID, ClientOrderID: request.ClientOrderID, ExchangeSymbol: request.ExchangeSymbol,
		OrderType: request.OrderType, Side: request.Side, PositionSide: request.PositionSide,
		Quantity: request.Quantity, FilledQuantity: request.Quantity, Status: exchange.OrderStatusFilled,
		CreatedAt: testNow, UpdatedAt: testNow,
	}
	x.accepted[request.ClientOrderID] = order
	if roll < 12 {
		// 结果未知：交易所已受理，但响应丢了。
		return exchange.Order{}, &exchange.Error{Kind: exchange.ErrorTransportUnknown}
	}
	return order, nil
}

func (*stressExchange) CancelOrder(context.Context, exchange.SymbolKey, string) (exchange.Order, error) {
	return exchange.Order{}, nil
}

func absInt(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

type stressSync struct{ service *accountsync.Service }

func (s stressSync) SyncAccount(ctx context.Context, accountID string) error {
	_, err := s.service.SyncAccount(ctx, accountID)
	return err
}

func (s stressSync) SyncAccountLight(ctx context.Context, accountID string) error {
	_, err := s.service.SyncAccountLight(ctx, accountID)
	return err
}

// 多标的批量收敛：随机拒单、限流、结果未知下，最终持仓到位，任何标的都不超过目标（没有重复下单）。
func TestBatchConvergenceUnderRandomFailuresDoesNotDuplicate(t *testing.T) {
	const count = 120
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = tradeStore.Close() })
	symbols := make([]string, 0, count)
	targets := make([]store.InstrumentTarget, 0, count)
	wanted := make(map[string]int64, count)
	for index := 0; index < count; index++ {
		base := fmt.Sprintf("S%03d", index)
		symbols = append(symbols, base+"USDT")
		quantity := int64(3 + index%7)
		if index%3 == 0 {
			quantity = -quantity
		}
		wanted[base+"USDT"] = quantity
		targets = append(targets, store.InstrumentTarget{InstrumentID: base + "-USDT-SWAP", Quantity: strconv.FormatInt(quantity, 10)})
	}
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: testSpace, TradingAccountID: testAccount, Name: "stress",
			Exchange: string(exchange.ExchangeBinance), Markets: []string{string(exchange.MarketTypeSwap)},
			ExecutionMode: string(exchange.ExecutionModeLive), Environment: string(exchange.AccountEnvironmentTestnet),
			CredentialSecretID: "stress-only", SettlementAsset: "USDT", MarginMode: string(exchange.MarginModeCross),
			Status: string(exchange.AccountStatusEnabled), Ready: true,
			LeverageSettings: store.LeverageSettings{store.DefaultLeverageKey: "10"},
			Snapshot: store.TradingAccountSnapshot{
				Balances: []store.AssetBalance{{Wallet: "UNIFIED", Asset: "USDT", Available: "100000000", Total: "100000000"}},
				Equity:   "100000000", AvailableFunds: "100000000", ExchangeUpdatedAt: testNow.UnixMilli(),
			},
			SnapshotSourceTime: testNow.UnixMilli(), LastSyncAt: testNow.UnixMilli(),
		}); err != nil {
			return err
		}
		for _, symbol := range symbols {
			base := symbol[:len(symbol)-4]
			if err := tx.UpsertInstrument(store.InstrumentRecord{
				Exchange: string(exchange.ExchangeBinance), MarketType: string(exchange.MarketTypeSwap),
				Environment: string(exchange.AccountEnvironmentTestnet), ExchangeSymbol: symbol,
				InstrumentID: base + "-USDT-SWAP", BaseAsset: base, QuoteAsset: "USDT", SettlementAsset: "USDT",
				Linear: true, ContractValue: "1", ContractValueAsset: base,
				ExchangeQuantityStep: "1", MinExchangeQuantity: "1", PriceTick: "0.01", MinNotional: "1",
				Status: "TRADING", ExchangeUpdatedAt: testNow.UnixMilli(),
			}); err != nil {
				return err
			}
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: testSpace, LogicalAccountID: testLogicalAccount, Name: "stress logical",
			OwnerInstanceID: testRunner, OwnerSessionID: "session-e2e",
			ExecutionMode: string(exchange.ExecutionModeLive), SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: testSpace, LogicalAccountID: testLogicalAccount, TradingAccountID: testAccount, Enabled: true, Priority: 1,
		}); err != nil {
			return err
		}
		return tx.SetLogicalAccountAutomation(testSpace, testLogicalAccount, "ACTIVE", "")
	}))
	_, accepted, err := tradeStore.AcceptLogicalAccountTarget(context.Background(), store.LogicalAccountTargetRecord{
		SpaceID: testSpace, LogicalAccountID: testLogicalAccount, TargetID: "stress-target",
		InstanceID: testRunner, SessionID: "session-e2e", StrategyID: "stress",
		BarEndTime: testNow.UnixMilli(), EffectiveAt: testNow.UnixMilli(), ValidUntil: testNow.Add(time.Hour).UnixMilli(),
		Targets: targets, Status: targetapp.StatusPending, AcceptedAt: testNow.UnixMilli(),
	})
	require.NoError(t, err)
	require.True(t, accepted)

	market := newStressExchange(symbols, 7)
	source := adapterSource{adapter: market}
	clock := testNow.Add(time.Minute)
	now := func() time.Time { return clock }
	accountService := &accountapp.Service{Store: accountapp.Repository{Store: tradeStore}, SessionState: readySession(true)}
	syncService := &accountsync.Service{
		Store: tradeStore, Adapters: source, SessionState: readySession(true),
		Fills: &consumer.Reducer{Store: tradeStore, Now: now}, Now: now,
	}
	orders := &orderapp.Service{
		Store: tradeStore,
		Validator: orderapp.Validator{
			Accounts: accountService, Instruments: instrumentSource{store: tradeStore},
			Positions: positionSource{store: tradeStore}, Now: now,
			MaxReferenceAge: time.Hour, MaxChildNotional: shared.MustDecimal("1000000"),
			MaxLeverage: shared.MustDecimal("20"), FeeBufferRate: shared.MustDecimal("0.001"),
		},
		Adapters: source, Syncer: stressSync{service: syncService}, Now: now, UnknownLookupWindow: time.Second,
	}
	syncService.Orders = orders
	executor := &targetapp.Executor{
		Store: tradeStore, Orders: orders, Now: now,
		Prices:           targetapp.ExchangePriceSource{Adapters: source},
		MaxChildNotional: shared.MustDecimal("1000000"),
	}
	var _ execution.ExecutionAdapter = market
	converged := false
	for round := 0; round < 400 && !converged; round++ {
		result, convergeErr := executor.Converge(context.Background(), testSpace, testLogicalAccount)
		if convergeErr == nil && result.Status == targetapp.StatusConverged {
			converged = true
			break
		}
		// 模拟私有事件触发的轻量同步和时间推进（让未知窗口到期）。
		_, _ = syncService.SyncAccountLight(context.Background(), testAccount)
		clock = clock.Add(400 * time.Millisecond)
	}
	// 被交易所明确拒过单的标的，本目标内不再向这个账户下单（下一个目标再试），会留在阻塞列表里。
	current, err := tradeStore.GetLogicalAccountTarget(context.Background(), testSpace, testLogicalAccount)
	require.NoError(t, err)
	require.True(t, converged || current.Status == targetapp.StatusBlocked, "收敛没有稳定下来：%s", current.Status)
	rejected := make(map[string]bool)
	for _, blocked := range current.BlockedTargets {
		rejected[blocked.InstrumentID] = true
	}
	market.mu.Lock()
	defer market.mu.Unlock()
	reached := 0
	for symbol, quantity := range wanted {
		instrumentID := symbol[:len(symbol)-4] + "-USDT-SWAP"
		require.LessOrEqual(t, market.maxAbs[symbol], absInt(quantity), "%s 出现重复下单", symbol)
		if market.positions[symbol] != 0 {
			require.Equal(t, quantity > 0, market.positions[symbol] > 0, "%s 方向", symbol)
		}
		if rejected[instrumentID] {
			continue
		}
		require.Equal(t, quantity, market.positions[symbol], "%s 最终持仓", symbol)
		reached++
	}
	require.Greater(t, reached, count*3/4, "大部分标的应收敛到目标")
	t.Logf("收敛 %d 个标的，%d 个因拒单阻塞；下单 %d 次，查单 %d 次，读全部挂单 %d 次", reached, len(rejected), market.calls["place"], market.calls["order"], market.calls["open_orders"])
}
