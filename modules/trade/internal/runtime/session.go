package runtime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/accountsync"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

var ErrSessionConfig = errors.New("trade runtime: ExchangeSession is not configured")
var errPrivateDisconnected = errors.New("trade runtime: private stream disconnected")

type ExchangeSession struct {
	Account           store.TradingAccountRecord
	Adapter           execution.ExecutionAdapter
	MarketData        execution.MarketDataSource
	AccountEvents     execution.AccountEventSource
	ReservationPolicy execution.ReservationPolicy
	Sync              *accountsync.Service
	SyncInterval      time.Duration
	PaperMatcherReady func() bool
	OnReady           func(string)

	ready         atomic.Bool
	opMu          sync.Mutex
	syncRequested chan struct{}
}

type TradingSession = ExchangeSession

func (s *ExchangeSession) Ready() bool {
	return s != nil && s.ready.Load()
}

func (s *ExchangeSession) ExecutionAdapter() execution.ExecutionAdapter {
	if s == nil {
		return nil
	}
	return s.Adapter
}

func (s *ExchangeSession) ExecutionBundle() execution.ExecutionBundle {
	if s == nil {
		return execution.ExecutionBundle{}
	}
	policy := s.ReservationPolicy
	if policy == nil {
		policy = execution.LiveReservationPolicy{}
		if exchange.ExecutionMode(s.Account.ExecutionMode) == exchange.ExecutionModePaper {
			policy = execution.PaperReservationPolicy{}
		}
	}
	return execution.ExecutionBundle{
		Adapter: s.Adapter, AccountEvents: s.AccountEvents, MarketData: s.MarketData,
		ReservationPolicy: policy,
	}
}

func (s *ExchangeSession) Run(ctx context.Context) error {
	if s == nil || s.Adapter == nil || s.Sync == nil || s.Sync.Store == nil ||
		s.Account.TradingAccountID == "" {
		return ErrSessionConfig
	}
	account, err := s.Sync.Store.GetTradingAccountByID(
		ctx,
		s.Account.TradingAccountID,
	)
	if err != nil {
		return err
	}
	s.Account = account
	marketData := s.MarketData
	if marketData == nil {
		marketData, _ = s.Adapter.(execution.MarketDataSource)
	}
	if exchange.MarketType(account.MarketType) == exchange.MarketTypeSpot &&
		exchange.ExecutionMode(account.ExecutionMode) == exchange.ExecutionModeLive &&
		len(account.SyncSymbols) == 0 {
		return fmt.Errorf("%w: SPOT account requires sync symbols", ErrSessionConfig)
	}
	if exchange.ExecutionMode(account.ExecutionMode) == exchange.ExecutionModePaper {
		return s.runPaper(ctx)
	}
	accountEvents := s.AccountEvents
	if accountEvents == nil {
		accountEvents, _ = s.Adapter.(execution.AccountEventSource)
	}
	if marketData == nil || accountEvents == nil {
		return ErrSessionConfig
	}
	// Public metadata must be durable before private events are accepted. The
	// handler only starts buffering once Subscribe begins, so the later REST
	// snapshot can safely replay the buffered stream against canonical facts.
	instruments, err := marketData.LoadInstruments(ctx)
	if err != nil {
		return s.disconnect(ctx, err)
	}
	if err := s.persistInstruments(ctx, instruments); err != nil {
		return s.disconnect(ctx, err)
	}
	s.syncRequested = make(chan struct{}, 1)
	s.ready.Store(false)
	_ = s.Sync.SetReady(ctx, s.Account.TradingAccountID, false, nil)

	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	handler := newSessionHandler(s.applyEvent)
	streamDone := make(chan error, 1)
	disconnected := make(chan struct{})
	go func() {
		err := accountEvents.Subscribe(streamCtx, handler)
		s.ready.Store(false)
		close(disconnected)
		streamDone <- err
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-streamDone:
		return s.disconnect(ctx, err)
	case <-handler.ready:
	}
	if err, ended := privateStreamError(disconnected, streamDone); ended {
		return s.disconnect(ctx, err)
	}

	if err, ended := privateStreamError(disconnected, streamDone); ended {
		return s.disconnect(ctx, err)
	}
	if exchange.MarketType(s.Account.MarketType) == exchange.MarketTypeSwap {
		symbols := leverageSymbols(s.Account.LeverageSettings)
		for _, symbol := range symbols {
			if err := s.Adapter.SetMarginMode(
				ctx,
				shared.ExchangeSymbol(symbol),
				exchange.MarginModeCross,
			); err != nil {
				return s.disconnect(ctx, err)
			}
		}
		for _, symbol := range symbols {
			leverage, parseErr := shared.ParseDecimal(s.Account.LeverageSettings[symbol])
			if parseErr != nil {
				return s.disconnect(ctx, parseErr)
			}
			if err := s.Adapter.SetLeverage(ctx, shared.ExchangeSymbol(symbol), leverage); err != nil {
				return s.disconnect(ctx, err)
			}
		}
	}

	accountSnapshot, err := s.Adapter.GetAccountSnapshot(ctx)
	if err != nil {
		return s.disconnect(ctx, err)
	}
	if err, ended := privateStreamError(disconnected, streamDone); ended {
		return s.disconnect(ctx, err)
	}
	positions, err := s.Adapter.ListPositionSnapshots(ctx)
	if err != nil {
		return s.disconnect(ctx, err)
	}
	if err, ended := privateStreamError(disconnected, streamDone); ended {
		return s.disconnect(ctx, err)
	}
	orders, err := s.Adapter.ListOpenOrders(ctx)
	if err != nil {
		return s.disconnect(ctx, err)
	}
	if err, ended := privateStreamError(disconnected, streamDone); ended {
		return s.disconnect(ctx, err)
	}
	localOrders, err := s.Sync.Store.ListOrdersForAccount(
		ctx,
		s.Account.SpaceID,
		s.Account.TradingAccountID,
		0,
	)
	if err != nil {
		return s.disconnect(ctx, err)
	}
	symbols := sessionSymbols(
		s.Account,
		orders,
		localOrders,
		positions,
		instruments,
		accountSnapshot,
	)
	fills := make([]exchange.Fill, 0)
	cursors := cloneFillCursors(s.Account.FillCursors)
	for _, symbol := range symbols {
		rows, cursor, listErr := s.Adapter.ListRecentFills(
			ctx,
			shared.ExchangeSymbol(symbol),
			cursors[symbol],
		)
		if listErr != nil {
			return s.disconnect(ctx, listErr)
		}
		if err, ended := privateStreamError(disconnected, streamDone); ended {
			return s.disconnect(ctx, err)
		}
		fills = append(fills, rows...)
		if cursor != "" {
			cursors[symbol] = cursor
		}
	}

	s.opMu.Lock()
	_, err = s.Sync.ApplySnapshot(ctx, s.Account.TradingAccountID, accountsync.Snapshot{
		Fills: fills, Orders: orders, Positions: positions,
		Account: accountSnapshot, FillCursors: cursors, Ready: false,
	})
	s.opMu.Unlock()
	if err != nil {
		return s.disconnect(ctx, err)
	}
	if err := handler.activate(ctx, func() error {
		if err, ended := privateStreamError(disconnected, streamDone); ended {
			return err
		}
		if err := s.Sync.SetReady(ctx, s.Account.TradingAccountID, true, nil); err != nil {
			return err
		}
		s.ready.Store(true)
		select {
		case <-disconnected:
			s.ready.Store(false)
			_ = s.Sync.SetReady(
				context.Background(),
				s.Account.TradingAccountID,
				false,
				errPrivateDisconnected,
			)
			return errPrivateDisconnected
		default:
		}
		return nil
	}); err != nil {
		return s.disconnect(ctx, err)
	}

	interval := s.SyncInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return s.disconnect(context.Background(), ctx.Err())
		case err := <-streamDone:
			return s.disconnect(context.Background(), err)
		case <-ticker.C:
			s.opMu.Lock()
			_, err := s.Sync.SyncAccount(ctx, s.Account.TradingAccountID)
			s.opMu.Unlock()
			if err != nil {
				return s.disconnect(context.Background(), err)
			}
			if err, ended := privateStreamError(disconnected, streamDone); ended {
				return s.disconnect(context.Background(), err)
			}
		case <-s.syncRequested:
			s.opMu.Lock()
			_, err := s.Sync.SyncAccount(ctx, s.Account.TradingAccountID)
			s.opMu.Unlock()
			if err != nil {
				return s.disconnect(context.Background(), err)
			}
			if err, ended := privateStreamError(disconnected, streamDone); ended {
				return s.disconnect(context.Background(), err)
			}
		}
	}
}

// runPaper shares the same SQLite facts and sync projections as Live, but has
// no private stream. Readiness is owned by the single process-local matcher.
func (s *ExchangeSession) runPaper(ctx context.Context) error {
	if s.Adapter == nil || s.Sync == nil {
		return ErrSessionConfig
	}
	marketData := s.MarketData
	if marketData == nil {
		marketData, _ = s.Adapter.(execution.MarketDataSource)
	}
	if marketData == nil {
		return ErrSessionConfig
	}
	// Paper's adapter keeps the canonical instrument -> native symbol map used
	// by reference quotes. Load through it when available, while retaining the
	// public source fallback for lightweight adapters in tests.
	instrumentLoader, _ := s.Adapter.(interface {
		LoadInstruments(context.Context) ([]exchange.Instrument, error)
	})
	var instruments []exchange.Instrument
	var err error
	if instrumentLoader != nil {
		instruments, err = instrumentLoader.LoadInstruments(ctx)
	} else {
		instruments, err = marketData.LoadInstruments(ctx)
	}
	if err != nil {
		return s.disconnect(ctx, err)
	}
	if len(instruments) > 0 {
		if err := s.persistInstruments(ctx, instruments); err != nil {
			return s.disconnect(ctx, err)
		}
	}
	accountSnapshot, err := s.Adapter.GetAccountSnapshot(ctx)
	if err != nil {
		return s.disconnect(ctx, err)
	}
	positions, err := s.Adapter.ListPositionSnapshots(ctx)
	if err != nil {
		return s.disconnect(ctx, err)
	}
	orders, err := s.Adapter.ListOpenOrders(ctx)
	if err != nil {
		return s.disconnect(ctx, err)
	}
	localOrders, err := s.Sync.Store.ListOrdersForAccount(ctx, s.Account.SpaceID, s.Account.TradingAccountID, 0)
	if err != nil {
		return s.disconnect(ctx, err)
	}
	symbols := sessionSymbols(s.Account, orders, localOrders, positions, instruments, accountSnapshot)
	fills := make([]exchange.Fill, 0)
	for _, symbol := range symbols {
		rows, _, listErr := s.Adapter.ListRecentFills(ctx, shared.ExchangeSymbol(symbol), "")
		if listErr != nil {
			return s.disconnect(ctx, listErr)
		}
		fills = append(fills, rows...)
	}
	if _, err := s.Sync.ApplySnapshot(ctx, s.Account.TradingAccountID, accountsync.Snapshot{
		Fills: fills, Orders: orders, Positions: positions, Account: accountSnapshot, Ready: false,
	}); err != nil {
		return s.disconnect(ctx, err)
	}
	setReady := func(ready bool) error {
		if err := s.Sync.SetReady(ctx, s.Account.TradingAccountID, ready, nil); err != nil {
			return err
		}
		previous := s.ready.Swap(ready)
		if ready && !previous && s.OnReady != nil {
			s.OnReady(s.Account.TradingAccountID)
		}
		return nil
	}
	interval := s.SyncInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()
	refresh := time.NewTicker(interval)
	defer refresh.Stop()
	for {
		matcherReady := s.PaperMatcherReady != nil && s.PaperMatcherReady()
		if matcherReady != s.Ready() {
			if err := setReady(matcherReady); err != nil {
				return s.disconnect(ctx, err)
			}
		}
		select {
		case <-ctx.Done():
			_ = s.disconnect(context.Background(), ctx.Err())
			return ctx.Err()
		case <-poll.C:
		case <-refresh.C:
			if _, err := s.Sync.SyncAccount(ctx, s.Account.TradingAccountID); err != nil {
				return s.disconnect(ctx, err)
			}
		}
	}
}

func privateStreamError(
	disconnected <-chan struct{},
	streamDone <-chan error,
) (error, bool) {
	select {
	case <-disconnected:
		err := <-streamDone
		if err == nil {
			err = errPrivateDisconnected
		}
		return err, true
	default:
		return nil, false
	}
}

func (s *ExchangeSession) disconnect(ctx context.Context, cause error) error {
	s.ready.Store(false)
	if ctx == nil {
		ctx = context.Background()
	}
	setCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := s.Sync.SetReady(setCtx, s.Account.TradingAccountID, false, cause); err != nil {
		if cause == nil {
			return err
		}
		return errors.Join(cause, err)
	}
	return cause
}

func (s *ExchangeSession) persistInstruments(
	ctx context.Context,
	instruments []exchange.Instrument,
) error {
	return s.Sync.Store.Transaction(ctx, func(tx *store.Tx) error {
		environment := s.Account.Environment
		if s.Account.ExecutionMode == "PAPER" || environment == "" {
			environment = "PRODUCTION"
		}
		for _, instrument := range instruments {
			if instrument.Exchange != exchange.Exchange(s.Account.Exchange) ||
				instrument.MarketType != exchange.MarketType(s.Account.MarketType) {
				return fmt.Errorf("trade runtime: conflicting instrument identity")
			}
			if err := tx.UpsertInstrument(store.InstrumentRecord{
				Exchange:       string(instrument.Exchange),
				Environment:    environment,
				MarketType:     string(instrument.MarketType),
				ExchangeSymbol: instrument.ExchangeSymbol, Symbol: instrument.Symbol, InstrumentID: instrument.InstrumentID,
				BaseAsset: instrument.BaseAsset, QuoteAsset: instrument.QuoteAsset,
				SettlementAsset: instrument.SettlementAsset, Linear: instrument.Linear,
				ContractValue:        instrument.ContractValue.String(),
				ContractValueAsset:   instrument.ContractValueAsset,
				ExchangeQuantityStep: instrument.ExchangeQuantityStep.String(),
				MinExchangeQuantity:  instrument.MinExchangeQuantity.String(),
				PriceTick:            instrument.PriceTick.String(),
				MinNotional:          instrument.MinNotional.String(), Status: instrument.Status,
				ExchangeUpdatedAt: instrument.ExchangeUpdatedAt.UnixMilli(),
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *ExchangeSession) applyEvent(ctx context.Context, event privateEvent) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	switch event.kind {
	case privateOrder:
		return s.Sync.ApplyOrder(ctx, s.Account.TradingAccountID, event.order)
	case privateFill:
		_, err := s.Sync.ApplyFill(ctx, s.Account.TradingAccountID, event.fill)
		return err
	case privatePosition:
		err := s.Sync.ApplyPosition(ctx, s.Account.TradingAccountID, event.position)
		if err == nil && s.syncRequested != nil {
			select {
			case s.syncRequested <- struct{}{}:
			default:
			}
		}
		return err
	case privateAccount:
		err := s.Sync.ApplyAccountSnapshot(
			ctx,
			s.Account.TradingAccountID,
			event.account,
		)
		if err == nil && s.syncRequested != nil {
			select {
			case s.syncRequested <- struct{}{}:
			default:
			}
		}
		return err
	default:
		return fmt.Errorf("trade runtime: unknown private event")
	}
}

type privateEventKind uint8

const (
	privateOrder privateEventKind = iota + 1
	privateFill
	privatePosition
	privateAccount
)

type privateEvent struct {
	kind     privateEventKind
	order    exchange.Order
	fill     exchange.Fill
	position exchange.Position
	account  exchange.AccountSnapshot
}

type sessionHandler struct {
	mu        sync.Mutex
	buffering bool
	pending   []privateEvent
	ready     chan struct{}
	readyOnce sync.Once
	apply     func(context.Context, privateEvent) error
}

func newSessionHandler(
	apply func(context.Context, privateEvent) error,
) *sessionHandler {
	return &sessionHandler{
		buffering: true,
		ready:     make(chan struct{}),
		apply:     apply,
	}
}

func (h *sessionHandler) OnSubscribed() {
	h.readyOnce.Do(func() { close(h.ready) })
}

func (h *sessionHandler) OnOrder(ctx context.Context, value exchange.Order) error {
	return h.handle(ctx, privateEvent{kind: privateOrder, order: value})
}

func (h *sessionHandler) OnFill(ctx context.Context, value exchange.Fill) error {
	return h.handle(ctx, privateEvent{kind: privateFill, fill: value})
}

func (h *sessionHandler) OnPosition(
	ctx context.Context,
	value exchange.Position,
) error {
	return h.handle(ctx, privateEvent{kind: privatePosition, position: value})
}

func (h *sessionHandler) OnAccountSnapshot(
	ctx context.Context,
	value exchange.AccountSnapshot,
) error {
	return h.handle(ctx, privateEvent{kind: privateAccount, account: value})
}

func (h *sessionHandler) handle(ctx context.Context, event privateEvent) error {
	h.mu.Lock()
	if h.buffering {
		h.pending = append(h.pending, event)
		h.mu.Unlock()
		return nil
	}
	h.mu.Unlock()
	return h.apply(ctx, event)
}

func (h *sessionHandler) activate(
	ctx context.Context,
	setReady func() error,
) error {
	for {
		h.mu.Lock()
		if len(h.pending) == 0 {
			if err := setReady(); err != nil {
				h.mu.Unlock()
				return err
			}
			h.buffering = false
			h.mu.Unlock()
			return nil
		}
		pending := append([]privateEvent(nil), h.pending...)
		h.pending = h.pending[:0]
		h.mu.Unlock()
		for _, event := range pending {
			if err := h.apply(ctx, event); err != nil {
				return err
			}
		}
	}
}

func leverageSymbols(settings store.LeverageSettings) []string {
	symbols := make([]string, 0, len(settings))
	for symbol := range settings {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)
	return symbols
}

func sessionSymbols(
	account store.TradingAccountRecord,
	orders []exchange.Order,
	local []store.OrderRecord,
	positions []exchange.Position,
	instruments []exchange.Instrument,
	snapshot exchange.AccountSnapshot,
) []string {
	set := make(map[string]struct{})
	for _, symbol := range account.SyncSymbols {
		set[symbol] = struct{}{}
	}
	for symbol := range account.LeverageSettings {
		set[symbol] = struct{}{}
	}
	for symbol := range account.FillCursors {
		set[symbol] = struct{}{}
	}
	for _, current := range orders {
		symbol := current.ExchangeSymbol
		if symbol == "" {
			symbol = current.Symbol
		}
		if symbol != "" {
			set[symbol] = struct{}{}
		}
	}
	for _, current := range local {
		if current.ExchangeSymbol != "" {
			set[current.ExchangeSymbol] = struct{}{}
		}
	}
	for _, position := range positions {
		symbol := position.ExchangeSymbol
		if symbol == "" {
			symbol = position.Symbol
		}
		if symbol != "" {
			set[symbol] = struct{}{}
		}
	}
	if exchange.MarketType(account.MarketType) == exchange.MarketTypeSpot {
		heldAssets := make(map[string]struct{})
		for _, balance := range snapshot.Balances {
			if balance.Total.Cmp(shared.Zero()) > 0 ||
				balance.Available.Cmp(shared.Zero()) > 0 ||
				balance.Locked.Cmp(shared.Zero()) > 0 {
				heldAssets[balance.Asset] = struct{}{}
			}
		}
		for _, instrument := range instruments {
			if instrument.Status != "TRADING" && instrument.Status != "live" {
				continue
			}
			if instrument.QuoteAsset != account.SettlementAsset {
				continue
			}
			if _, held := heldAssets[instrument.BaseAsset]; held {
				symbol := instrument.ExchangeSymbol
				if symbol == "" {
					symbol = instrument.Symbol
				}
				if symbol != "" {
					set[symbol] = struct{}{}
				}
			}
		}
	}
	symbols := make([]string, 0, len(set))
	for symbol := range set {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)
	return symbols
}

func cloneFillCursors(current store.FillCursors) store.FillCursors {
	result := make(store.FillCursors, len(current))
	for symbol, cursor := range current {
		result[symbol] = cursor
	}
	return result
}

var _ execution.AccountEventHandler = (*sessionHandler)(nil)
