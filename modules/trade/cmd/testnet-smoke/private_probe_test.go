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
	leverage   string
	marginMode exchange.MarginMode
}

func (a *capabilityAdapter) SetLeverage(_ context.Context, _ exchange.SymbolKey, leverage shared.Decimal) error {
	a.leverage = leverage.String()
	return nil
}

func (a *capabilityAdapter) SetMarginMode(_ context.Context, _ exchange.SymbolKey, mode exchange.MarginMode) error {
	a.marginMode = mode
	return nil
}

func (*capabilityAdapter) GetReferencePrice(context.Context, exchange.SymbolKey) (exchange.ReferencePrice, error) {
	return exchange.ReferencePrice{Price: shared.MustDecimal("1")}, nil
}

func (a *capabilityAdapter) GetOrderByExchangeID(_ context.Context, _ exchange.SymbolKey, exchangeOrderID string) (exchange.Order, error) {
	a.lookedUpID = exchangeOrderID
	return exchange.Order{Market: exchange.MarketTypeSwap, ExchangeOrderID: exchangeOrderID}, nil
}

func TestProbingAdapterPreservesOptionalCapabilities(t *testing.T) {
	base := &capabilityAdapter{}
	adapter := newProbingAdapter(base, newPrivateOrderProbe())
	order, err := adapter.GetOrderByExchangeID(
		context.Background(),
		exchange.NewSymbolKey(exchange.MarketTypeSwap, "BTCUSDT"),
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

	// 会话只在适配器实现了 SymbolConfigurer 时才设置全仓和杠杆：包装后必须仍然实现，并转发到真实适配器。
	var wrapped execution.ExecutionAdapter = adapter
	configurer, ok := wrapped.(execution.SymbolConfigurer)
	if !ok {
		t.Fatal("包装后的适配器丢了 SymbolConfigurer，永续测试单不会设置杠杆和全仓")
	}
	symbol := exchange.NewSymbolKey(exchange.MarketTypeSwap, "BTCUSDT")
	if err := configurer.SetMarginMode(context.Background(), symbol, exchange.MarginModeCross); err != nil {
		t.Fatal(err)
	}
	if err := configurer.SetLeverage(context.Background(), symbol, shared.MustDecimal("1")); err != nil {
		t.Fatal(err)
	}
	if base.marginMode != exchange.MarginModeCross || base.leverage != "1" {
		t.Fatalf("杠杆和保证金模式没有转发：%+v", base)
	}
}
