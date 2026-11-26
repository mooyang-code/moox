package setlock

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// 锁顺序：全局自定义算子锁（LockUdfContext）→ 定义锁（LockFactorContext）→ 补算锁（LockRecalcContext）→ 任务锁；
// 定义锁总是先于任何任务锁，多个任务锁按 task_id 升序取。

// Locks 是进程内的一组锁。任务锁串行化同一任务的目录修改（factor-mgr）、实时周期与连续水位追赶（引擎）；
// 补算锁串行化同一任务写历史周期的后台路径（补算作业、缺口恢复、失效重算），它们写到水位之后的周期时再取任务锁。
// 每把锁是一个单槽通道：等待者按到达顺序拿到锁，释放后立刻重取的一方不会饿死已经在等的一方。
type Locks struct {
	mu    sync.Mutex
	locks map[string]chan struct{}
}

// New 返回一组进程内的任务锁。
func New() *Locks { return &Locks{} }

// Lock 取 taskID 的锁并返回释放函数。
func (l *Locks) Lock(taskID string) func() {
	unlock, err := l.LockContext(context.Background(), taskID)
	if err != nil {
		panic(err)
	}
	return unlock
}

// LockContext 取 taskID 的进程内锁，ctx 结束时放弃等待。
func (l *Locks) LockContext(ctx context.Context, taskID string) (func(), error) {
	if l == nil {
		return func() {}, nil
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, errors.New("factor task id is required for locking")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	l.mu.Lock()
	if l.locks == nil {
		l.locks = make(map[string]chan struct{})
	}
	slot := l.locks[taskID]
	if slot == nil {
		slot = make(chan struct{}, 1)
		l.locks[taskID] = slot
	}
	l.mu.Unlock()
	select {
	case slot <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var once sync.Once
	return func() { once.Do(func() { <-slot }) }, nil
}

// LockFactorContext 取一个定义的锁。键带 "factor:" 前缀，不会与任务锁冲突。
func (l *Locks) LockFactorContext(ctx context.Context, factorID string) (func(), error) {
	factorID = strings.TrimSpace(factorID)
	if factorID == "" {
		return nil, errors.New("factor id is required for locking")
	}
	return l.LockContext(ctx, factorLockPrefix+factorID)
}

// LockRecalcContext 取一个任务的补算锁。键带 "recalc:" 前缀，与同名的任务锁互不阻塞。
func (l *Locks) LockRecalcContext(ctx context.Context, taskID string) (func(), error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, errors.New("factor task id is required for locking")
	}
	return l.LockContext(ctx, recalcLockPrefix+taskID)
}

// LockUdfContext 取全局的自定义算子锁（单用户，不按名称细分）。锁顺序：自定义算子锁 → 定义锁 → 任务锁。
// 发布、切换启用版本，以及新旧定义文本任一方引用自定义算子的创建、修改定义都先取它。
func (l *Locks) LockUdfContext(ctx context.Context) (func(), error) {
	return l.LockContext(ctx, udfLockKey)
}

const (
	factorLockPrefix = "factor:"
	recalcLockPrefix = "recalc:"
	udfLockKey       = "udf:global"
)
