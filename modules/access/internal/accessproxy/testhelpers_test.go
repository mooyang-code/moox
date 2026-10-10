package accessproxy

import (
	"strings"
	"sync"
)

// lockedBuffer 是并发安全的日志缓冲：子进程的输出由 exec 的拷贝协程写入，测试协程同时读取。
type lockedBuffer struct {
	mu   sync.Mutex
	text strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.String()
}
