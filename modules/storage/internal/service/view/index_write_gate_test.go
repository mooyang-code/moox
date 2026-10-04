package view

import (
	"context"
	"testing"
	"time"
)

func TestIndexWriteGateSerializesOneIndexAndAllowsDifferentIndex(t *testing.T) {
	gateA := newIndexWriteGate()
	releaseA, err := gateA.lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer releaseA()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := gateA.lock(ctx); err == nil {
		t.Fatal("second writer acquired the same index gate")
	}

	gateB := newIndexWriteGate()
	releaseB, err := gateB.lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	releaseB()
}
