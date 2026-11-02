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
	"github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

var ErrSessionConfig = errors.New("trade runtime: ExchangeSession is not configured")

const (
	sessionOrderWindow  = 5 * time.Minute
	pollingSyncInterval = 3 * time.Second
)

var errPrivateDisconnected = errors.New("trade runtime: private stream disconnected")

type ExchangeSession struct {
	Account               store.TradingAccountRecord
	Adapter               execution.ExecutionAdapter
	MarketData            execution.MarketDataSource
	AccountEvents         execution.AccountEventSource
	Sync                  *accountsync.Service
	SyncInterval          time.Duration
	PaperMatcherReady     func() bool
	PaperAccountState     func(string) paper.MatcherState
	PaperAccountRecovered func(string, uint64)
	OnReady               func(string)

	ready         atomic.Bool
	opMu          sync.Mutex
	syncRequested chan struct{}
}

type TradingSession = ExchangeSession

func (s *ExchangeSession) Ready() bool {
	if s == nil || !s.ready.Load() {
		return false
	}
	if s.Account.ExecutionMode == string(exchange.ExecutionModePaper) {
		if s.PaperMatcherReady == nil || !s.PaperMatcherReady() {
			return false
		}
		if s.PaperAccountState != nil && !s.PaperAccountState(s.Account.TradingAccountID).Ready {
			return false
		}
	}
	return true
}

func (s *ExchangeSession) ExecutionAdapter() execution.ExecutionAdapter {
	if s == nil {
		return nil
	}
	return s.Adapter
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
	marketData := s.marketData()
	if exchange.RuleFor(account.MarketType).PositionFromBalance &&
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
	if accountEvents == nil {
		return s.runPolling(ctx)
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
	if configurer, ok := s.Adapter.(execution.SymbolConfigurer); ok && exchange.RuleFor(s.Account.MarketType).Leverage {
		symbols := leverageSymbols(s.Account.LeverageSettings)
		for _, symbol := range symbols {
			if err := configurer.SetMarginMode(
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
			if err := configurer.SetLeverage(ctx, shared.ExchangeSymbol(symbol), leverage); err != nil {
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
		time.Now().Add(-sessionOrderWindow).UnixMilli(),
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
		select {
		case <-disconnected:
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
	if err := handler.finishActivation(ctx); err != nil {
		return s.disconnect(ctx, err)
	}
	// Keep Ready invisible until the handler has released its activation lock.
	// Events arriving in that handoff window are then applied synchronously
	// before callers can submit against this session.
	if err, ended := privateStreamError(disconnected, streamDone); ended {
		return s.disconnect(ctx, err)
	}
	previous := s.ready.Swap(true)
	if !previous && s.OnReady != nil {
		s.OnReady(s.Account.TradingAccountID)
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
			// 同步失败时账户已记录错误并保持未就绪，下一轮重试；只有私有流结束才重启会话。
			s.opMu.Lock()
			_, _ = s.Sync.SyncAccount(ctx, s.Account.TradingAccountID)
			s.opMu.Unlock()
			if err, ended := privateStreamError(disconnected, streamDone); ended {
				return s.disconnect(context.Background(), err)
			}
		case <-s.syncRequested:
			s.opMu.Lock()
			_, _ = s.Sync.SyncAccount(ctx, s.Account.TradingAccountID)
			s.opMu.Unlock()
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
	generation := s.paperAccountGeneration()
	// 模拟适配器保存标的到原生代码的映射，所以从它加载。
	instruments, err := s.Adapter.LoadInstruments(ctx)
	if err != nil {
		return s.disconnect(ctx, err)
	}
	if len(instruments) > 0 {
		if err := s.persistInstruments(ctx, instruments); err != nil {
			return s.disconnect(ctx, err)
		}
	}
	// SyncAccount reads and applies the complete batch under the account lock.
	// Prefetching here could overwrite a fill committed while ApplySnapshot
	// waits for that lock, then falsely retire its refresh failure.
	if _, err := s.Sync.SyncAccount(ctx, s.Account.TradingAccountID); err != nil {
		return s.disconnect(ctx, err)
	}
	orders, err := s.Adapter.ListOpenOrders(ctx)
	if err != nil {
		return s.disconnect(ctx, err)
	}
	s.recoverPaperAccountAfterSync(generation, orders)
	setReady := func(ready bool) error {
		var cause error
		if !ready && s.PaperAccountState != nil {
			state := s.PaperAccountState(s.Account.TradingAccountID)
			if !state.Ready && state.LastError != "" {
				cause = errors.New(state.LastError)
			}
		}
		if err := s.Sync.SetReady(ctx, s.Account.TradingAccountID, ready, cause); err != nil {
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
		if s.PaperAccountState != nil {
			matcherReady = matcherReady && s.PaperAccountState(s.Account.TradingAccountID).Ready
		}
		if matcherReady != s.ready.Load() {
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
			generation := s.paperAccountGeneration()
			if _, err := s.Sync.SyncAccount(ctx, s.Account.TradingAccountID); err != nil {
				return s.disconnect(ctx, err)
			}
			orders, err := s.Adapter.ListOpenOrders(ctx)
			if err != nil {
				return s.disconnect(ctx, err)
			}
			s.recoverPaperAccountAfterSync(generation, orders)
		}
	}
}

// runPolling 给没有私有流的场所用：短周期同步代替事件推送。
func (s *ExchangeSession) runPolling(ctx context.Context) error {
	instruments, err := s.marketData().LoadInstruments(ctx)
	if err != nil {
		return s.disconnect(ctx, err)
	}
	if err := s.persistInstruments(ctx, instruments); err != nil {
		return s.disconnect(ctx, err)
	}
	if _, err := s.Sync.SyncAccount(ctx, s.Account.TradingAccountID); err != nil {
		return s.disconnect(ctx, err)
	}
	s.ready.Store(true)
	if err := s.Sync.SetReady(ctx, s.Account.TradingAccountID, true, nil); err != nil {
		return s.disconnect(ctx, err)
	}
	if s.OnReady != nil {
		s.OnReady(s.Account.TradingAccountID)
	}
	ticker := time.NewTicker(pollingSyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return s.disconnect(context.Background(), ctx.Err())
		case <-ticker.C:
			// 失败时账户已记录错误并保持未就绪，下一轮重试。
			_, _ = s.Sync.SyncAccount(ctx, s.Account.TradingAccountID)
		}
	}
}

func (s *ExchangeSession) marketData() execution.MarketDataSource {
	if s.MarketData != nil {
		return s.MarketData
	}
	return s.Adapter
}

func (s *ExchangeSession) paperAccountGeneration() uint64 {
	if s.PaperAccountState == nil {
		return 0
	}
	return s.PaperAccountState(s.Account.TradingAccountID).Generation
}

func (s *ExchangeSession) recoverPaperAccount(generation uint64) {
	if s.PaperAccountRecovered != nil {
		s.PaperAccountRecovered(s.Account.TradingAccountID, generation)
	}
}

func (s *ExchangeSession) recoverPaperAccountAfterSync(generation uint64, orders []exchange.Order) {
	// A balance-only sync does not prove that a failing order quote recovered.
	// Once that order is gone, sync can retire the otherwise orphaned fault.
	if s.PaperAccountState != nil && s.PaperAccountState(s.Account.TradingAccountID).Stage == "decision" && len(orders) != 0 {
		return
	}
	s.recoverPaperAccount(generation)
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
				ExchangeSymbol: instrument.ExchangeSymbol, InstrumentID: instrument.InstrumentID,
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
		applied, err := s.Sync.ApplyFill(ctx, s.Account.TradingAccountID, event.fill)
		if err == nil && applied && s.syncRequested != nil {
			// 成交已使准入关闭，请求完整同步以确认余额后恢复
			select {
			case s.syncRequested <- struct{}{}:
			default:
			}
		}
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
	setReady  func() error
	gateMu    sync.Mutex
	gateCond  *sync.Cond
	closing   bool
	activated bool
	inflight  int
	// dirty：激活前处理过会使持久化就绪置为“等待完整同步”的事件（成交、持仓）。此时不能再把就绪写回 true，
	// 必须等排队的完整同步确认。
	dirty bool
}

func newSessionHandler(
	apply func(context.Context, privateEvent) error,
) *sessionHandler {
	handler := &sessionHandler{
		buffering: true,
		ready:     make(chan struct{}),
		apply:     apply,
	}
	handler.gateCond = sync.NewCond(&handler.gateMu)
	return handler
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
	h.gateMu.Lock()
	for h.closing && !h.activated {
		h.gateCond.Wait()
	}
	if !h.activated {
		h.inflight++
	}
	activated := h.activated
	h.gateMu.Unlock()
	if activated {
		return h.apply(ctx, event)
	}
	defer func() {
		h.gateMu.Lock()
		h.inflight--
		h.gateCond.Broadcast()
		h.gateMu.Unlock()
	}()
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
			// Persist readiness only after the final gate has drained events
			// admitted during this initial activation pass.
			h.setReady = setReady
			h.mu.Unlock()
			return nil
		}
		pending := append([]privateEvent(nil), h.pending...)
		h.pending = h.pending[:0]
		h.mu.Unlock()
		h.markDirty(pending)
		for _, event := range pending {
			if err := h.apply(ctx, event); err != nil {
				h.gateMu.Lock()
				h.activated = true
				h.closing = false
				h.gateCond.Broadcast()
				h.gateMu.Unlock()
				return err
			}
		}
	}
}

func (h *sessionHandler) markDirty(events []privateEvent) {
	for _, event := range events {
		if event.kind == privateFill || event.kind == privatePosition || event.kind == privateAccount {
			h.mu.Lock()
			h.dirty = true
			h.mu.Unlock()
			return
		}
	}
}

// finishActivation closes admission, drains pre-ready events, then commits
// readiness before allowing synchronous event processing.
func (h *sessionHandler) finishActivation(ctx context.Context) error {
	h.gateMu.Lock()
	h.closing = true
	for h.inflight > 0 {
		h.gateCond.Wait()
	}
	h.gateMu.Unlock()
	defer func() {
		h.gateMu.Lock()
		h.activated = true
		h.closing = false
		h.gateCond.Broadcast()
		h.gateMu.Unlock()
	}()
	for {
		h.mu.Lock()
		if len(h.pending) == 0 {
			setReady := h.setReady
			dirty := h.dirty
			h.buffering = false
			h.mu.Unlock()
			if setReady != nil && !dirty {
				return setReady()
			}
			return nil
		}
		pending := append([]privateEvent(nil), h.pending...)
		h.pending = h.pending[:0]
		h.mu.Unlock()
		h.markDirty(pending)
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
		if symbol != store.DefaultLeverageKey {
			symbols = append(symbols, symbol)
		}
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
		if symbol != store.DefaultLeverageKey {
			set[symbol] = struct{}{}
		}
	}
	for symbol := range account.FillCursors {
		set[symbol] = struct{}{}
	}
	for _, current := range orders {
		if current.ExchangeSymbol != "" {
			set[current.ExchangeSymbol] = struct{}{}
		}
	}
	for _, current := range local {
		if current.ExchangeSymbol != "" {
			set[current.ExchangeSymbol] = struct{}{}
		}
	}
	for _, position := range positions {
		if position.ExchangeSymbol != "" {
			set[position.ExchangeSymbol] = struct{}{}
		}
	}
	if exchange.RuleFor(account.MarketType).PositionFromBalance {
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
