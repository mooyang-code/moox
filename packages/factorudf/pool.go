package factorudf

import (
	"context"
	"sync"
	"time"
)

// pool 是一个模块的实例池：借出、用完归还；总数不超过上限；空闲超过时限的实例关闭。
// 超过上限时借出方等待归还（或上下文结束）。
type pool struct {
	m *Module

	mu     sync.Mutex
	idle   []*Instance
	total  int // 已创建且未关闭的实例数（含借出的）
	notify chan struct{}
	closed bool
}

func newPool(m *Module) *pool { return &pool{m: m, notify: make(chan struct{})} }

// Get 借出一个实例；池里没有空闲实例且未到上限时新建。
func (p *pool) Get(ctx context.Context) (*Instance, error) {
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, errClosed
		}
		p.reapLocked(ctx, time.Now())
		if n := len(p.idle); n > 0 {
			in := p.idle[n-1]
			p.idle = p.idle[:n-1]
			p.mu.Unlock()
			return in, nil
		}
		if p.total < p.m.rt.limits.PoolSize {
			p.total++
			p.mu.Unlock()
			in, err := p.m.instantiate(ctx)
			if err != nil {
				p.mu.Lock()
				p.total--
				p.wakeLocked()
				p.mu.Unlock()
				return nil, err
			}
			return in, nil
		}
		wait := p.notify
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wait:
		}
	}
}

// Put 归还一个健康的实例。
func (p *pool) Put(ctx context.Context, in *Instance) {
	in.lastUsed = time.Now()
	p.mu.Lock()
	if p.closed {
		p.total--
		p.mu.Unlock()
		in.close(ctx)
		return
	}
	p.idle = append(p.idle, in)
	p.wakeLocked()
	p.mu.Unlock()
}

// Discard 丢弃出错的实例（关闭并释放预算）。
func (p *pool) Discard(ctx context.Context, in *Instance) {
	in.close(ctx)
	p.mu.Lock()
	p.total--
	p.wakeLocked()
	p.mu.Unlock()
}

// Reap 关闭空闲超时的实例，返回关闭的个数；持有者可以定时调用，不调用时每次借出也会顺带清理。
func (p *pool) Reap(ctx context.Context, now time.Time) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reapLocked(ctx, now)
}

func (p *pool) reapLocked(ctx context.Context, now time.Time) int {
	limit := p.m.rt.limits.IdleTimeout
	kept := p.idle[:0]
	closed := 0
	for _, in := range p.idle {
		if now.Sub(in.lastUsed) > limit {
			in.close(ctx)
			p.total--
			closed++
			continue
		}
		kept = append(kept, in)
	}
	p.idle = kept
	return closed
}

func (p *pool) wakeLocked() {
	close(p.notify)
	p.notify = make(chan struct{})
}

func (p *pool) close(ctx context.Context) {
	p.mu.Lock()
	p.closed = true
	idle := p.idle
	p.idle = nil
	p.total -= len(idle)
	p.wakeLocked()
	p.mu.Unlock()
	for _, in := range idle {
		in.close(ctx)
	}
}

// Stats 是实例池的状态（指标与测试用）。
type PoolStats struct{ Total, Idle int }

// Stats 返回池里的实例数。
func (m *Module) Stats() PoolStats {
	m.pool.mu.Lock()
	defer m.pool.mu.Unlock()
	return PoolStats{Total: m.pool.total, Idle: len(m.pool.idle)}
}

// Reap 关闭空闲超时的实例。
func (m *Module) Reap(ctx context.Context, now time.Time) int { return m.pool.Reap(ctx, now) }

// Acquire 借出一个实例，Release 归还，Discard 丢弃。
func (m *Module) Acquire(ctx context.Context) (*Instance, error) { return m.pool.Get(ctx) }
func (m *Module) Release(ctx context.Context, in *Instance)      { m.pool.Put(ctx, in) }
func (m *Module) Discard(ctx context.Context, in *Instance)      { m.pool.Discard(ctx, in) }
