package main

import (
	"context"
	"errors"
	"sync"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
)

type privateOrderProbe struct {
	mu      sync.Mutex
	seen    map[string]struct{}
	waiters map[string]chan struct{}
}

func newPrivateOrderProbe() *privateOrderProbe {
	return &privateOrderProbe{
		seen:    make(map[string]struct{}),
		waiters: make(map[string]chan struct{}),
	}
}

func (p *privateOrderProbe) observe(clientOrderID string) {
	if p == nil || clientOrderID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen[clientOrderID] = struct{}{}
	if waiter := p.waiters[clientOrderID]; waiter != nil {
		close(waiter)
		delete(p.waiters, clientOrderID)
	}
}

func (p *privateOrderProbe) wait(
	ctx context.Context,
	clientOrderID string,
) error {
	if p == nil || clientOrderID == "" {
		return errors.New("private order probe requires a client order ID")
	}
	p.mu.Lock()
	if _, found := p.seen[clientOrderID]; found {
		p.mu.Unlock()
		return nil
	}
	waiter := make(chan struct{})
	p.waiters[clientOrderID] = waiter
	p.mu.Unlock()
	select {
	case <-ctx.Done():
		p.mu.Lock()
		delete(p.waiters, clientOrderID)
		p.mu.Unlock()
		return ctx.Err()
	case <-waiter:
		return nil
	}
}

type probingAdapter struct {
	execution.ExecutionAdapter
	marketData    execution.MarketDataSource
	accountEvents execution.AccountEventSource
	probe         *privateOrderProbe
}

func newProbingAdapter(adapter execution.ExecutionAdapter, probe *privateOrderProbe) probingAdapter {
	marketData, _ := adapter.(execution.MarketDataSource)
	accountEvents, _ := adapter.(execution.AccountEventSource)
	return probingAdapter{ExecutionAdapter: adapter, marketData: marketData, accountEvents: accountEvents, probe: probe}
}

func (a probingAdapter) Subscribe(
	ctx context.Context,
	handler execution.AccountEventHandler,
) error {
	if a.accountEvents == nil {
		return errors.New("testnet adapter has no account-event source")
	}
	return a.accountEvents.Subscribe(ctx, probingHandler{
		AccountEventHandler: handler,
		probe:               a.probe,
	})
}

func (a probingAdapter) LoadInstruments(ctx context.Context) ([]exchange.Instrument, error) {
	if a.marketData == nil {
		return nil, errors.New("testnet adapter has no market-data source")
	}
	return a.marketData.LoadInstruments(ctx)
}

func (a probingAdapter) GetQuote(ctx context.Context, symbol shared.ExchangeSymbol) (execution.MarketQuote, error) {
	if a.marketData == nil {
		return execution.MarketQuote{}, errors.New("testnet adapter has no market-data source")
	}
	return a.marketData.GetQuote(ctx, symbol)
}

func (a probingAdapter) GetReferencePrice(
	ctx context.Context,
	symbol string,
) (exchange.ReferencePrice, error) {
	source, ok := a.ExecutionAdapter.(execution.ReferencePriceSource)
	if !ok {
		return exchange.ReferencePrice{}, errors.New(
			"testnet adapter has no reference price source",
		)
	}
	return source.GetReferencePrice(ctx, symbol)
}

func (a probingAdapter) GetOrderByExchangeID(
	ctx context.Context,
	symbol string,
	exchangeOrderID string,
) (exchange.Order, error) {
	lookup, ok := a.ExecutionAdapter.(execution.ExchangeOrderLookup)
	if !ok {
		return exchange.Order{}, errors.New(
			"testnet adapter has no Exchange order lookup",
		)
	}
	return lookup.GetOrderByExchangeID(ctx, symbol, exchangeOrderID)
}

type probingHandler struct {
	execution.AccountEventHandler
	probe *privateOrderProbe
}

func (h probingHandler) OnSubscribed() {
	h.AccountEventHandler.OnSubscribed()
}

func (h probingHandler) OnOrder(
	ctx context.Context,
	order exchange.Order,
) error {
	h.probe.observe(order.ClientOrderID)
	return h.AccountEventHandler.OnOrder(ctx, order)
}

func (h probingHandler) OnFill(
	ctx context.Context,
	fill exchange.Fill,
) error {
	h.probe.observe(fill.ClientOrderID)
	return h.AccountEventHandler.OnFill(ctx, fill)
}

func (h probingHandler) OnPosition(ctx context.Context, position exchange.Position) error {
	return h.AccountEventHandler.OnPosition(ctx, position)
}

func (h probingHandler) OnAccountSnapshot(ctx context.Context, snapshot exchange.AccountSnapshot) error {
	return h.AccountEventHandler.OnAccountSnapshot(ctx, snapshot)
}
