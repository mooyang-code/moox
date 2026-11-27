package engine

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// RunnerHandle 是单次因子任务运行的唯一句柄：同一任务并发运行实时周期与补算分块时各自持有独立句柄。
type RunnerHandle uint64

type runnerEntry struct {
	taskID    string
	startedAt time.Time
}

type Health struct {
	mu                 sync.Mutex
	periodBudgetMax    time.Duration
	storageWriteErrors int
	dependencies       map[string]bool
	nextHandle         RunnerHandle
	runners            map[RunnerHandle]runnerEntry
}

type HealthStatus struct {
	Healthy bool
	Reasons []string
}

func NewHealth(periodBudgetMax time.Duration) *Health {
	if periodBudgetMax <= 0 {
		periodBudgetMax = 15 * time.Minute
	}
	return &Health{
		periodBudgetMax: periodBudgetMax,
		dependencies:    make(map[string]bool),
		runners:         make(map[RunnerHandle]runnerEntry),
	}
}

func (h *Health) SetDependency(name string, ready bool) {
	if h == nil || strings.TrimSpace(name) == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dependencies[strings.TrimSpace(name)] = ready
}

func (h *Health) RecordStorageWrite(success bool) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if success {
		h.storageWriteErrors = 0
		return
	}
	h.storageWriteErrors++
}

// StartRunner 记录一次因子任务运行的起始时间并返回本次运行的唯一句柄。
// 同一任务并发执行实时周期与补算分块时各自持有独立句柄，互不覆盖或误删。
func (h *Health) StartRunner(taskID string, startedAt time.Time) RunnerHandle {
	taskID = strings.TrimSpace(taskID)
	if h == nil || taskID == "" {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextHandle++
	handle := h.nextHandle
	h.runners[handle] = runnerEntry{taskID: taskID, startedAt: startedAt}
	return handle
}

// EndRunner 注销指定句柄对应的单次运行记录；零值或重复注销均安全无害。
func (h *Health) EndRunner(handle RunnerHandle) {
	if h == nil || handle == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.runners, handle)
}

func (h *Health) Check(now time.Time) HealthStatus {
	status := HealthStatus{Healthy: true}
	if h == nil {
		return HealthStatus{Reasons: []string{"health monitor is unavailable"}}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.storageWriteErrors >= 3 {
		status.Healthy = false
		status.Reasons = append(status.Reasons, "storage write failure latch is open")
	}
	for name, ready := range h.dependencies {
		if !ready {
			status.Healthy = false
			status.Reasons = append(status.Reasons, name+" is not ready")
		}
	}
	stuckTasks := make(map[string]struct{})
	for _, entry := range h.runners {
		if now.Sub(entry.startedAt) > 2*h.periodBudgetMax {
			status.Healthy = false
			stuckTasks[entry.taskID] = struct{}{}
		}
	}
	for taskID := range stuckTasks {
		status.Reasons = append(status.Reasons, "factor task runner "+taskID+" is stuck")
	}
	sort.Strings(status.Reasons)
	return status
}
