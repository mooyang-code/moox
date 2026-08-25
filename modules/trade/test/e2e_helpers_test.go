package test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	"github.com/mooyang-code/moox/modules/trade/internal/application/accountsync"
	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

const (
	testSpace   = "space-e2e"
	testAccount = "account-e2e"
	testSymbol  = "BTCUSDT"
)

var testNow = time.Unix(1_700_000_000, 0).UTC()

// fakeExchange models only Exchange facts. It deliberately does not use the
// production order reducer, so reducer assertions do not mirror production.
type fakeExchange struct {
	mu sync.Mutex

	market     exchange.MarketType
	instrument exchange.Instrument
	account    exchange.AccountSnapshot
	reference  exchange.ReferencePrice
	positions  []exchange.Position
	orders     map[string]exchange.Order
	fills      []exchange.Fill

	placeErr       error
	openOrdersErr  error
	positionsErr   error
	accountErr     error
	fillsErr       error
	subscribeErr   error
	placeCalls     int
	lookupCalls    int
	subscribeCalls int
	requests       []exchange.OrderRequest
	leverageCalls  []leverageCall
	marginCalls    []marginCall
}

type leverageCall struct {
	symbol   string
	leverage shared.Decimal
}

type marginCall struct {
	symbol string
	mode   exchange.MarginMode
}

func newFakeExchange(market exchange.MarketType) *fakeExchange {
	instrument := exchange.Instrument{
		Exchange: exchange.ExchangeBinance, MarketType: market,
		Symbol: testSymbol, InstrumentID: "BTC-USDT",
		BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
		ExchangeQuantityStep: shared.MustDecimal("0.001"),
		MinExchangeQuantity:  shared.MustDecimal("0.001"),
		PriceTick:            shared.MustDecimal("0.1"), MinNotional: shared.MustDecimal("1"),
		Status: "TRADING", ExchangeUpdatedAt: testNow,
	}
	if market == exchange.MarketTypeSwap {
		instrument.Linear = true
		instrument.ContractValue = shared.MustDecimal("0.001")
		instrument.ContractValueAsset = "BTC"
		instrument.ExchangeQuantityStep = shared.MustDecimal("1")
		instrument.MinExchangeQuantity = shared.MustDecimal("1")
	}
	return &fakeExchange{
		market: market, instrument: instrument, orders: make(map[string]exchange.Order),
		reference: exchange.ReferencePrice{
			Price: shared.MustDecimal("50000"), UpdatedAt: testNow,
		},
		account: exchange.AccountSnapshot{
			Balances: []exchange.AssetBalance{
				{Asset: "USDT", Available: shared.MustDecimal("100000"), Total: shared.MustDecimal("100000")},
				{Asset: "BTC", Available: shared.MustDecimal("10"), Total: shared.MustDecimal("10")},
			},
			Equity: shared.MustDecimal("100000"), AvailableFunds: shared.MustDecimal("100000"),
			ExchangeUpdatedAt: testNow,
			Present: exchange.AccountSnapshotPresence{
				Balances: true, Equity: true, AvailableFunds: true, UsedMargin: true,
				MaintenanceMargin: true, UnrealizedPnL: true,
			},
		},
	}
}

func (f *fakeExchange) Exchange() exchange.Exchange { return exchange.ExchangeBinance }
func (f *fakeExchange) LoadInstruments(context.Context) ([]exchange.Instrument, error) {
	return []exchange.Instrument{f.instrument}, nil
}
func (f *fakeExchange) GetReferencePrice(context.Context, string) (exchange.ReferencePrice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reference, nil
}
func (f *fakeExchange) GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.accountErr != nil {
		return exchange.AccountSnapshot{}, f.accountErr
	}
	return f.account, nil
}
func (f *fakeExchange) ListPositionSnapshots(context.Context) ([]exchange.Position, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.positionsErr != nil {
		return nil, f.positionsErr
	}
	return append([]exchange.Position(nil), f.positions...), nil
}
func (f *fakeExchange) ListOpenOrders(context.Context) ([]exchange.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.openOrdersErr != nil {
		return nil, f.openOrdersErr
	}
	var result []exchange.Order
	for _, current := range f.orders {
		if current.Status == exchange.OrderStatusOpen ||
			current.Status == exchange.OrderStatusPartiallyFilled {
			result = append(result, current)
		}
	}
	return result, nil
}
func (f *fakeExchange) ListRecentFills(_ context.Context, symbol, cursor string) ([]exchange.Fill, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fillsErr != nil {
		return nil, cursor, f.fillsErr
	}
	result := make([]exchange.Fill, 0, len(f.fills))
	next := cursor
	pastCursor := cursor == ""
	for _, fill := range f.fills {
		if !pastCursor {
			pastCursor = fill.ExchangeTradeID == cursor
			continue
		}
		if fill.Symbol == symbol {
			result = append(result, fill)
			next = fill.ExchangeTradeID
		}
	}
	return result, next, nil
}
func (f *fakeExchange) GetOrder(_ context.Context, _ string, clientOrderID string) (exchange.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookupCalls++
	current, ok := f.orders[clientOrderID]
	if !ok {
		return exchange.Order{}, &exchange.Error{Kind: exchange.ErrorOrderNotFound}
	}
	return current, nil
}
func (f *fakeExchange) PlaceOrder(_ context.Context, request exchange.OrderRequest) (exchange.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.placeCalls++
	f.requests = append(f.requests, request)
	if f.placeErr != nil {
		return exchange.Order{}, f.placeErr
	}
	if current, ok := f.orders[request.ClientOrderID]; ok {
		return current, nil
	}
	now := testNow.Add(time.Duration(f.placeCalls) * time.Second)
	current := exchange.Order{
		ExchangeOrderID: fmt.Sprintf("exchange-%d", f.placeCalls),
		ClientOrderID:   request.ClientOrderID, Symbol: request.Symbol,
		OrderType: request.OrderType, TimeInForce: request.NativeTimeInForce(),
		Side: request.Side, PositionSide: request.PositionSide,
		Quantity: request.Quantity, ReduceOnly: request.ReduceOnly,
		Status: exchange.OrderStatusOpen, CreatedAt: now, UpdatedAt: now,
	}
	f.orders[request.ClientOrderID] = current
	return current, nil
}
func (f *fakeExchange) CancelOrder(_ context.Context, _ string, clientOrderID string) (exchange.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	current, ok := f.orders[clientOrderID]
	if !ok {
		return exchange.Order{}, &exchange.Error{Kind: exchange.ErrorOrderNotFound}
	}
	current.Status = exchange.OrderStatusCanceled
	current.UpdatedAt = current.UpdatedAt.Add(time.Second)
	f.orders[clientOrderID] = current
	return current, nil
}
func (f *fakeExchange) SetLeverage(_ context.Context, symbol string, leverage shared.Decimal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.leverageCalls = append(f.leverageCalls, leverageCall{symbol: symbol, leverage: leverage})
	return nil
}
func (f *fakeExchange) SetMarginMode(_ context.Context, symbol string, mode exchange.MarginMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marginCalls = append(f.marginCalls, marginCall{symbol: symbol, mode: mode})
	return nil
}
func (f *fakeExchange) SubscribePrivate(_ context.Context, handler exchange.EventHandler) error {
	f.mu.Lock()
	f.subscribeCalls++
	err := f.subscribeErr
	f.mu.Unlock()
	if err == nil {
		exchange.NotifyPrivateReady(handler)
	}
	return err
}

type recordingAdapter struct {
	exchange.Adapter
	recorder *fakeExchange
}

func (a recordingAdapter) PlaceOrder(ctx context.Context, request exchange.OrderRequest) (exchange.Order, error) {
	a.recorder.mu.Lock()
	a.recorder.requests = append(a.recorder.requests, request)
	a.recorder.placeCalls++
	a.recorder.mu.Unlock()
	return a.Adapter.PlaceOrder(ctx, request)
}

func (a recordingAdapter) SetLeverage(ctx context.Context, symbol string, leverage shared.Decimal) error {
	a.recorder.mu.Lock()
	a.recorder.leverageCalls = append(
		a.recorder.leverageCalls,
		leverageCall{symbol: symbol, leverage: leverage},
	)
	a.recorder.mu.Unlock()
	return a.Adapter.SetLeverage(ctx, symbol, leverage)
}

func (a recordingAdapter) SetMarginMode(ctx context.Context, symbol string, mode exchange.MarginMode) error {
	a.recorder.mu.Lock()
	a.recorder.marginCalls = append(a.recorder.marginCalls, marginCall{symbol: symbol, mode: mode})
	a.recorder.mu.Unlock()
	return a.Adapter.SetMarginMode(ctx, symbol, mode)
}

func (f *fakeExchange) emitFill(clientID, tradeID, quantity, price, realized string) exchange.Fill {
	f.mu.Lock()
	defer f.mu.Unlock()
	current := f.orders[clientID]
	fill := exchange.Fill{
		ExchangeTradeID: tradeID, ExchangeOrderID: current.ExchangeOrderID,
		ClientOrderID: clientID, Symbol: current.Symbol, Side: current.Side,
		PositionSide: current.PositionSide, Quantity: shared.MustDecimal(quantity),
		Price: shared.MustDecimal(price), Fee: shared.MustDecimal("0.1"), FeeAsset: "USDT",
		RealizedPnL: shared.MustDecimal(realized), SettlementAsset: "USDT",
		LiquidityRole: "TAKER", TradedAt: testNow.Add(time.Duration(len(f.fills)+10) * time.Second),
	}
	f.fills = append(f.fills, fill)
	current.FilledQuantity = current.FilledQuantity.Add(fill.Quantity)
	current.AveragePrice = fill.Price
	if current.FilledQuantity.Cmp(current.Quantity) >= 0 {
		current.Status = exchange.OrderStatusFilled
	} else {
		current.Status = exchange.OrderStatusPartiallyFilled
	}
	f.orders[clientID] = current
	return fill
}

type adapterSource struct{ adapter exchange.Adapter }

func (s adapterSource) Adapter(string) (exchange.Adapter, error) { return s.adapter, nil }

type readySession bool

func (s readySession) ReadyFor(tradingaccount.Account) bool { return bool(s) }
func (readySession) Invalidate(string)                      {}
func (s readySession) Ready(string) bool                    { return bool(s) }

type instrumentSource struct{ store *store.Store }

func (s instrumentSource) GetInstrument(ctx context.Context, name exchange.Exchange, market exchange.MarketType, symbol string) (exchange.Instrument, error) {
	record, err := s.store.GetInstrument(ctx, string(name), string(market), symbol)
	if err != nil {
		return exchange.Instrument{}, err
	}
	return exchange.Instrument{
		Exchange: name, MarketType: market, Symbol: record.Symbol,
		InstrumentID: record.InstrumentID, BaseAsset: record.BaseAsset,
		QuoteAsset: record.QuoteAsset, SettlementAsset: record.SettlementAsset,
		Linear: record.Linear, ContractValue: decimal(record.ContractValue),
		ContractValueAsset:   record.ContractValueAsset,
		ExchangeQuantityStep: decimal(record.ExchangeQuantityStep),
		MinExchangeQuantity:  decimal(record.MinExchangeQuantity),
		PriceTick:            decimal(record.PriceTick), MinNotional: decimal(record.MinNotional),
		Status: record.Status, ExchangeUpdatedAt: time.UnixMilli(record.ExchangeUpdatedAt),
	}, nil
}

type positionSource struct{ store *store.Store }

func (s positionSource) GetPosition(ctx context.Context, accountID, symbol string) (exchange.Position, error) {
	record, found, err := s.store.GetPosition(ctx, testSpace, accountID, symbol, string(exchange.PositionSideNet))
	if err != nil || !found {
		return exchange.Position{TradingAccountID: accountID, Symbol: symbol, PositionSide: exchange.PositionSideNet}, err
	}
	return exchange.Position{
		TradingAccountID: accountID, Symbol: symbol,
		PositionSide:   exchange.PositionSide(record.PositionSide),
		SignedQuantity: decimal(record.SignedQuantity),
		EntryPrice:     decimal(record.EntryPrice), MarkPrice: decimal(record.MarkPrice),
		Leverage: decimal(record.Leverage), MarginMode: exchange.MarginMode(record.MarginMode),
		RealizedPnL: decimal(record.RealizedPnL),
	}, nil
}

type syncBridge struct{ service *accountsync.Service }

func (s syncBridge) SyncAccount(ctx context.Context, accountID string) error {
	_, err := s.service.SyncAccount(ctx, accountID)
	return err
}

type fixture struct {
	path    string
	store   *store.Store
	fake    *fakeExchange
	adapter exchange.Adapter
	orders  *orderapp.Service
	sync    *accountsync.Service
	reducer *consumer.Reducer
	account *accountapp.Service
}

func newFixture(t *testing.T, market exchange.MarketType, adapter exchange.Adapter) *fixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trade.db")
	tradeStore, err := store.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = tradeStore.Close()
	})
	fake, ok := adapter.(*fakeExchange)
	if !ok {
		fake = newFakeExchange(market)
	}
	seedFixture(t, tradeStore, market, fake.instrument)
	if adapter == nil {
		adapter = fake
	}
	return buildFixture(tradeStore, path, fake, adapter)
}

func newPaperFixture(t *testing.T, market exchange.MarketType) *fixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trade.db")
	tradeStore, err := store.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = tradeStore.Close()
	})
	fake := newFakeExchange(market)
	seedFixture(t, tradeStore, market, fake.instrument)
	margin := exchange.MarginModeUnspecified
	settings := store.LeverageSettings{}
	if market == exchange.MarketTypeSwap {
		margin = exchange.MarginModeCross
		settings[testSymbol] = "10"
	}
	adapter := paper.New(
		fake, tradeStore, testSpace, testAccount, market, "USDT",
		shared.MustDecimal("100000"), margin, settings,
	)
	_, err = adapter.LoadInstruments(context.Background())
	require.NoError(t, err)
	return buildFixture(
		tradeStore,
		path,
		fake,
		recordingAdapter{Adapter: adapter, recorder: fake},
	)
}

func buildFixture(
	tradeStore *store.Store,
	path string,
	fake *fakeExchange,
	adapter exchange.Adapter,
) *fixture {
	source := adapterSource{adapter: adapter}
	accountService := &accountapp.Service{
		Store: accountapp.Repository{Store: tradeStore}, SessionState: readySession(true),
	}
	reducer := &consumer.Reducer{Store: tradeStore, Now: func() time.Time { return testNow }}
	syncService := &accountsync.Service{
		Store: tradeStore, Adapters: source, SessionState: readySession(true),
		Fills: reducer, Now: func() time.Time { return testNow.Add(time.Minute) },
	}
	orderService := &orderapp.Service{
		Store: tradeStore,
		Validator: orderapp.Validator{
			Accounts: accountService, Instruments: instrumentSource{store: tradeStore},
			Positions: positionSource{store: tradeStore}, Now: func() time.Time { return testNow },
			MaxReferenceAge: time.Minute, MaxChildNotional: shared.MustDecimal("1000000"),
			MaxLeverage: shared.MustDecimal("20"), FeeBufferRate: shared.MustDecimal("0.001"),
		},
		Adapters: source, Syncer: syncBridge{service: syncService},
		Now: func() time.Time { return testNow }, UnknownLookupWindow: time.Second,
	}
	syncService.Orders = orderService
	return &fixture{
		path: path, store: tradeStore, fake: fake, adapter: adapter, orders: orderService,
		sync: syncService, reducer: reducer, account: accountService,
	}
}

func seedFixture(t *testing.T, tradeStore *store.Store, market exchange.MarketType, instrument exchange.Instrument) {
	t.Helper()
	margin := ""
	leverage := store.LeverageSettings{}
	if market == exchange.MarketTypeSwap {
		margin = string(exchange.MarginModeCross)
		leverage[testSymbol] = "10"
	}
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: testSpace, TradingAccountID: testAccount, Name: "E2E",
			Exchange: string(exchange.ExchangeBinance), MarketType: string(market),
			ExecutionMode:   string(exchange.ExecutionModePaper),
			Environment:     string(exchange.AccountEnvironmentPaper),
			SettlementAsset: "USDT",
			MarginMode:      margin, Status: string(exchange.AccountStatusEnabled), Ready: true,
			SyncSymbols: []string{testSymbol}, LeverageSettings: leverage,
			Snapshot: store.TradingAccountSnapshot{
				Balances: []store.AssetBalance{
					{Asset: "USDT", Available: "100000", Total: "100000"},
					{Asset: "BTC", Available: "10", Total: "10"},
				},
				Equity: "100000", AvailableFunds: "100000", ExchangeUpdatedAt: testNow.UnixMilli(),
			},
			SnapshotSourceTime: testNow.UnixMilli(), LastSyncAt: testNow.UnixMilli(),
		}); err != nil {
			return err
		}
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: string(instrument.Exchange), MarketType: string(market),
			Symbol: instrument.Symbol, InstrumentID: instrument.InstrumentID,
			BaseAsset: instrument.BaseAsset, QuoteAsset: instrument.QuoteAsset,
			SettlementAsset: instrument.SettlementAsset, Linear: instrument.Linear,
			ContractValue:        instrument.ContractValue.String(),
			ContractValueAsset:   instrument.ContractValueAsset,
			ExchangeQuantityStep: instrument.ExchangeQuantityStep.String(),
			MinExchangeQuantity:  instrument.MinExchangeQuantity.String(),
			PriceTick:            instrument.PriceTick.String(), MinNotional: instrument.MinNotional.String(),
			Status: instrument.Status, ExchangeUpdatedAt: testNow.UnixMilli(),
		})
	}))
}

func (f *fixture) close(t *testing.T) {
	t.Helper()
	require.NoError(t, f.store.Close())
	f.store = nil
}

func marketSpec(clientID string, side exchange.Side, quantity string) orderdomain.OrderSpec {
	return orderdomain.OrderSpec{
		ClientOrderSpec: orderdomain.ClientOrderSpec{
			TradingAccountID: testAccount, ClientOrderID: clientID,
			InstrumentID: testSymbol, Type: exchange.OrderTypeMarket, Side: side,
			Quantity: shared.MustDecimal(quantity),
		},
		ReferencePrice: shared.MustDecimal("50000"), ReferencePriceAt: testNow,
		Owner: orderdomain.OrderOwner{
			Type: orderdomain.OwnerExternal, OwnerID: "e2e-" + clientID,
		},
	}
}

func swapSpec(clientID string, side exchange.Side, quantity string, reduceOnly bool) orderdomain.OrderSpec {
	spec := marketSpec(clientID, side, quantity)
	spec.PositionSide = exchange.PositionSideNet
	spec.ReducePositionOnly = reduceOnly
	return spec
}

func mustPlace(t *testing.T, fixture *fixture, spec orderdomain.OrderSpec) orderdomain.Order {
	t.Helper()
	value, err := fixture.orders.Place(context.Background(), testSpace, spec)
	require.NoError(t, err)
	return value
}

func decimal(raw string) shared.Decimal {
	if raw == "" {
		return shared.Zero()
	}
	return shared.MustDecimal(raw)
}

func transportError(message string) error {
	return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: errors.New(message)}
}
