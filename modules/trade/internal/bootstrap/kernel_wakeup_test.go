package bootstrap

import "testing"

func TestKernelWakeupCoalesces(t *testing.T) {
	wakeup := newKernelWakeup()
	wakeup.Wake()
	wakeup.Wake()
	if got := len(wakeup.ch); got != 1 {
		t.Fatalf("buffered wakeups = %d, want 1", got)
	}
}
