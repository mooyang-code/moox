package pebble

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// 缓存回写必须与写入的提交互斥：扫描的 revision 已经过期（期间有事件提交）时不能用“空队列”覆盖；
// 写入正持有 outboxMu 时，回写要等它结束再复核。
func TestOutboxHintWritebackDoesNotOverwriteConcurrentCommit(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// 1) revision 已过期：不回写。
	stale := store.outboxRevision.Load()
	store.outboxMu.Lock()
	store.noteOutboxCommitted(1, time.Now())
	store.outboxMu.Unlock()
	applied := false
	store.applyOutboxHintIfUnchanged(stale, func() { applied = true })
	if applied {
		t.Fatal("过期的 revision 不应回写缓存")
	}

	// 2) 写入持有 outboxMu 期间发起的回写会等待，写入提交后复核发现 revision 已变，不回写。
	revision := store.outboxRevision.Load()
	store.outboxMu.Lock()
	done := make(chan struct{})
	var wrote bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		store.applyOutboxHintIfUnchanged(revision, func() { wrote = true })
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("回写没有等待正在提交的写入")
	case <-time.After(50 * time.Millisecond):
	}
	store.noteOutboxCommitted(1, time.Now()) // 写入在锁内提交并推进 revision
	store.outboxMu.Unlock()
	wg.Wait()
	if wrote {
		t.Fatal("写入提交后复核应发现 revision 已变，不回写")
	}
	if pending, _, _, _ := store.OutboxPendingHint(); pending != 2 {
		t.Fatalf("两次提交的计数应保留：%d", pending)
	}
}
