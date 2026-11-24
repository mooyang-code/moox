package setlock

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// 锁顺序：全局自定义算子锁（LockUdfContext）在前，然后是定义锁（LockFactorContext），最后是任务锁；
// 定义锁总是先于任何任务锁，多个任务锁按 task_id 升序取。

// Locks 在进程内串行化同一任务的目录修改、实时计算与补算。每把锁是一个单槽通道：等待者按到达顺序拿到锁，
// 补算在分段之间释放后立刻重取时，不会饿死已经在等的实时周期。
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

// LockUdfContext 取全局的自定义算子锁（单用户，不按名称细分）。锁顺序：自定义算子锁 → 定义锁 → 任务锁。
// 发布、切换启用版本，以及新旧定义文本任一方引用自定义算子的创建、修改定义都先取它。
func (l *Locks) LockUdfContext(ctx context.Context) (func(), error) {
	return l.LockContext(ctx, udfLockKey)
}

const (
	factorLockPrefix = "factor:"
	udfLockKey       = "udf:global"
)
