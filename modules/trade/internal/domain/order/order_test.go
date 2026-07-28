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
			return order.ApplyFill(Fill{ID: "fill-1", Quantity: shared.MustDecimal("0.4")})
		}, PartiallyFilled},
		{"fill", func() ([]Event, error) {
			return order.ApplyFill(Fill{ID: "fill-2", Quantity: shared.MustDecimal("0.6")})
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

func TestCancelAndFillRaceMatrix(t *testing.T) {
	tests := []struct {
		name          string
		initialFill   string
		cancelUnknown bool
		lateFill      string
		wantBefore    State
		wantFinal     State
	}{
		{
			name:       "partial Fill while canceling retains cancel intent",
			lateFill:   "0.4",
			wantBefore: Canceling,
			wantFinal:  PartiallyCanceled,
		},
		{
			name:          "partial Fill while cancel unknown retains cancel intent",
			cancelUnknown: true,
			lateFill:      "0.4",
			wantBefore:    CancelUnknown,
			wantFinal:     PartiallyCanceled,
		},
		{
			name:        "remaining Fill while canceling wins over cancel",
			initialFill: "0.4",
			lateFill:    "0.6",
			wantBefore:  Filled,
			wantFinal:   Filled,
		},
		{
			name:          "remaining Fill while cancel unknown wins over cancel",
			initialFill:   "0.4",
			cancelUnknown: true,
			lateFill:      "0.6",
			wantBefore:    Filled,
			wantFinal:     Filled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, _, _ := New("order-1", validSpec())
			_, _ = value.BeginSubmit()
			_, _ = value.Acknowledge("exchange-1")
			if tt.initialFill != "" {
				_, _ = value.ApplyFill(Fill{
					ID:       "fill-before-cancel",
					Quantity: shared.MustDecimal(tt.initialFill),
				})
			}
			_, _ = value.BeginCancel()
			if tt.cancelUnknown {
				_, _ = value.MarkCancelUnknown()
			}
			if _, err := value.ApplyFill(Fill{
				ID:       "fill-during-cancel",
				Quantity: shared.MustDecimal(tt.lateFill),
			}); err != nil {
				t.Fatalf("ApplyFill() error = %v", err)
			}
			if value.State != tt.wantBefore {
				t.Fatalf("state before ConfirmCancel = %s, want %s", value.State, tt.wantBefore)
			}
			if value.State == Filled {
				if _, err := value.ConfirmCancel(); !errors.Is(err, ErrInvalidTransition) {
					t.Fatalf("ConfirmCancel() error = %v, want terminal protection", err)
				}
			} else if _, err := value.ConfirmCancel(); err != nil {
				t.Fatalf("ConfirmCancel() error = %v", err)
			}
			if value.State != tt.wantFinal {
				t.Fatalf("final state = %s, want %s", value.State, tt.wantFinal)
			}
		})
	}
}

func TestTerminalOrderAcceptsUniqueLateFills(t *testing.T) {
	tests := []struct {
		name     string
		terminal func(*Order)
		want     State
	}{
		{
			name: "CANCELED becomes PARTIALLY_CANCELED",
			terminal: func(value *Order) {
				_, _ = value.BeginSubmit()
				_, _ = value.Acknowledge("exchange-1")
				_, _ = value.BeginCancel()
				_, _ = value.ConfirmCancel()
			},
			want: PartiallyCanceled,
		},
		{
			name: "PARTIALLY_CANCELED accepts another partial Fill",
			terminal: func(value *Order) {
				_, _ = value.BeginSubmit()
				_, _ = value.Acknowledge("exchange-1")
				_, _ = value.ApplyFill(Fill{ID: "fill-before-cancel", Quantity: shared.MustDecimal("0.2")})
				_, _ = value.BeginCancel()
				_, _ = value.ConfirmCancel()
			},
			want: PartiallyCanceled,
		},
		{
			name: "REJECTED repairs to PARTIALLY_CANCELED",
			terminal: func(value *Order) {
				_, _ = value.Reject()
			},
			want: PartiallyCanceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, _, _ := New("order-1", validSpec())
			tt.terminal(value)
			if _, err := value.ApplyFill(Fill{
				ID:       "unique-late-fill",
				Quantity: shared.MustDecimal("0.3"),
			}); err != nil {
				t.Fatalf("ApplyFill() error = %v", err)
			}
			if value.State != tt.want {
				t.Fatalf("state = %s, want %s", value.State, tt.want)
			}
		})
	}
}

func TestLateFillCanCompleteTerminalOrder(t *testing.T) {
	value, _, _ := New("order-1", validSpec())
	_, _ = value.Reject()
	if _, err := value.ApplyFill(Fill{
		ID:       "late-full-fill",
		Quantity: shared.MustDecimal("1"),
	}); err != nil {
		t.Fatalf("ApplyFill() error = %v", err)
	}
	if value.State != Filled {
		t.Fatalf("state = %s, want %s", value.State, Filled)
	}
}

func TestApplyFillIsIdempotentByFillIDAndRejectsOverfill(t *testing.T) {
	value, _, _ := New("order-1", validSpec())
	_, _ = value.BeginSubmit()
	_, _ = value.Acknowledge("exchange-1")
	fill := Fill{ID: "fill-1", Quantity: shared.MustDecimal("0.4")}
	events, err := value.ApplyFill(fill)
	if err != nil || len(events) != 1 {
		t.Fatalf("first ApplyFill() events = %v, error = %v", events, err)
	}
	version := value.Version
	events, err = value.ApplyFill(fill)
	if err != nil || len(events) != 0 {
		t.Fatalf("duplicate ApplyFill() events = %v, error = %v", events, err)
	}
	if value.Version != version || value.FilledQuantity.String() != "0.4" {
		t.Fatalf("duplicate mutated order = %+v", value)
	}
	if _, err := value.ApplyFill(Fill{
		ID:       "fill-over",
		Quantity: shared.MustDecimal("0.7"),
	}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("overfill error = %v", err)
	}
	if _, err := value.ApplyFill(Fill{
		ID:       "fill-1",
		Quantity: shared.MustDecimal("0.5"),
	}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("conflicting duplicate error = %v", err)
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

func TestCancelRejectedRestoresExecutableState(t *testing.T) {
	value, _, _ := New("order-1", validSpec())
	_, _ = value.BeginSubmit()
	_, _ = value.Acknowledge("exchange-1")
	_, _ = value.ApplyFill(Fill{
		ID: "fill-before-cancel", Quantity: shared.MustDecimal("0.2"),
	})
	_, _ = value.BeginCancel()
	if _, err := value.CancelRejected(); err != nil {
		t.Fatalf("CancelRejected() error = %v", err)
	}
	if value.State != PartiallyFilled {
		t.Fatalf("state = %s", value.State)
	}
}

func TestAcknowledgementCanFollowFillRace(t *testing.T) {
	value, _, _ := New("order-1", validSpec())
	_, _ = value.BeginSubmit()
	_, _ = value.ApplyFill(Fill{
		ID: "fill-before-ack", Quantity: shared.MustDecimal("1"),
	})
	if _, err := value.Acknowledge("exchange-1"); err != nil {
		t.Fatalf("Acknowledge() error = %v", err)
	}
	if value.State != Filled || value.ExchangeOrderID != "exchange-1" {
		t.Fatalf("order = %+v", value)
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
