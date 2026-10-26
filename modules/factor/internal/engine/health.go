package engine

import (
	"sort"
	"strings"
	"sync"
	"time"
)

type Health struct {
	mu                 sync.Mutex
	periodBudgetMax    time.Duration
	storageWriteErrors int
	dependencies       map[string]bool
	runners            map[string]time.Time
}

type HealthStatus struct {
	Healthy bool
	Reasons []string
}

func NewHealth(periodBudgetMax time.Duration) *Health {
	if periodBudgetMax <= 0 {
		periodBudgetMax = 15 * time.Minute
	}
	return &Health{periodBudgetMax: periodBudgetMax, dependencies: make(map[string]bool), runners: make(map[string]time.Time)}
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

func (h *Health) StartRunner(taskID string, startedAt time.Time) {
	if h == nil || strings.TrimSpace(taskID) == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.runners[strings.TrimSpace(taskID)] = startedAt
}

func (h *Health) EndRunner(taskID string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.runners, strings.TrimSpace(taskID))
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
	for taskID, startedAt := range h.runners {
		if now.Sub(startedAt) > 2*h.periodBudgetMax {
			status.Healthy = false
			status.Reasons = append(status.Reasons, "factor task runner "+taskID+" is stuck")
		}
	}
	sort.Strings(status.Reasons)
	return status
}
