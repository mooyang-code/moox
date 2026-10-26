// Package memadmit 是进程级内存准入（设计 4.4）：常驻的窗口与缓存先从预算里扣除，计算、回放、导入、窗口换代
// 在开始前向它预留内存，预留不到就排队，等不到时拒绝。实时周期的计算优先级最高，其他任务让路。
package memadmit

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"sync"
)

// Priority 是预留的优先级：数值越大越先被满足。
type Priority int

const (
	PriorityBackground Priority = 0 // 回放、重算、导入
	PriorityRealtime   Priority = 10
)

// ErrTooLarge 表示预留量超过（预算 − 常驻），永远预留不到。
var ErrTooLarge = errors.New("内存预留量超过进程预算")

// Admitter 是准入器。预算为 0 表示不限制。
type Admitter struct {
	mu       sync.Mutex
	budget   int64
	resident map[string]int64
	reserved int64
	waiters  *list.List
	seq      uint64
}

type waiter struct {
	bytes int64
	prio  Priority
	seq   uint64
	ready chan struct{}
	done  bool
}

// New 创建准入器。
func New(budget int64) *Admitter {
	return &Admitter{budget: budget, resident: map[string]int64{}, waiters: list.New()}
}

// Stats 是准入器的当前占用。
type Stats struct {
	Budget, Resident, Reserved int64
	Waiting                    int
}

// Stats 返回当前占用。
func (a *Admitter) Stats() Stats {
	a.mu.Lock()
	defer a.mu.Unlock()
	return Stats{Budget: a.budget, Resident: a.residentLocked(), Reserved: a.reserved, Waiting: a.waiters.Len()}
}

func (a *Admitter) residentLocked() int64 {
	var total int64
	for _, v := range a.resident {
		total += v
	}
	return total
}

// SetResident 登记一项常驻内存（窗口、块缓存）；同名覆盖。常驻减少后会唤醒排队的预留。
func (a *Admitter) SetResident(name string, bytes int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if bytes <= 0 {
		delete(a.resident, name)
	} else {
		a.resident[name] = bytes
	}
	a.dispatchLocked()
}

// CanResident 判断再增加 extra 字节常驻内存之后，常驻部分是否仍在预算之内（启用新任务前检查，设计 4.2）。
func (a *Admitter) CanResident(name string, extra int64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.budget <= 0 {
		return true
	}
	return a.residentLocked()-a.resident[name]+extra <= a.budget
}

type priorityKey struct{}

// WithPriority 在 ctx 里带上预留的优先级：管线只通过 Reserve(ctx, bytes) 预留，实时周期与补算由调用方用它区分。
func WithPriority(ctx context.Context, prio Priority) context.Context {
	return context.WithValue(ctx, priorityKey{}, prio)
}

// Reserve 按 ctx 里带的优先级（没有时为后台优先级）预留 bytes 字节。
func (a *Admitter) Reserve(ctx context.Context, bytes int64) (func(), error) {
	prio, _ := ctx.Value(priorityKey{}).(Priority)
	return a.ReservePriority(ctx, bytes, prio)
}

// ReserveRealtime 以实时周期的优先级预留 bytes 字节。
func (a *Admitter) ReserveRealtime(ctx context.Context, bytes int64) (func(), error) {
	return a.ReservePriority(ctx, bytes, PriorityRealtime)
}

// ReservePriority 预留 bytes 字节，预留不到时按（优先级，到达顺序）排队，直到 ctx 结束。返回的函数释放预留，可重复调用。
func (a *Admitter) ReservePriority(ctx context.Context, bytes int64, prio Priority) (func(), error) {
	if bytes <= 0 || a.budget <= 0 {
		return func() {}, nil
	}
	a.mu.Lock()
	if bytes > a.budget-a.residentLocked() {
		free := a.budget - a.residentLocked()
		a.mu.Unlock()
		return nil, fmt.Errorf("%w：需要 %d 字节，预算 %d 字节扣除常驻后只剩 %d 字节", ErrTooLarge, bytes, a.budget, free)
	}
	if a.waiters.Len() == 0 && a.fitsLocked(bytes) {
		a.reserved += bytes
		a.mu.Unlock()
		return a.releaser(bytes), nil
	}
	a.seq++
	w := &waiter{bytes: bytes, prio: prio, seq: a.seq, ready: make(chan struct{})}
	a.insertLocked(w)
	a.mu.Unlock()
	select {
	case <-w.ready:
		return a.releaser(bytes), nil
	case <-ctx.Done():
		a.mu.Lock()
		if w.done { // 已被授予：交还给后面排队的
			a.reserved -= bytes
			a.dispatchLocked()
			a.mu.Unlock()
		} else {
			a.removeLocked(w)
			a.dispatchLocked()
			a.mu.Unlock()
		}
		return nil, fmt.Errorf("等待内存预留 %d 字节失败：%w", bytes, ctx.Err())
	}
}

func (a *Admitter) fitsLocked(bytes int64) bool {
	return a.reserved+a.residentLocked()+bytes <= a.budget
}

func (a *Admitter) releaser(bytes int64) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			a.reserved -= bytes
			a.dispatchLocked()
			a.mu.Unlock()
		})
	}
}

// insertLocked 按（优先级降序、到达顺序升序）插入。
func (a *Admitter) insertLocked(w *waiter) {
	for e := a.waiters.Front(); e != nil; e = e.Next() {
		other := e.Value.(*waiter)
		if w.prio > other.prio {
			a.waiters.InsertBefore(w, e)
			return
		}
	}
	a.waiters.PushBack(w)
}

func (a *Admitter) removeLocked(w *waiter) {
	for e := a.waiters.Front(); e != nil; e = e.Next() {
		if e.Value.(*waiter) == w {
			a.waiters.Remove(e)
			return
		}
	}
}

// dispatchLocked 按排队顺序授予预留；队首放不下就停（避免大请求被小请求一直插队饿死）。
func (a *Admitter) dispatchLocked() {
	for e := a.waiters.Front(); e != nil; e = a.waiters.Front() {
		w := e.Value.(*waiter)
		if !a.fitsLocked(w.bytes) {
			return
		}
		a.waiters.Remove(e)
		a.reserved += w.bytes
		w.done = true
		close(w.ready)
	}
}
