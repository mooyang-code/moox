package order

import (
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"testing"
)

func TestOrderLifecycleAndTerminalProtection(t *testing.T) {
	o, _, err := New("o1", "c1", shared.MustDecimal("10"))
	if err != nil {
		t.Fatal(err)
	}
	steps := []func() error{
		func() error { _, e := o.MarkReady(); return e },
		func() error { _, e := o.BeginSubmit(); return e },
		func() error { _, e := o.Acknowledge(); return e },
		func() error { _, e := o.ApplyFill(shared.MustDecimal("4")); return e },
		func() error { _, e := o.ApplyFill(shared.MustDecimal("6")); return e },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	if o.State != Filled || o.Version != 6 {
		t.Fatalf("order=%+v", o)
	}
	if _, err := o.BeginCancel(); err == nil {
		t.Fatal("terminal state regressed")
	}
}

func TestUnknownMustBeQueriedBeforeRetry(t *testing.T) {
	o, _, _ := New("o1", "c1", shared.MustDecimal("1"))
	o.MarkReady()
	o.BeginSubmit()
	o.MarkUnknown()
	if o.State != SubmitUnknown {
		t.Fatal(o.State)
	}
	if _, err := o.Acknowledge(); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverSubmitting_FromSubmitting_ShouldMarkUnknown(t *testing.T) {
	o, _, _ := New("o1", "c1", shared.MustDecimal("1"))
	o.MarkReady()
	o.BeginSubmit()
	_, err := o.RecoverSubmitting()
	if err != nil {
		t.Fatal(err)
	}
	if o.State != SubmitUnknown {
		t.Fatalf("state=%s", o.State)
	}
}

func TestCancelFlow_PartialFill_ShouldEndPartiallyCanceled(t *testing.T) {
	o, _, _ := New("o1", "c1", shared.MustDecimal("10"))
	o.MarkReady()
	o.BeginSubmit()
	o.Acknowledge()
	o.ApplyFill(shared.MustDecimal("3"))
	o.BeginCancel()
	o.ConfirmCancel()
	if o.State != PartiallyCanceled {
		t.Fatalf("state=%s", o.State)
	}
}

func TestReject_FromReady_ShouldSucceed(t *testing.T) {
	o, _, _ := New("o1", "c1", shared.MustDecimal("1"))
	o.MarkReady()
	if _, err := o.Reject(); err != nil {
		t.Fatal(err)
	}
	if o.State != Rejected {
		t.Fatalf("state=%s", o.State)
	}
}

func TestCancelRecoveryTransitions(t *testing.T) {
	o, _, _ := New("o1", "c1", shared.MustDecimal("10"))
	o.MarkReady()
	o.BeginSubmit()
	o.Acknowledge()
	o.BeginCancel()

	if _, err := o.RecoverCanceling(); err != nil {
		t.Fatal(err)
	}
	if o.State != CancelUnknown {
		t.Fatalf("state=%s, want %s", o.State, CancelUnknown)
	}
	if _, err := o.CancelStillOpen(); err != nil {
		t.Fatal(err)
	}
	if o.State != Open {
		t.Fatalf("state=%s, want %s", o.State, Open)
	}
}

func TestCancelFailedRestoresStateByFillQuantity(t *testing.T) {
	tests := []struct {
		name       string
		fill       string
		wantState  State
		wantFilled string
	}{
		{name: "no_fill_returns_open", fill: "0", wantState: Open, wantFilled: "0"},
		{name: "partial_fill_returns_partially_filled", fill: "3", wantState: PartiallyFilled, wantFilled: "3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, _, _ := New("o1", "c1", shared.MustDecimal("10"))
			o.MarkReady()
			o.BeginSubmit()
			o.Acknowledge()
			if tt.fill != "0" {
				if _, err := o.ApplyFill(shared.MustDecimal(tt.fill)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := o.BeginCancel(); err != nil {
				t.Fatal(err)
			}

			if _, err := o.CancelFailed(); err != nil {
				t.Fatal(err)
			}
			if o.State != tt.wantState {
				t.Fatalf("state=%s, want %s", o.State, tt.wantState)
			}
			if o.FilledQuantity.String() != tt.wantFilled {
				t.Fatalf("filled=%s, want %s", o.FilledQuantity.String(), tt.wantFilled)
			}
		})
	}
}

func TestNewRejectsInvalidInputs(t *testing.T) {
	tests := []struct {
		name     string
		id       shared.OrderID
		clientID string
		qty      shared.Decimal
	}{
		{name: "empty_order_id", id: "", clientID: "c1", qty: shared.MustDecimal("1")},
		{name: "empty_client_id", id: "o1", clientID: "", qty: shared.MustDecimal("1")},
		{name: "zero_quantity", id: "o1", clientID: "c1", qty: shared.Zero()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := New(tt.id, tt.clientID, tt.qty); err != ErrInvalidTransition {
				t.Fatalf("err=%v, want %v", err, ErrInvalidTransition)
			}
		})
	}
}
