package main

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
)

func TestPrivateOrderProbeAcceptsEventBeforeWait(t *testing.T) {
	probe := newPrivateOrderProbe()
	probe.observe("client-1")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := probe.wait(ctx, "client-1"); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateOrderProbeWakesWaitingSubmit(t *testing.T) {
	probe := newPrivateOrderProbe()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- probe.wait(ctx, "client-2")
	}()

	probe.observe("client-2")
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

type capabilityAdapter struct {
	execution.ExecutionAdapter
	lookedUpID string
}

func (*capabilityAdapter) GetReferencePrice(
	context.Context,
	string,
) (exchange.ReferencePrice, error) {
	return exchange.ReferencePrice{Price: shared.MustDecimal("1")}, nil
}

func (a *capabilityAdapter) GetOrderByExchangeID(
	_ context.Context,
	_ string,
	exchangeOrderID string,
) (exchange.Order, error) {
	a.lookedUpID = exchangeOrderID
	return exchange.Order{ExchangeOrderID: exchangeOrderID}, nil
}

func TestProbingAdapterPreservesOptionalCapabilities(t *testing.T) {
	base := &capabilityAdapter{}
	adapter := newProbingAdapter(base, newPrivateOrderProbe())
	order, err := adapter.GetOrderByExchangeID(
		context.Background(),
		"BTCUSDT",
		"exchange-1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if base.lookedUpID != "exchange-1" || order.ExchangeOrderID != "exchange-1" {
		t.Fatalf("lookup was not forwarded: base=%q order=%+v", base.lookedUpID, order)
	}

	var _ execution.ExchangeOrderLookup = adapter
	var _ execution.ReferencePriceSource = adapter
}
