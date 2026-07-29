package engine

import (
	"context"
	"testing"
	"time"
)

func TestProbeCompletesRealWorkerHandshake(t *testing.T) {
	engine, err := NewWithWorkers(context.Background(), "python3", "../../pyworker/worker.py", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.Probe(ctx); err != nil {
		t.Fatal(err)
	}
	if ready := engine.ReadyWorkers(); ready != 2 {
		t.Fatalf("ReadyWorkers() = %d, want 2", ready)
	}
}

func TestProbeRejectsWorkerWithoutHello(t *testing.T) {
	engine, err := NewWithWorkers(context.Background(), "python3", "-c", 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := engine.Probe(ctx); err == nil {
		t.Fatal("expected handshake failure")
	}
}
