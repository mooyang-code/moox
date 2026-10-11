package rpc

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestKeyedTaskLockerWaitHonorsCancellationAndReleasesReference(t *testing.T) {
	var locker keyedTaskLocker
	unlock, err := locker.lock(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := locker.lock(ctx, "k"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("等待已被占用的任务锁应按期限返回: %v", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatalf("等待锁未响应取消：%s", time.Since(started))
	}
	unlock()
	if len(locker.locks) != 0 {
		t.Fatalf("锁引用未回收: %d", len(locker.locks))
	}
	// 释放后可以再次获取
	again, err := locker.lock(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	again()
}

func TestCreateTaskMutexWaitHonorsCancellation(t *testing.T) {
	var mu ctxMutex
	unlock, err := mu.lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := mu.lock(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}
