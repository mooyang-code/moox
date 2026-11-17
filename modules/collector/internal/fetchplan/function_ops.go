package fetchplan

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/cloud"
)

// 改云上状态的操作只在这里进出：计划推进、读回修复、代码包升级、出口去重、补采函数、暂停与恢复、RPC 删除都起一个操作，
// cloud.Functions 的写方法（Ensure、UpgradeCode、SetTimer、Delete）只在操作的协程里调用。
// 每个函数同一时刻只有一个操作；操作有自己的上下文（时限 cloud.OperationTimeout），不跟随对账一轮的超时，
// 一轮里起完操作后最多等到本轮剩余时间减 5 秒，几秒就能做完的操作当轮就能确认。
// 操作表只在内存里：进程重启即空，没确认的函数下一轮重新 Ensure，Ensure 先读云上状态，做完的部分不会重做。

// 操作的用途（functionOp.key 的前缀）。计划推进的 key 还带计划号与阶段，见 planOpKey。
const (
	opKeyRepair    = "repair"
	opKeyUpgrade   = "upgrade"
	opKeyEgress    = "egress"
	opKeyBackfill  = "backfill"
	opKeyDelete    = "delete"
	opKeyDisable   = "disable"
	opKeyEnable    = "enable"
	opKeyUnplanned = "unplanned"
)

var (
	// errOpBusy 表示这个函数已经有一个操作在跑。
	errOpBusy = errors.New("这个函数已经有一个云端操作在跑")
	// errOpPausing 表示空间正在暂停，不再起可能打开定时器的操作。
	errOpPausing = errors.New("空间正在暂停，不起可能打开定时器的操作")
	// errOpsClosed 表示进程正在退出。
	errOpsClosed = errors.New("Collector 正在退出，不再起云端操作")
)

// functionOps 记录每个函数当前在跑的那一个云端操作，以及结束后还没被取走的结果。
// 键是函数在云上的位置（地域/命名空间/函数名，见 refKey）：同名函数在别的命名空间里的副本是另一个函数。
type functionOps struct {
	mu      sync.Mutex
	running map[string]*functionOp
	// slots 是各空间共用的并发上限（scf_plan.max_concurrent_operations）；为空表示不限。
	slots chan struct{}
	// pausing 报告空间是否正在暂停：正在暂停时拒绝起可能打开定时器的操作。
	pausing func() bool
	closed  bool
	wg      sync.WaitGroup
	now     func() time.Time
}

// functionOp 是一个函数上的一个云端操作。
type functionOp struct {
	name string
	key  string
	// timerCapable 表示这个操作可能把定时器打开：定时函数的 Ensure、启用定时器、升级失败后退回 Ensure、出口去重的重建。
	// 暂停时只拒绝、取消和等待这一类；补采函数、删除、停用定时器照常。
	timerCapable bool
	startedAt    time.Time
	cancel       context.CancelFunc
	done         chan struct{}
	// holding 表示已经拿到并发名额、开始碰云端；没拿到之前取消不会留下任何云端改动。受 functionOps.mu 保护。
	holding bool
	// 以下在 done 关闭之后才读。
	err error
	// packageID 是操作开始时现取的代码包（补采函数的就绪缓存与读回缓存用）。
	packageID string
	// timerSuppressed 为 true 表示 Ensure 因为空间正在暂停把定时器设成了停用：这次成功不能算本阶段的确认。
	timerSuppressed atomic.Bool
}

func (op *functionOp) finished() bool {
	select {
	case <-op.done:
		return true
	default:
		return false
	}
}

// confirmed 报告操作已经成功结束，并且定时器没有因为暂停被改成停用。
func (op *functionOp) confirmed() bool {
	return op.err == nil && !op.timerSuppressed.Load()
}

// refKey 是函数在操作表与读回缓存里的键：地域/命名空间/函数名。
func refKey(ref cloud.FunctionRef) string {
	return ref.Region + "/" + ref.Namespace + "/" + ref.Name
}

func newFunctionOps(slots chan struct{}, pausing func() bool, now func() time.Time) *functionOps {
	return &functionOps{running: map[string]*functionOp{}, slots: slots, pausing: pausing, now: now}
}

// start 为函数起一个操作。这个函数已经有操作在跑时返回 errOpBusy；空间正在暂停且操作可能打开定时器时返回 errOpPausing。
// 上一次已经结束、没被取走的结果直接丢掉。run 在操作自己的协程里执行，拿到并发名额之后才开始。
func (o *functionOps) start(name, key string, timerCapable bool, run func(ctx context.Context, op *functionOp) error) (*functionOp, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil, errOpsClosed
	}
	if current := o.running[name]; current != nil && !current.finished() {
		return nil, errOpBusy
	}
	if timerCapable && o.pausing != nil && o.pausing() {
		return nil, errOpPausing
	}
	ctx, cancel := context.WithTimeout(context.Background(), cloud.OperationTimeout)
	op := &functionOp{name: name, key: key, timerCapable: timerCapable, startedAt: o.now(), cancel: cancel, done: make(chan struct{})}
	o.running[name] = op
	o.wg.Add(1)
	go o.run(ctx, op, run)
	return op, nil
}

func (o *functionOps) run(ctx context.Context, op *functionOp, run func(context.Context, *functionOp) error) {
	defer o.wg.Done()
	defer close(op.done)
	defer op.cancel()
	if o.slots != nil {
		select {
		case o.slots <- struct{}{}:
			defer func() { <-o.slots }()
		case <-ctx.Done():
			op.err = ctx.Err()
			return
		}
	}
	o.mu.Lock()
	if err := ctx.Err(); err != nil {
		// 拿到名额之前已被取消（暂停取消排队的操作、进程退出）：不碰云端。
		o.mu.Unlock()
		op.err = err
		return
	}
	op.holding = true
	o.mu.Unlock()
	op.err = run(ctx, op)
}

// current 返回函数当前的操作（在跑的，或已结束还没被取走的）；没有时返回 nil。
func (o *functionOps) current(name string) *functionOp {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.running[name]
}

// busy 报告函数有操作在跑。
func (o *functionOps) busy(name string) bool {
	op := o.current(name)
	return op != nil && !op.finished()
}

// take 取走函数上已经结束、用途是 key 的操作结果；没有这样的结果时返回 nil。
func (o *functionOps) take(name, key string) *functionOp {
	o.mu.Lock()
	defer o.mu.Unlock()
	op := o.running[name]
	if op == nil || op.key != key || !op.finished() {
		return nil
	}
	delete(o.running, name)
	return op
}

// takeFinished 取走用途在 keys 里、已经结束的全部操作结果（跨轮才结束、当轮没被取走的那些）。
func (o *functionOps) takeFinished(keys ...string) []*functionOp {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []*functionOp
	for name, op := range o.running {
		if !op.finished() {
			continue
		}
		for _, key := range keys {
			if op.key == key {
				out = append(out, op)
				delete(o.running, name)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// wait 等这些操作结束，最多等 budget；返回时可能还有没结束的。
func (o *functionOps) wait(ctx context.Context, ops []*functionOp, budget time.Duration) {
	if len(ops) == 0 || budget <= 0 {
		return
	}
	timer := time.NewTimer(budget)
	defer timer.Stop()
	for _, op := range ops {
		select {
		case <-op.done:
		case <-timer.C:
			return
		case <-ctx.Done():
			return
		}
	}
}

// quiesceTimerCapable 是暂停的第 2～4 步：取消还没拿到并发名额的、可能打开定时器的操作并等它们退出；
// 已经在跑的不取消（避免留下建到一半的函数），返回还在跑的个数；已经结束的这类操作的结果一并清掉，免得恢复后被当成某个阶段的确认。
func (o *functionOps) quiesceTimerCapable() int {
	o.mu.Lock()
	var queued []*functionOp
	for _, op := range o.running {
		if op.timerCapable && !op.finished() && !op.holding {
			op.cancel()
			queued = append(queued, op)
		}
	}
	o.mu.Unlock()
	for _, op := range queued {
		<-op.done
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	running := 0
	for name, op := range o.running {
		if !op.timerCapable {
			continue
		}
		if op.finished() {
			delete(o.running, name)
			continue
		}
		running++
	}
	return running
}

// oldestRunning 返回最早一个在跑的操作的开始时间。
func (o *functionOps) oldestRunning() (time.Time, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	var since time.Time
	found := false
	for _, op := range o.running {
		if op.finished() {
			continue
		}
		if !found || op.startedAt.Before(since) {
			since, found = op.startedAt, true
		}
	}
	return since, found
}

// runningKeys 返回在跑的操作：函数名 → 用途。
func (o *functionOps) runningKeys() map[string]string {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := map[string]string{}
	for name, op := range o.running {
		if !op.finished() {
			out[name] = op.key
		}
	}
	return out
}

// cancelAll 取消全部在跑的操作，不等它们返回（测试结束时用）。
func (o *functionOps) cancelAll() {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, op := range o.running {
		op.cancel()
	}
}

// stop 取消全部操作并等它们返回。close 为 true（进程退出）时之后不再接受新操作；
// 为 false（空间的对账协程退出）时操作表照常可用：暂停期间补采函数与 RPC 删除还要用它。
func (o *functionOps) stop(close bool) {
	o.mu.Lock()
	if close {
		o.closed = true
	}
	ops := make([]*functionOp, 0, len(o.running))
	for _, op := range o.running {
		ops = append(ops, op)
	}
	o.mu.Unlock()
	sort.Slice(ops, func(i, j int) bool { return ops[i].name < ops[j].name })
	for _, op := range ops {
		op.cancel()
	}
	for _, op := range ops {
		<-op.done
	}
	if close {
		o.wg.Wait()
	}
}
