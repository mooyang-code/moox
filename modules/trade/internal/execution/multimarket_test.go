package execution

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
)

// marketStub 是一个市场的单市场适配器：记录收到的调用，返回带自己市场的结果。
type marketStub struct {
	market exchange.MarketType
	wallet exchange.Wallet
	at     time.Time

	mu     sync.Mutex
	placed []exchange.OrderRequest
	quotes []exchange.SymbolKey
	// 私有流
	subscribeStarted chan struct{}
	subscribeErr     error
	subscribeBlock   bool
}

func (s *marketStub) Exchange() exchange.Exchange { return exchange.ExchangeBinance }
func (s *marketStub) LoadInstruments(context.Context) ([]exchange.Instrument, error) {
	return []exchange.Instrument{{MarketType: s.market, ExchangeSymbol: "BTCUSDT"}}, nil
}
func (s *marketStub) GetQuote(_ context.Context, key exchange.SymbolKey) (MarketQuote, error) {
	s.mu.Lock()
	s.quotes = append(s.quotes, key)
	s.mu.Unlock()
	return MarketQuote{Last: shared.MustDecimal("1")}, nil
}
func (s *marketStub) GetReferencePrice(context.Context, exchange.SymbolKey) (exchange.ReferencePrice, error) {
	return exchange.ReferencePrice{Price: shared.MustDecimal("1")}, nil
}
func (s *marketStub) GetOrderByExchangeID(_ context.Context, key exchange.SymbolKey, id string) (exchange.Order, error) {
	return exchange.Order{Market: s.market, ExchangeSymbol: key.Symbol, ExchangeOrderID: id}, nil
}
func (s *marketStub) GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error) {
	return exchange.AccountSnapshot{
		Balances: []exchange.AssetBalance{{Wallet: s.wallet, Asset: "USDT", Total: shared.MustDecimal("10")}},
		Equity:   shared.MustDecimal("10"), AvailableFunds: shared.MustDecimal("5"), ExchangeUpdatedAt: s.at,
		Present: exchange.AccountSnapshotPresence{Balances: true, Equity: true, AvailableFunds: true, UsedMargin: true, MaintenanceMargin: true, UnrealizedPnL: true},
	}, nil
}
func (s *marketStub) ListPositionSnapshots(context.Context) ([]exchange.Position, error) {
	return []exchange.Position{{Market: s.market, ExchangeSymbol: "BTCUSDT"}}, nil
}
func (s *marketStub) ListOpenOrders(context.Context) ([]exchange.Order, error) {
	return []exchange.Order{{Market: s.market, ExchangeSymbol: "BTCUSDT"}}, nil
}
func (s *marketStub) ListRecentFills(_ context.Context, key exchange.SymbolKey, _ string) ([]exchange.Fill, string, error) {
	return []exchange.Fill{{Market: s.market, ExchangeSymbol: key.Symbol}}, "", nil
}
func (s *marketStub) GetOrder(_ context.Context, key exchange.SymbolKey, id string) (exchange.Order, error) {
	return exchange.Order{Market: s.market, ClientOrderID: id}, nil
}
func (s *marketStub) PlaceOrder(_ context.Context, request exchange.OrderRequest) (exchange.Order, error) {
	s.mu.Lock()
	s.placed = append(s.placed, request)
	s.mu.Unlock()
	return exchange.Order{Market: s.market, ClientOrderID: request.ClientOrderID}, nil
}
func (s *marketStub) CancelOrder(_ context.Context, _ exchange.SymbolKey, id string) (exchange.Order, error) {
	return exchange.Order{Market: s.market, ClientOrderID: id}, nil
}
func (s *marketStub) Subscribe(ctx context.Context, handler AccountEventHandler) error {
	handler.OnSubscribed()
	if s.subscribeStarted != nil {
		close(s.subscribeStarted)
	}
	if s.subscribeErr != nil {
		return s.subscribeErr
	}
	if s.subscribeBlock {
		<-ctx.Done()
	}
	return nil
}

func newTwoMarket(t *testing.T) (*MultiMarket, *marketStub, *marketStub) {
	t.Helper()
	at := time.UnixMilli(1_000)
	spot := &marketStub{market: exchange.MarketTypeSpot, wallet: exchange.WalletSpot, at: at.Add(time.Second)}
	swap := &marketStub{market: exchange.MarketTypeSwap, wallet: exchange.WalletFutures, at: at}
	router, err := NewMultiMarket(exchange.ExchangeBinance, []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap},
		map[exchange.MarketType]ExecutionAdapter{exchange.MarketTypeSpot: spot, exchange.MarketTypeSwap: swap})
	require.NoError(t, err)
	return router, spot, swap
}

func TestMultiMarketRoutesByMarketNotBySymbol(t *testing.T) {
	ctx := context.Background()
	router, spot, swap := newTwoMarket(t)
	spotKey := exchange.SymbolKey{Market: exchange.MarketTypeSpot, Symbol: "BTCUSDT"}
	swapKey := exchange.SymbolKey{Market: exchange.MarketTypeSwap, Symbol: "BTCUSDT"}

	// 同名代码、同一个交易所订单号，按市场各查各的。
	for _, key := range []exchange.SymbolKey{spotKey, swapKey} {
		order, err := router.GetOrderByExchangeID(ctx, key, "100")
		require.NoError(t, err)
		require.Equal(t, key.Market, order.Market)
		fills, _, err := router.ListRecentFills(ctx, key, "")
		require.NoError(t, err)
		require.Equal(t, key.Market, fills[0].Market)
		_, err = router.GetQuote(ctx, key)
		require.NoError(t, err)
		placed, err := router.PlaceOrder(ctx, exchange.OrderRequest{Market: key.Market, ExchangeSymbol: key.Symbol, ClientOrderID: "c-" + string(key.Market)})
		require.NoError(t, err)
		require.Equal(t, key.Market, placed.Market)
	}
	require.Equal(t, []exchange.SymbolKey{spotKey}, spot.quotes)
	require.Equal(t, []exchange.SymbolKey{swapKey}, swap.quotes)
	require.Len(t, spot.placed, 1)
	require.Len(t, swap.placed, 1)

	_, err := router.GetQuote(ctx, exchange.SymbolKey{Market: exchange.MarketTypeStock, Symbol: "600519"})
	require.True(t, exchange.IsKind(err, exchange.ErrorRejected), "没有开通的市场直接拒绝")
	_, err = router.PlaceOrder(ctx, exchange.OrderRequest{ExchangeSymbol: "BTCUSDT"})
	require.True(t, exchange.IsKind(err, exchange.ErrorRejected), "下单必须写明市场")
}

func TestMultiMarketMergesAccountReads(t *testing.T) {
	ctx := context.Background()
	router, _, _ := newTwoMarket(t)

	instruments, err := router.LoadInstruments(ctx)
	require.NoError(t, err)
	require.Len(t, instruments, 2)
	orders, err := router.ListOpenOrders(ctx)
	require.NoError(t, err)
	require.Len(t, orders, 2)
	positions, err := router.ListPositionSnapshots(ctx)
	require.NoError(t, err)
	require.Len(t, positions, 2)

	snapshot, err := router.GetAccountSnapshot(ctx)
	require.NoError(t, err)
	require.Len(t, snapshot.Balances, 2)
	require.Equal(t, exchange.WalletSpot, snapshot.Balances[0].Wallet)
	require.Equal(t, exchange.WalletFutures, snapshot.Balances[1].Wallet)
	require.Zero(t, snapshot.AvailableFunds.Cmp(shared.MustDecimal("10")))
	// 每个钱包记自己的读取时间；总体时间取最早的，保证“覆盖水位”的判断不会被较新的钱包误导。
	require.Equal(t, time.UnixMilli(2_000), snapshot.WalletUpdatedAt[exchange.WalletSpot])
	require.Equal(t, time.UnixMilli(1_000), snapshot.WalletUpdatedAt[exchange.WalletFutures])
	require.Equal(t, time.UnixMilli(1_000), snapshot.ExchangeUpdatedAt)
	require.True(t, snapshot.Present.Balances && snapshot.Present.Equity)
}

func TestMultiMarketSubscribeReportsSubscribedOnceAndEndsWhenAnyStreamEnds(t *testing.T) {
	router, spot, swap := newTwoMarket(t)
	spot.subscribeBlock = true
	swap.subscribeErr = errors.New("合约流断开")
	handler := &subscribeCounter{}

	err := router.Subscribe(context.Background(), handler)

	require.EqualError(t, err, "合约流断开", "任一路断开都整体结束并上报")
	require.Equal(t, 1, handler.subscribed, "两路都订阅成功后才通知一次")
}

type subscribeCounter struct {
	mu         sync.Mutex
	subscribed int
}

func (h *subscribeCounter) OnSubscribed() {
	h.mu.Lock()
	h.subscribed++
	h.mu.Unlock()
}
func (*subscribeCounter) OnOrder(context.Context, exchange.Order) error       { return nil }
func (*subscribeCounter) OnFill(context.Context, exchange.Fill) error         { return nil }
func (*subscribeCounter) OnPosition(context.Context, exchange.Position) error { return nil }
func (*subscribeCounter) OnAccountSnapshot(context.Context, exchange.AccountSnapshot) error {
	return nil
}

func TestRegistryBindsOneAdapterPerMarketAndWrapsMultiMarket(t *testing.T) {
	registry := NewRegistry()
	var bound []exchange.AccountConfig
	registry.Register(ExchangeDescriptor{Exchange: exchange.ExchangeBinance}, func(config exchange.AccountConfig, _ exchange.Credential) (ExecutionAdapter, error) {
		bound = append(bound, config)
		return &marketStub{market: config.Market()}, nil
	})
	config := exchange.AccountConfig{
		TradingAccountID: "a", Exchange: exchange.ExchangeBinance, ExecutionMode: exchange.ExecutionModeLive,
		Markets: []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap}, SettlementAsset: "USDT",
		MarginMode: exchange.MarginModeCross,
	}

	adapter, err := registry.Bind(config, exchange.Credential{APIKey: "k", APISecret: "s"})

	require.NoError(t, err)
	_, isRouter := adapter.(*MultiMarket)
	require.True(t, isRouter)
	require.Len(t, bound, 2)
	require.Equal(t, []exchange.MarketType{exchange.MarketTypeSpot}, bound[0].Markets)
	require.Equal(t, exchange.MarginModeUnspecified, bound[0].MarginMode, "现货适配器不带保证金模式")
	require.Equal(t, []exchange.MarketType{exchange.MarketTypeSwap}, bound[1].Markets)
	require.Equal(t, exchange.MarginModeCross, bound[1].MarginMode)

	for name, invalid := range map[string]exchange.AccountConfig{
		"没有市场":       {TradingAccountID: "a", Exchange: exchange.ExchangeBinance, ExecutionMode: exchange.ExecutionModeLive, SettlementAsset: "USDT"},
		"顺序不规范":      {TradingAccountID: "a", Exchange: exchange.ExchangeBinance, ExecutionMode: exchange.ExecutionModeLive, SettlementAsset: "USDT", Markets: []exchange.MarketType{exchange.MarketTypeSwap, exchange.MarketTypeSpot}, MarginMode: exchange.MarginModeCross},
		"含合约必须全仓":    {TradingAccountID: "a", Exchange: exchange.ExchangeBinance, ExecutionMode: exchange.ExecutionModeLive, SettlementAsset: "USDT", Markets: []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap}},
		"没有合约不能设保证金": {TradingAccountID: "a", Exchange: exchange.ExchangeBinance, ExecutionMode: exchange.ExecutionModeLive, SettlementAsset: "USDT", Markets: []exchange.MarketType{exchange.MarketTypeSpot}, MarginMode: exchange.MarginModeCross},
	} {
		_, err := registry.Bind(invalid, exchange.Credential{APIKey: "k", APISecret: "s"})
		require.Error(t, err, name)
	}
}
