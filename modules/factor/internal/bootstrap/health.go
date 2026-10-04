package bootstrap

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
	lanes              map[string]time.Time
}

type HealthStatus struct {
	Healthy bool
	Reasons []string
}

func NewHealth(periodBudgetMax time.Duration) *Health {
	if periodBudgetMax <= 0 {
		periodBudgetMax = 15 * time.Minute
	}
	return &Health{periodBudgetMax: periodBudgetMax, dependencies: make(map[string]bool), lanes: make(map[string]time.Time)}
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

func (h *Health) StartLane(setID string, startedAt time.Time) {
	if h == nil || strings.TrimSpace(setID) == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lanes[strings.TrimSpace(setID)] = startedAt
}

func (h *Health) EndLane(setID string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.lanes, strings.TrimSpace(setID))
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
	for setID, startedAt := range h.lanes {
		if now.Sub(startedAt) > 2*h.periodBudgetMax {
			status.Healthy = false
			status.Reasons = append(status.Reasons, "factor set lane "+setID+" is stuck")
		}
	}
	sort.Strings(status.Reasons)
	return status
}
