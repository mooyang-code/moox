package main

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
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
	exchange.Adapter
	metadataReady bool
	lookedUpID    string
}

func (a *capabilityAdapter) MarkPrivateStreamMetadataReady() {
	a.metadataReady = true
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
	adapter := probingAdapter{Adapter: base, probe: newPrivateOrderProbe()}

	adapter.MarkPrivateStreamMetadataReady()
	if !base.metadataReady {
		t.Fatal("metadata-ready signal was not forwarded")
	}
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

	var _ exchange.PrivateStreamMetadataGate = adapter
	var _ exchange.ExchangeOrderLookup = adapter
	var _ exchange.ReferencePriceSource = adapter
}
