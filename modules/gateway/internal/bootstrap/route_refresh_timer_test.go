package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRouteRefreshJobSkipsConcurrentInvocation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	job, err := newRouteRefreshJob(func(context.Context) error {
		calls.Add(1)
		close(started)
		<-release
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan error, 1)
	go func() { firstDone <- job.Handle(context.Background()) }()
	<-started
	if err := job.Handle(context.Background()); err != nil {
		t.Fatalf("overlap Handle() = %v", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Handle() = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls.Load())
	}
}

func TestRouteRefreshJobReturnsRefreshError(t *testing.T) {
	want := errors.New("control plane unavailable")
	job, err := newRouteRefreshJob(func(context.Context) error { return want })
	if err != nil {
		t.Fatal(err)
	}
	if err := job.Handle(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Handle() = %v, want %v", err, want)
	}
}

func TestRouteRefreshJobReturnsInvocationTimeout(t *testing.T) {
	job, err := newRouteRefreshJobWithTimeout(time.Millisecond, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := job.Handle(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Handle() = %v, want deadline exceeded", err)
	}
}

func TestRouteRefreshTimerConfig(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "config", "trpc_go.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"name: " + routeRefreshTimerService,
		"ip: 127.0.0.1",
		"port: 11013",
		"network: \"*/15 * * * * *\"",
		"protocol: timer",
		"timeout: 10000",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("timer config missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "startAtOnce") {
		t.Fatalf("route refresh timer must not start at once:\n%s", text)
	}
	if strings.Contains(text, "11002") || strings.Contains(text, "11012") {
		t.Fatalf("manual HTTP ports leaked into tRPC config:\n%s", text)
	}
}
