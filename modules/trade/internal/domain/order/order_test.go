package order

import (
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

func TestOrderLifecycle(t *testing.T) {
	order, events, err := New("order-1", validSpec())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if order.State != Pending || order.Version != 1 || len(events) != 1 {
		t.Fatalf("new order = %+v, events = %+v", order, events)
	}

	steps := []struct {
		name string
		run  func() ([]Event, error)
		want State
	}{
		{"begin submit", order.BeginSubmit, Submitting},
		{"acknowledge", func() ([]Event, error) { return order.Acknowledge("exchange-1") }, Open},
		{"partial fill", func() ([]Event, error) {
			return order.ApplyFill(shared.MustDecimal("0.4"))
		}, PartiallyFilled},
		{"fill", func() ([]Event, error) {
			return order.ApplyFill(shared.MustDecimal("0.6"))
		}, Filled},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			if _, err := step.run(); err != nil {
				t.Fatalf("transition error = %v", err)
			}
			if order.State != step.want {
				t.Fatalf("state = %s, want %s", order.State, step.want)
			}
		})
	}
	if _, err := order.BeginCancel(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("terminal transition error = %v", err)
	}
}

func TestOrderUnknownStates(t *testing.T) {
	order, _, _ := New("order-1", validSpec())
	_, _ = order.BeginSubmit()
	if _, err := order.MarkSubmitUnknown(); err != nil {
		t.Fatalf("MarkSubmitUnknown() error = %v", err)
	}
	if _, err := order.Acknowledge("exchange-1"); err != nil {
		t.Fatalf("Acknowledge() error = %v", err)
	}
	if _, err := order.BeginCancel(); err != nil {
		t.Fatalf("BeginCancel() error = %v", err)
	}
	if _, err := order.MarkCancelUnknown(); err != nil {
		t.Fatalf("MarkCancelUnknown() error = %v", err)
	}
	if _, err := order.ConfirmCancel(); err != nil {
		t.Fatalf("ConfirmCancel() error = %v", err)
	}
	if order.State != Canceled {
		t.Fatalf("state = %s", order.State)
	}
}

func TestOrderRequiresPositiveQuantityAndIdentity(t *testing.T) {
	tests := []struct {
		name   string
		id     shared.OrderID
		mutate func(*OrderSpec)
	}{
		{"missing ID", "", func(*OrderSpec) {}},
		{"missing account", "order-1", func(spec *OrderSpec) { spec.ExchangeAccountID = "" }},
		{"missing client ID", "order-1", func(spec *OrderSpec) { spec.ClientOrderID = "" }},
		{"zero quantity", "order-1", func(spec *OrderSpec) { spec.Quantity = shared.Zero() }},
		{"negative quantity", "order-1", func(spec *OrderSpec) { spec.Quantity = shared.MustDecimal("-1") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := validSpec()
			tt.mutate(&spec)
			if _, _, err := New(tt.id, spec); !errors.Is(err, ErrInvalidOrder) {
				t.Fatalf("New() error = %v, want ErrInvalidOrder", err)
			}
		})
	}
}

func validSpec() OrderSpec {
	return OrderSpec{
		ExchangeAccountID: "account-1",
		ClientOrderID:     "client-1",
		Symbol:            "BTC-USDT",
		OrderType:         exchange.OrderTypeMarket,
		Side:              exchange.SideBuy,
		Quantity:          shared.MustDecimal("1"),
		ReferencePrice:    shared.MustDecimal("60000"),
		ReferencePriceAt:  time.Now(),
		Source:            "RPC",
	}
}
