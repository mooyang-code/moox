package view

import (
	"testing"
	"time"
)

// 新建的运行时立即带着查询路径的无锁副本：首次构建期间整段索引写入持有运行时锁，读取 active 不能等它。
func TestNewViewRuntimePublishesReadState(t *testing.T) {
	runtime := newViewRuntime()
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	done := make(chan struct{})
	go func() {
		_ = runtime.queryState()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("读取新建运行时的查询状态等在运行时锁上")
	}
}
