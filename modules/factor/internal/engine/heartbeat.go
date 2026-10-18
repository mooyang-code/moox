package engine

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"trpc.group/trpc-go/trpc-go/log"
)

// HeartbeatClient is the manager call the heartbeat loop needs.
type HeartbeatClient interface {
	Heartbeat(ctx context.Context, status domain.EngineStatus) (time.Duration, error)
}

// leaseState tracks whether the manager told this engine that another engine
// holds the lease. Only an explicit conflict stops work: an unreachable
// manager keeps the last state, so a manager outage never stops computation.
type leaseState struct {
	mu        sync.RWMutex
	conflict  bool
	lastOK    time.Time
	lastError string
}

func (l *leaseState) Conflict() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.conflict
}

func (l *leaseState) record(err error, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case err == nil:
		l.conflict, l.lastOK, l.lastError = false, now, ""
	case errors.Is(err, ErrLeaseConflict):
		l.conflict, l.lastError = true, err.Error()
	default:
		l.lastError = err.Error()
	}
}

type leaseSnapshot struct {
	Conflict  bool
	LastOK    time.Time
	LastError string
}

func (l *leaseState) snapshot() leaseSnapshot {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return leaseSnapshot{Conflict: l.conflict, LastOK: l.lastOK, LastError: l.lastError}
}

type heartbeater struct {
	client   HeartbeatClient
	lease    *leaseState
	status   func() domain.EngineStatus
	interval time.Duration
	onChange func()
	now      func() time.Time
	// wake 让心跳循环不等定时器立刻发一次（容量 1，多次唤醒合并）：每算完一期就唤醒，
	// 因子管理服务和 Monitor 因此约 1 秒内知道这一期的完成时刻，而不是最多等 10 秒的心跳间隔。
	wake chan struct{}
}

// Wake 请求尽快发一次心跳；已有待发的唤醒时合并。
func (h *heartbeater) Wake() {
	if h == nil || h.wake == nil {
		return
	}
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

func (h *heartbeater) BeatOnce(ctx context.Context) error {
	before := h.lease.Conflict()
	_, err := h.client.Heartbeat(ctx, h.status())
	h.lease.record(err, h.now().UTC())
	if err != nil {
		log.WarnContextf(ctx, "factor_engine_heartbeat_failed error=%v", err)
	}
	if h.lease.Conflict() != before && h.onChange != nil {
		h.onChange()
	}
	return err
}

func (h *heartbeater) Run(ctx context.Context) {
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-h.wake:
		}
		beatCtx, cancel := context.WithTimeout(ctx, h.interval)
		_ = h.BeatOnce(beatCtx)
		cancel()
	}
}
