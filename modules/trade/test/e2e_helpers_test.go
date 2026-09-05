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
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	executionpaper "github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

const (
	testSpace        = "space-e2e"
	testAccount      = "account-e2e"
	testSymbol       = "BTCUSDT"
	testInstrumentID = "BTC-USDT"
)

// Target validity is checked against the real Trade clock. Keep the shared
// fixture clock near process start so generated target windows remain valid
// while the package tests run.
var testNow = time.Now().UTC()

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
	quoteErr       error
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
		ExchangeSymbol: testSymbol, InstrumentID: "BTC-USDT",
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
func (f *fakeExchange) GetQuote(context.Context, shared.ExchangeSymbol) (execution.MarketQuote, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.quoteErr != nil {
		return execution.MarketQuote{}, f.quoteErr
	}
	return execution.MarketQuote{Bid: f.reference.Price, Ask: f.reference.Price, Last: f.reference.Price, SourceTime: f.reference.UpdatedAt}, nil
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
func (f *fakeExchange) ListRecentFills(_ context.Context, symbol shared.ExchangeSymbol, cursor string) ([]exchange.Fill, string, error) {
	symbolValue := symbol.String()
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
		if fill.ExchangeSymbol == symbolValue {
			result = append(result, fill)
			next = fill.ExchangeTradeID
		}
	}
	return result, next, nil
}
func (f *fakeExchange) GetOrder(_ context.Context, _ shared.ExchangeSymbol, clientOrderID string) (exchange.Order, error) {
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
		ClientOrderID:   request.ClientOrderID, ExchangeSymbol: request.ExchangeSymbol,
		OrderType: request.OrderType, TimeInForce: request.NativeTimeInForce(),
		Side: request.Side, PositionSide: request.PositionSide,
		Quantity: request.Quantity, ReduceOnly: request.ReduceOnly,
		Status: exchange.OrderStatusOpen, CreatedAt: now, UpdatedAt: now,
	}
	f.orders[request.ClientOrderID] = current
	return current, nil
}
func (f *fakeExchange) CancelOrder(_ context.Context, _ shared.ExchangeSymbol, clientOrderID string) (exchange.Order, error) {
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
func (f *fakeExchange) SetLeverage(_ context.Context, symbol shared.ExchangeSymbol, leverage shared.Decimal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.leverageCalls = append(f.leverageCalls, leverageCall{symbol: symbol.String(), leverage: leverage})
	return nil
}
func (f *fakeExchange) SetMarginMode(_ context.Context, symbol shared.ExchangeSymbol, mode exchange.MarginMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marginCalls = append(f.marginCalls, marginCall{symbol: symbol.String(), mode: mode})
	return nil
}
func (f *fakeExchange) Subscribe(_ context.Context, handler execution.AccountEventHandler) error {
	f.mu.Lock()
	f.subscribeCalls++
	err := f.subscribeErr
	f.mu.Unlock()
	if err == nil {
		handler.OnSubscribed()
	}
	return err
}

type recordingAdapter struct {
	execution.ExecutionAdapter
	recorder *fakeExchange
}

func (a recordingAdapter) LoadInstruments(ctx context.Context) ([]exchange.Instrument, error) {
	source, ok := a.ExecutionAdapter.(execution.MarketDataSource)
	if !ok {
		return nil, errors.New("recording adapter: market data source is unavailable")
	}
	return source.LoadInstruments(ctx)
}

func (a recordingAdapter) GetQuote(ctx context.Context, symbol shared.ExchangeSymbol) (execution.MarketQuote, error) {
	source, ok := a.ExecutionAdapter.(execution.MarketDataSource)
	if !ok {
		return execution.MarketQuote{}, errors.New("recording adapter: market data source is unavailable")
	}
	return source.GetQuote(ctx, symbol)
}

func (a recordingAdapter) GetReferencePrice(ctx context.Context, symbol string) (exchange.ReferencePrice, error) {
	source, ok := a.ExecutionAdapter.(execution.ReferencePriceSource)
	if !ok {
		return exchange.ReferencePrice{}, errors.New("recording adapter: reference price source is unavailable")
	}
	return source.GetReferencePrice(ctx, symbol)
}

func (a recordingAdapter) PlaceOrder(ctx context.Context, request exchange.OrderRequest) (exchange.Order, error) {
	a.recorder.mu.Lock()
	a.recorder.requests = append(a.recorder.requests, request)
	a.recorder.placeCalls++
	a.recorder.mu.Unlock()
	return a.ExecutionAdapter.PlaceOrder(ctx, request)
}

func (a recordingAdapter) SetLeverage(ctx context.Context, symbol shared.ExchangeSymbol, leverage shared.Decimal) error {
	a.recorder.mu.Lock()
	a.recorder.leverageCalls = append(
		a.recorder.leverageCalls,
		leverageCall{symbol: symbol.String(), leverage: leverage},
	)
	a.recorder.mu.Unlock()
	return a.ExecutionAdapter.SetLeverage(ctx, symbol, leverage)
}

func (a recordingAdapter) SetMarginMode(ctx context.Context, symbol shared.ExchangeSymbol, mode exchange.MarginMode) error {
	a.recorder.mu.Lock()
	a.recorder.marginCalls = append(a.recorder.marginCalls, marginCall{symbol: symbol.String(), mode: mode})
	a.recorder.mu.Unlock()
	return a.ExecutionAdapter.SetMarginMode(ctx, symbol, mode)
}

// synchronousPaperAdapter keeps the older unit-style E2E ergonomics while
// exercising the production SQLite-backed paper adapter. The real runtime
// matches these orders asynchronously; this test adapter queues a deterministic
// fill for the normal account-sync path to consume immediately.
type synchronousPaperAdapter struct {
	*executionpaper.Adapter
	Store   *store.Store
	Fake    *fakeExchange
	mu      sync.Mutex
	pending map[string][]exchange.Fill
}

func (a *synchronousPaperAdapter) PlaceOrder(ctx context.Context, request exchange.OrderRequest) (exchange.Order, error) {
	response, err := a.Adapter.PlaceOrder(ctx, request)
	if err != nil {
		return exchange.Order{}, err
	}
	record, err := a.Store.GetOrderByClientID(ctx, testSpace, testAccount, request.ClientOrderID)
	if err != nil {
		return exchange.Order{}, err
	}
	price := request.ReferencePrice
	if request.LimitPrice != nil {
		price = *request.LimitPrice
	}
	realized := shared.Zero()
	if request.ReduceOnly && record.MarketType == string(exchange.MarketTypeSwap) {
		position, found, positionErr := a.Store.GetPosition(ctx, testSpace, testAccount, record.ExchangeSymbol, string(exchange.PositionSideNet))
		if positionErr != nil {
			return exchange.Order{}, positionErr
		}
		if found {
			entry := decimal(position.EntryPrice)
			quantity := request.Quantity
			if decimal(position.SignedQuantity).Cmp(shared.Zero()) >= 0 {
				realized = price.Sub(entry).Mul(quantity)
			} else {
				realized = entry.Sub(price).Mul(quantity)
			}
		}
	}
	fill := exchange.Fill{
		ExchangeTradeID: "paper-trade-" + request.ClientOrderID,
		ExchangeOrderID: response.ExchangeOrderID, ClientOrderID: request.ClientOrderID,
		ExchangeSymbol: record.ExchangeSymbol,
		Side:           exchange.Side(record.Side), PositionSide: exchange.PositionSide(record.PositionSide),
		Quantity: request.Quantity, Price: price, Fee: shared.Zero(),
		FeeAsset: record.ReservedAsset, SettlementAsset: "USDT",
		RealizedPnL: realized, LiquidityRole: "TAKER", TradedAt: testNow,
	}
	a.mu.Lock()
	if a.pending == nil {
		a.pending = make(map[string][]exchange.Fill)
	}
	a.pending[record.ExchangeSymbol] = append(a.pending[record.ExchangeSymbol], fill)
	a.mu.Unlock()
	response.Status = exchange.OrderStatusFilled
	response.FilledQuantity = request.Quantity
	response.AveragePrice = price
	return response, nil
}

func (a *synchronousPaperAdapter) ListRecentFills(
	ctx context.Context,
	symbol shared.ExchangeSymbol,
	cursor string,
) ([]exchange.Fill, string, error) {
	rows, next, err := a.Adapter.ListRecentFills(ctx, symbol, cursor)
	if err != nil {
		return nil, cursor, err
	}
	a.mu.Lock()
	pending := append([]exchange.Fill(nil), a.pending[symbol.String()]...)
	delete(a.pending, symbol.String())
	a.mu.Unlock()
	return append(rows, pending...), next, nil
}

func (a *synchronousPaperAdapter) GetAccountSnapshot(ctx context.Context) (exchange.AccountSnapshot, error) {
	snapshot, err := a.Adapter.GetAccountSnapshot(ctx)
	if err != nil {
		return exchange.AccountSnapshot{}, err
	}
	a.mu.Lock()
	pending := make([]exchange.Fill, 0)
	for _, fills := range a.pending {
		pending = append(pending, fills...)
	}
	a.mu.Unlock()
	if a.Account.MarketType != string(exchange.MarketTypeSpot) {
		return snapshot, nil
	}
	balances := make(map[string]exchange.AssetBalance, len(snapshot.Balances))
	for _, balance := range snapshot.Balances {
		balances[balance.Asset] = balance
	}
	for _, fill := range pending {
		instrument, instrumentErr := a.Store.GetInstrument(ctx, a.Account.Exchange, a.Account.MarketType, fill.ExchangeSymbol)
		if instrumentErr != nil {
			continue
		}
		if instrument.BaseAsset == "" || instrument.QuoteAsset == "" {
			continue
		}
		base, quote := balances[instrument.BaseAsset], balances[instrument.QuoteAsset]
		base.Asset, quote.Asset = instrument.BaseAsset, instrument.QuoteAsset
		value := fill.Price.Mul(fill.Quantity)
		if fill.Side == exchange.SideBuy {
			base.Available = base.Available.Add(fill.Quantity)
			base.Total = base.Total.Add(fill.Quantity)
			quote.Available = quote.Available.Sub(value)
			quote.Total = quote.Total.Sub(value)
			snapshot.AvailableFunds = snapshot.AvailableFunds.Sub(value)
		} else {
			base.Available = base.Available.Sub(fill.Quantity)
			base.Total = base.Total.Sub(fill.Quantity)
			quote.Available = quote.Available.Add(value)
			quote.Total = quote.Total.Add(value)
			snapshot.AvailableFunds = snapshot.AvailableFunds.Add(value)
		}
		balances[instrument.BaseAsset], balances[instrument.QuoteAsset] = base, quote
	}
	snapshot.Balances = snapshot.Balances[:0]
	for _, balance := range balances {
		snapshot.Balances = append(snapshot.Balances, balance)
	}
	return snapshot, nil
}

func (a *synchronousPaperAdapter) ListPositionSnapshots(ctx context.Context) ([]exchange.Position, error) {
	positions, err := a.Adapter.ListPositionSnapshots(ctx)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	pending := make([]exchange.Fill, 0)
	for _, fills := range a.pending {
		pending = append(pending, fills...)
	}
	a.mu.Unlock()
	if a.Account.MarketType != string(exchange.MarketTypeSwap) {
		return positions, nil
	}
	bySymbol := make(map[string]int, len(positions))
	for index := range positions {
		bySymbol[positions[index].ExchangeSymbol] = index
	}
	for _, fill := range pending {
		index, found := bySymbol[fill.ExchangeSymbol]
		if !found {
			positions = append(positions, exchange.Position{
				TradingAccountID:  a.Account.TradingAccountID,
				InstrumentID:      fill.ExchangeSymbol,
				ExchangeSymbol:    fill.ExchangeSymbol,
				PositionSide:      exchange.PositionSideNet,
				Leverage:          decimal(a.Account.LeverageSettings[fill.ExchangeSymbol]),
				MarginMode:        exchange.MarginMode(a.Account.MarginMode),
				ExchangeUpdatedAt: testNow,
			})
			index = len(positions) - 1
			bySymbol[fill.ExchangeSymbol] = index
		}
		position := &positions[index]
		delta := fill.Quantity
		if fill.Side == exchange.SideSell {
			delta = delta.Neg()
		}
		if position.SignedQuantity.IsZero() {
			position.EntryPrice = fill.Price
		}
		position.SignedQuantity = position.SignedQuantity.Add(delta)
		position.MarkPrice = fill.Price
		position.ExchangeUpdatedAt = testNow
	}
	return positions, nil
}

func (f *fakeExchange) emitFill(clientID, tradeID, quantity, price, realized string) exchange.Fill {
	f.mu.Lock()
	defer f.mu.Unlock()
	current := f.orders[clientID]
	fill := exchange.Fill{
		ExchangeTradeID: tradeID, ExchangeOrderID: current.ExchangeOrderID,
		ClientOrderID: clientID, ExchangeSymbol: current.ExchangeSymbol, Side: current.Side,
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

type adapterSource struct{ adapter execution.ExecutionAdapter }

func (s adapterSource) Adapter(string) (execution.ExecutionAdapter, error) { return s.adapter, nil }

type readySession bool

func (s readySession) ReadyFor(tradingaccount.Account) bool { return bool(s) }
func (readySession) Invalidate(string)                      {}
func (s readySession) Ready(string) bool                    { return bool(s) }

type instrumentSource struct{ store *store.Store }

func (s instrumentSource) GetInstrument(ctx context.Context, name exchange.Exchange, market exchange.MarketType, symbol string) (exchange.Instrument, error) {
	record, err := s.store.GetInstrumentByIDScoped(ctx, symbol, string(name), string(market))
	if err != nil {
		return exchange.Instrument{}, err
	}
	return exchange.Instrument{
		Exchange: name, MarketType: market, ExchangeSymbol: record.ExchangeSymbol,
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
	if !found && err == nil {
		if instrument, instrumentErr := s.store.GetInstrumentByIDScoped(ctx, symbol, "BINANCE", "SWAP"); instrumentErr == nil {
			record, found, err = s.store.GetPosition(ctx, testSpace, accountID, instrument.ExchangeSymbol, string(exchange.PositionSideNet))
		}
	}
	if err != nil || !found {
		return exchange.Position{TradingAccountID: accountID, ExchangeSymbol: symbol, PositionSide: exchange.PositionSideNet}, err
	}
	return exchange.Position{
		TradingAccountID: accountID, ExchangeSymbol: symbol,
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

func (s syncBridge) ConfirmCancel(ctx context.Context, spaceID, orderID string) error {
	return s.service.ConfirmCancel(ctx, spaceID, orderID)
}

type fixture struct {
	path    string
	store   *store.Store
	fake    *fakeExchange
	adapter execution.ExecutionAdapter
	orders  *orderapp.Service
	sync    *accountsync.Service
	reducer *consumer.Reducer
	account *accountapp.Service
}

func newFixture(t *testing.T, market exchange.MarketType, adapter execution.ExecutionAdapter) *fixture {
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
	account, err := tradeStore.GetTradingAccountByID(context.Background(), testAccount)
	require.NoError(t, err)
	base := &executionpaper.Adapter{Account: account, Store: tradeStore, MarketData: fake, Now: func() time.Time { return testNow }}
	_, err = base.LoadInstruments(context.Background())
	require.NoError(t, err)
	adapter := &synchronousPaperAdapter{Adapter: base, Store: tradeStore, Fake: fake}
	return buildFixture(
		tradeStore,
		path,
		fake,
		recordingAdapter{ExecutionAdapter: adapter, recorder: fake},
	)
}

func buildFixture(
	tradeStore *store.Store,
	path string,
	fake *fakeExchange,
	adapter execution.ExecutionAdapter,
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
			ExchangeSymbol: instrument.ExchangeSymbol, InstrumentID: instrument.InstrumentID,
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
			InstrumentID: testInstrumentID, Type: exchange.OrderTypeMarket, Side: side,
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
