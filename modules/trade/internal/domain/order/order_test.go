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
