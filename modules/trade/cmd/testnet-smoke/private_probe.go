package main

import (
	"context"
	"errors"
	"sync"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
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
	exchange.Adapter
	probe *privateOrderProbe
}

func (a probingAdapter) Subscribe(
	ctx context.Context,
	handler exchange.EventHandler,
) error {
	return a.Adapter.Subscribe(ctx, probingHandler{
		EventHandler: handler,
		probe:        a.probe,
	})
}

func (a probingAdapter) GetReferencePrice(
	ctx context.Context,
	symbol string,
) (exchange.ReferencePrice, error) {
	source, ok := a.Adapter.(exchange.ReferencePriceSource)
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
	lookup, ok := a.Adapter.(exchange.ExchangeOrderLookup)
	if !ok {
		return exchange.Order{}, errors.New(
			"testnet adapter has no Exchange order lookup",
		)
	}
	return lookup.GetOrderByExchangeID(ctx, symbol, exchangeOrderID)
}

func (a probingAdapter) MarkPrivateStreamMetadataReady() {
	if gate, ok := a.Adapter.(exchange.PrivateStreamMetadataGate); ok {
		gate.MarkPrivateStreamMetadataReady()
	}
}

type probingHandler struct {
	exchange.EventHandler
	probe *privateOrderProbe
}

func (h probingHandler) OnPrivateReady() {
	exchange.NotifyPrivateReady(h.EventHandler)
}

func (h probingHandler) OnOrder(
	ctx context.Context,
	order exchange.Order,
) error {
	h.probe.observe(order.ClientOrderID)
	return h.EventHandler.OnOrder(ctx, order)
}

func (h probingHandler) OnFill(
	ctx context.Context,
	fill exchange.Fill,
) error {
	h.probe.observe(fill.ClientOrderID)
	return h.EventHandler.OnFill(ctx, fill)
}
