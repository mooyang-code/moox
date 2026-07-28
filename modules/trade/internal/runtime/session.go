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
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

var ErrSessionConfig = errors.New("trade runtime: ExchangeSession is not configured")
var errPrivateDisconnected = errors.New("trade runtime: private stream disconnected")

type ExchangeSession struct {
	Account      store.ExchangeAccountRecord
	Adapter      exchange.Adapter
	Sync         *accountsync.Service
	SyncInterval time.Duration

	ready         atomic.Bool
	opMu          sync.Mutex
	syncRequested chan struct{}
}

func (s *ExchangeSession) Ready() bool {
	return s != nil && s.ready.Load()
}

func (s *ExchangeSession) ExchangeAdapter() exchange.Adapter {
	if s == nil {
		return nil
	}
	return s.Adapter
}

func (s *ExchangeSession) Run(ctx context.Context) error {
	if s == nil || s.Adapter == nil || s.Sync == nil || s.Sync.Store == nil ||
		s.Account.ExchangeAccountID == "" {
		return ErrSessionConfig
	}
	account, err := s.Sync.Store.GetExchangeAccountByID(
		ctx,
		s.Account.ExchangeAccountID,
	)
	if err != nil {
		return err
	}
	s.Account = account
	if exchange.MarketType(account.MarketType) == exchange.MarketTypeSpot &&
		len(account.SyncSymbols) == 0 {
		return fmt.Errorf("%w: SPOT account requires sync symbols", ErrSessionConfig)
	}
	s.syncRequested = make(chan struct{}, 1)
	s.ready.Store(false)
	_ = s.Sync.SetReady(ctx, s.Account.ExchangeAccountID, false, nil)

	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	handler := newSessionHandler(s.applyEvent)
	streamDone := make(chan error, 1)
	disconnected := make(chan struct{})
	go func() {
		err := s.Adapter.SubscribePrivate(streamCtx, handler)
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

	instruments, err := s.Adapter.LoadInstruments(ctx)
	if err != nil {
		return s.disconnect(ctx, err)
	}
	if gate, ok := s.Adapter.(exchange.PrivateStreamMetadataGate); ok {
		gate.MarkPrivateStreamMetadataReady()
	}
	if err, ended := privateStreamError(disconnected, streamDone); ended {
		return s.disconnect(ctx, err)
	}
	if err := s.persistInstruments(ctx, instruments); err != nil {
		return s.disconnect(ctx, err)
	}
	if exchange.MarketType(s.Account.MarketType) == exchange.MarketTypeSwap {
		symbols := leverageSymbols(s.Account.LeverageSettings)
		for _, symbol := range symbols {
			if err := s.Adapter.SetMarginMode(
				ctx,
				symbol,
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
			if err := s.Adapter.SetLeverage(ctx, symbol, leverage); err != nil {
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
		s.Account.ExchangeAccountID,
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
			symbol,
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
	_, err = s.Sync.ApplySnapshot(ctx, s.Account.ExchangeAccountID, accountsync.Snapshot{
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
		if err := s.Sync.SetReady(ctx, s.Account.ExchangeAccountID, true, nil); err != nil {
			return err
		}
		s.ready.Store(true)
		select {
		case <-disconnected:
			s.ready.Store(false)
			_ = s.Sync.SetReady(
				context.Background(),
				s.Account.ExchangeAccountID,
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
			_, err := s.Sync.SyncAccount(ctx, s.Account.ExchangeAccountID)
			s.opMu.Unlock()
			if err != nil {
				return s.disconnect(context.Background(), err)
			}
			if err, ended := privateStreamError(disconnected, streamDone); ended {
				return s.disconnect(context.Background(), err)
			}
		case <-s.syncRequested:
			s.opMu.Lock()
			_, err := s.Sync.SyncAccount(ctx, s.Account.ExchangeAccountID)
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
	if err := s.Sync.SetReady(setCtx, s.Account.ExchangeAccountID, false, cause); err != nil {
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
		for _, instrument := range instruments {
			if instrument.Exchange != exchange.Exchange(s.Account.Exchange) ||
				instrument.MarketType != exchange.MarketType(s.Account.MarketType) {
				return fmt.Errorf("trade runtime: conflicting instrument identity")
			}
			if err := tx.UpsertInstrument(store.InstrumentRecord{
				Exchange:   string(instrument.Exchange),
				MarketType: string(instrument.MarketType),
				Symbol:     instrument.Symbol, InstrumentID: instrument.InstrumentID,
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
		return s.Sync.ApplyOrder(ctx, s.Account.ExchangeAccountID, event.order)
	case privateFill:
		_, err := s.Sync.ApplyFill(ctx, s.Account.ExchangeAccountID, event.fill)
		return err
	case privatePosition:
		err := s.Sync.ApplyPosition(ctx, s.Account.ExchangeAccountID, event.position)
		if err == nil && event.position.RequiresSync && s.syncRequested != nil {
			select {
			case s.syncRequested <- struct{}{}:
			default:
			}
		}
		return err
	case privateAccount:
		err := s.Sync.ApplyAccountSnapshot(
			ctx,
			s.Account.ExchangeAccountID,
			event.account,
		)
		if err == nil && event.account.RequiresSync && s.syncRequested != nil {
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

func (h *sessionHandler) OnPrivateReady() {
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
	account store.ExchangeAccountRecord,
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
		if current.Symbol != "" {
			set[current.Symbol] = struct{}{}
		}
	}
	for _, current := range local {
		if current.Symbol != "" {
			set[current.Symbol] = struct{}{}
		}
	}
	for _, position := range positions {
		if position.Symbol != "" {
			set[position.Symbol] = struct{}{}
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
				set[instrument.Symbol] = struct{}{}
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

var _ exchange.EventHandler = (*sessionHandler)(nil)
var _ exchange.PrivateReadyHandler = (*sessionHandler)(nil)
