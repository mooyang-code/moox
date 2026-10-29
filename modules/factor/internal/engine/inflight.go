package engine

import (
	"context"
	"fmt"
	"sync"
)

// inflight 登记正在写结果的操作，供暂停时等待它们结束。
//
// 协议是“先登记在途、再检查门禁”：操作先 enter 登记，再查门禁，门禁已经关闭就立刻注销并拒绝。
// 生命周期先关门禁、后调用 Quiesce，所以任何操作要么在关门之前已经登记（Quiesce 会等它），要么登记后发现门禁已关而放弃，
// 不存在“已经通过门禁但尚未登记”的空隙。
type inflight struct {
	mu    sync.Mutex
	total int
	tasks map[string]int
	idle  chan struct{} // 有等待者时存在，计数归零（总数或任务数）时关闭并重建
}

func newInflight() *inflight { return &inflight{tasks: map[string]int{}} }

// enter 登记一个在途操作；taskID 为空表示模块级操作（例如补算领取）。返回的函数注销登记，可重复调用。
func (f *inflight) enter(taskID string) func() {
	if f == nil {
		return func() {}
	}
	f.mu.Lock()
	f.total++
	if taskID != "" {
		f.tasks[taskID]++
	}
	f.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { f.leave(taskID) }) }
}

func (f *inflight) leave(taskID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.total--
	if taskID != "" {
		if f.tasks[taskID]--; f.tasks[taskID] <= 0 {
			delete(f.tasks, taskID)
		}
	}
	if f.idle != nil {
		close(f.idle)
		f.idle = nil
	}
}

// count 返回在途数；taskID 为空是全部。
func (f *inflight) count(taskID string) int {
	if f == nil {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if taskID == "" {
		return f.total
	}
	return f.tasks[taskID]
}

// wait 等待在途数归零（taskID 为空是全部在途）；ctx 先结束时返回还剩多少。
func (f *inflight) wait(ctx context.Context, taskID string) error {
	if f == nil {
		return nil
	}
	for {
		f.mu.Lock()
		n := f.total
		if taskID != "" {
			n = f.tasks[taskID]
		}
		if n == 0 {
			f.mu.Unlock()
			return nil
		}
		if f.idle == nil {
			f.idle = make(chan struct{})
		}
		idle := f.idle
		f.mu.Unlock()
		select {
		case <-ctx.Done():
			return fmt.Errorf("还有 %d 个写结果的操作在途：%w", f.count(taskID), ctx.Err())
		case <-idle:
		}
	}
}
