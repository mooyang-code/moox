package engine

import (
	"context"
	"testing"
	"time"
)

func TestProbeCompletesRealWorkerHandshake(t *testing.T) {
	engine, err := NewWithWorkers(context.Background(), "python3", "../../pyworker/worker.py", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.Probe(ctx); err != nil {
		t.Fatal(err)
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
