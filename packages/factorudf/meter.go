package factorudf

import (
	"context"
	"sync"

	"github.com/tetratelabs/wazero/experimental"
)

// meter 记录一个实例的线性内存占用，并在**每次分配与增长发生的时刻**向预算申请（经 wazero 的内存分配器钩子）：
// 超预算时分配失败，guest 的 memory.grow 返回 -1，由它自己的分配器报错或 trap；而不是事后检查。
// 实例化（包括模块检查阶段的 ABI 探测）与每次调用中 guest 自己的增长都走同一条路。
type meter struct {
	budget Budget

	mu     sync.Mutex
	used   int64
	denied bool // 预算拒绝过一次分配或增长
}

func newMeter(budget Budget) *meter { return &meter{budget: budget} }

// context 返回带着这个 meter 的分配器的上下文；实例化模块时必须用它。
func (m *meter) context(ctx context.Context) context.Context {
	return experimental.WithMemoryAllocator(ctx, experimental.MemoryAllocatorFunc(func(_, max uint64) experimental.LinearMemory {
		return &accountedMemory{m: m, max: max}
	}))
}

// Used 返回当前已向预算申请的字节数。
func (m *meter) Used() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.used
}

// Denied 报告是否被预算拒绝过。
func (m *meter) Denied() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.denied
}

// resize 把已申请量调整到 want：增长先向预算申请，被拒绝返回 false 且不改变占用。
func (m *meter) resize(want int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if delta := want - m.used; delta > 0 {
		if err := m.budget.Acquire(delta); err != nil {
			m.denied = true
			return false
		}
	} else if delta < 0 {
		m.budget.Release(-delta)
	}
	m.used = want
	return true
}

// precharge 在实例化之前先按模块声明的初始内存向预算申请：wazero 在创建内存时（分配器的第一次 Reallocate）不接受失败，
// 返回 nil 会直接 panic，所以初始内存必须在这里提前准入；之后的增长才走分配器钩子，失败时 memory.grow 返回 -1。
func (m *meter) precharge(bytes int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.budget.Acquire(bytes); err != nil {
		m.denied = true
		return err
	}
	m.used += bytes
	return nil
}

// release 归还全部已申请量（实例关闭）。
func (m *meter) release() { m.resize(0) }

// accountedMemory 是带记账的线性内存：新增的页按 wasm 要求清零。
type accountedMemory struct {
	m   *meter
	max uint64
	buf []byte
}

// Reallocate 把内存调整到 size 字节；预算不允许、或超过声明的最大值时返回 nil（memory.grow 失败）。
func (a *accountedMemory) Reallocate(size uint64) []byte {
	if a.max != 0 && size > a.max {
		return nil
	}
	if size > uint64(1)<<40 || !a.m.resize(int64(size)) {
		return nil
	}
	switch {
	case size <= uint64(len(a.buf)):
		a.buf = a.buf[:size]
	case size <= uint64(cap(a.buf)):
		// 容量内扩展：这段字节从未暴露给 guest（make 时清零，缩小从不发生），仍是零。
		a.buf = a.buf[:size]
	default:
		newCap := max(size, uint64(cap(a.buf))*2)
		if a.max != 0 && newCap > a.max {
			newCap = a.max
		}
		grown := make([]byte, size, newCap)
		copy(grown, a.buf)
		a.buf = grown
	}
	return a.buf
}

// Free 在模块关闭时被调用。
func (a *accountedMemory) Free() {
	a.m.release()
	a.buf = nil
}
