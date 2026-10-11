package partitionlock

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLockWaitHonorsContextDeadline(t *testing.T) {
	locker := New()
	unlock, err := locker.Lock(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := locker.Lock(ctx, "p"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("等待被占用的分区锁应按期限返回: %v", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatalf("等待未响应取消: %s", time.Since(started))
	}
	unlock()
	again, err := locker.Lock(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	again()
	// 不同分区互不阻塞
	other, err := locker.Lock(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	other()
}
