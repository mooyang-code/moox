package catalogsync

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestReconcileJobScheduleAndRetry(t *testing.T) {
	now := time.Unix(100, 0)
	calls := 0
	failure := errors.New("unavailable")
	job, err := NewReconcileJob(30*time.Second, time.Second, func() time.Time { return now }, func(ctx context.Context) (SyncResult, error) {
		calls++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("missing reconciliation deadline")
		}
		if calls == 1 {
			return SyncResult{}, failure
		}
		return SyncResult{Revision: 2, Applied: true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := job.Handle(context.Background()); err != nil || calls != 0 {
		t.Fatalf("startup: calls=%d err=%v", calls, err)
	}
	now = now.Add(30 * time.Second)
	if err := job.Handle(context.Background()); !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("first: calls=%d err=%v", calls, err)
	}
	if err := job.Handle(context.Background()); err != nil || calls != 1 {
		t.Fatalf("throttle: calls=%d err=%v", calls, err)
	}
	now = now.Add(30 * time.Second)
	if err := job.Handle(context.Background()); err != nil || calls != 2 {
		t.Fatalf("retry: calls=%d err=%v", calls, err)
	}
}

func TestReconcileJobTimeout(t *testing.T) {
	now := time.Now()
	job, err := NewReconcileJob(time.Second, time.Millisecond, func() time.Time { return now }, func(ctx context.Context) (SyncResult, error) {
		<-ctx.Done()
		return SyncResult{}, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if err := job.Handle(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
}
