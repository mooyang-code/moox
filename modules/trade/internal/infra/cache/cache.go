// Package cache 是 Trade 进程内的两种内存缓存：带有效期的整份快照，和写入时失效的按键缓存。
// 都不起后台协程，不需要关闭。
package cache

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Snapshot 缓存一份整体取得的数据（例如交易所全市场报价），过了 TTL 下次读取时重新取。
// 并发读取时只取一次，其他调用方等这次结果；等待响应上下文取消。零值不可用，用 NewSnapshot 创建。
type Snapshot[T any] struct {
	ttl     time.Duration
	mu      sync.Mutex
	value   T
	at      time.Time
	loading chan struct{}
	err     error
}

// NewSnapshot 创建有效期为 ttl 的快照缓存。
func NewSnapshot[T any](ttl time.Duration) *Snapshot[T] {
	return &Snapshot[T]{ttl: ttl}
}

// Get 返回快照和取得时间。快照过期时调用 load 重新取；取失败时返回错误，不保留旧值。
// 有效期按单调时钟判断，系统时间回拨不会让旧快照一直被当作新鲜；返回的取得时间是 UTC 墙钟。
// 只有正在取的调用方自己被取消时，等待者才重新竞争一次，不把别人的取消当成自己的错误。
func (s *Snapshot[T]) Get(ctx context.Context, load func(context.Context) (T, error)) (T, time.Time, error) {
	var zero T
	for {
		s.mu.Lock()
		if s.freshLocked() {
			value, at := s.value, s.at
			s.mu.Unlock()
			return value, at.UTC(), nil
		}
		if s.loading != nil {
			// 已有调用方在取：等它的结果，取到后从头再判断一次。
			waiting := s.loading
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return zero, time.Time{}, ctx.Err()
			case <-waiting:
			}
			s.mu.Lock()
			err := s.err
			fresh := s.freshLocked()
			s.mu.Unlock()
			if err != nil && !fresh && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				return zero, time.Time{}, err
			}
			continue
		}
		done := make(chan struct{})
		s.loading = done
		s.mu.Unlock()

		value, at, err := s.load(ctx, load, done)
		if err != nil {
			return zero, time.Time{}, err
		}
		return value, at.UTC(), nil
	}
}

// load 调用一次 load 并发布结果；load panic 时同样清掉在取标记、唤醒等待者，再把 panic 抛出去。
func (s *Snapshot[T]) load(ctx context.Context, load func(context.Context) (T, error), done chan struct{}) (value T, at time.Time, err error) {
	finished := false
	defer func() {
		if !finished {
			err = errors.New("cache: snapshot load panicked")
		}
		s.mu.Lock()
		s.loading, s.err = nil, err
		if err == nil {
			s.value, s.at = value, at
		}
		s.mu.Unlock()
		close(done)
	}()
	value, err = load(ctx)
	at = time.Now()
	finished = true
	return value, at, err
}

func (s *Snapshot[T]) freshLocked() bool {
	if s.at.IsZero() {
		return false
	}
	age := time.Since(s.at)
	return age >= 0 && age < s.ttl
}

// Memo 是写入时失效的按键缓存，用于很少变化、写入点明确的数据（例如标的定义、账户身份）。
// 读取期间发生失效时不写回，避免把失效前读到的旧数据留在缓存里。零值可用。
type Memo[K comparable, V any] struct {
	mu         sync.RWMutex
	generation uint64
	values     map[K]V
}

// Get 返回 key 的值，没有缓存时调用 load。load 出错时不缓存。
func (m *Memo[K, V]) Get(key K, load func() (V, error)) (V, error) {
	m.mu.RLock()
	value, ok := m.values[key]
	generation := m.generation
	m.mu.RUnlock()
	if ok {
		return value, nil
	}
	value, err := load()
	if err != nil {
		return value, err
	}
	m.mu.Lock()
	if m.generation == generation {
		if m.values == nil {
			m.values = make(map[K]V)
		}
		m.values[key] = value
	}
	m.mu.Unlock()
	return value, nil
}

// Invalidate 清空全部缓存。
func (m *Memo[K, V]) Invalidate() {
	m.mu.Lock()
	m.generation++
	m.values = nil
	m.mu.Unlock()
}
