package timerjob

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type contextKey string

func TestNewRejectsInvalidArguments(t *testing.T) {
	tests := []struct {
		name    string
		jobName string
		timeout time.Duration
		run     func(context.Context) error
	}{
		{name: "empty name", timeout: time.Second, run: func(context.Context) error { return nil }},
		{name: "zero timeout", jobName: "test", run: func(context.Context) error { return nil }},
		{name: "negative timeout", jobName: "test", timeout: -time.Second, run: func(context.Context) error { return nil }},
		{name: "nil callback", jobName: "test", timeout: time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.jobName, tt.timeout, tt.run); err == nil {
				t.Fatal("New() error = nil")
			}
		})
	}
}

func TestHandleClonesContextAndCompletesSynchronously(t *testing.T) {
	key := contextKey("trace")
	job, err := New("clone_context", time.Second, func(ctx context.Context) error {
		if got := ctx.Value(key); got != "value" {
			t.Fatalf("context value = %v, want value", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := job.Handle(context.WithValue(context.Background(), key, "value")); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if job.Running() {
		t.Fatal("Running() = true after Handle returned")
	}
}

func TestHandleSkipsOverlappingInvocation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	job, err := New("overlap", time.Second, func(context.Context) error {
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
	if !job.Running() {
		t.Fatal("Running() = false while callback is blocked")
	}
	if err := job.Handle(context.Background()); err != nil {
		t.Fatalf("overlapping Handle() error = %v", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Handle() error = %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("callback calls = %d, want 1", got)
	}
}

func TestHandleReturnsDeadlineExceeded(t *testing.T) {
	job, err := New("timeout", 10*time.Millisecond, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	err = job.Handle(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Handle() error = %v, want deadline exceeded", err)
	}
}
