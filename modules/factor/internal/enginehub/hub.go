// Package enginehub is moox-factor-mgr's side of the compute engine contract:
// it holds the single-engine lease, keeps the latest heartbeat, assembles the
// hashed catalog snapshot and leases recalc jobs to the engine.
package enginehub

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"trpc.group/trpc-go/trpc-go/log"
)

// ErrLeaseConflict means another engine holds the live engine lease.
var ErrLeaseConflict = errors.New("factor engine lease is held by another engine")

// Store is the catalog and job storage the hub reads and leases from.
type Store interface {
	ListTasks(ctx context.Context) ([]domain.FactorTask, error)
	GetTask(ctx context.Context, taskID string) (domain.FactorTask, error)
	ListMembers(ctx context.Context, taskID, status string) ([]domain.TaskMember, error)
	PullRecalcJob(ctx context.Context, engineID string, now time.Time, ttl time.Duration, skipTasks map[string]bool, maxRunning int) (store.RecalcJob, bool, error)
	ReportRecalcProgress(ctx context.Context, jobID, leaseToken string, progress int64, status, errText string, now time.Time, ttl time.Duration) (store.RecalcJob, error)
	GetRecalcJob(ctx context.Context, jobID string) (store.RecalcJob, error)
}

const (
	DefaultEngineLeaseTTL = 45 * time.Second
	DefaultJobLeaseTTL    = 15 * time.Minute
	// DefaultMaxRunningJobs 同时只跑一个补算作业：补算整窗口读取源数据，DataNode 通常只有几个核，
	// 并发的回填会让在线计算也读不动（2026-10-14 两个 1h 因子任务同时回填把 storage-node 打到 330% CPU）。
	DefaultMaxRunningJobs = 1
)

type Option func(*Hub)

// WithEngineLeaseTTL sets how long a heartbeat keeps an engine online and
// holding the lease.
func WithEngineLeaseTTL(ttl time.Duration) Option { return func(h *Hub) { h.engineTTL = ttl } }

// WithJobLeaseTTL sets how long a pulled recalc job stays leased without a
// progress report; it must exceed the duration of one recalc chunk.
func WithJobLeaseTTL(ttl time.Duration) Option { return func(h *Hub) { h.jobTTL = ttl } }

// WithMaxRunningJobs 设置同时运行的补算作业上限（所有引擎合计），0 表示不限制。
func WithMaxRunningJobs(n int) Option { return func(h *Hub) { h.maxRunningJobs = n } }

type Hub struct {
	store     Store
	engineTTL time.Duration
	jobTTL    time.Duration
	now       func() time.Time
	// maxRunningJobs 是同时运行的补算作业上限。补算按整窗口读取源数据，同时跑的任务多了会把 Storage 打满。
	maxRunningJobs int

	mu     sync.Mutex
	ready  map[string]bool
	engine *engineState
	// onHeartbeat 在每次心跳被接受后调用（锁外）：因子管理服务据此立刻刷新因子数据集指标并补报。
	onHeartbeat func()
	// heartbeats 保存最近一次心跳（离线告警用），persisted 是最近一次落库的内容。
	heartbeats HeartbeatStore
	persisted  store.EngineHeartbeat
}

// OnHeartbeat 登记每次心跳被接受后调用的函数。
func (h *Hub) OnHeartbeat(hook func()) {
	h.mu.Lock()
	h.onHeartbeat = hook
	h.mu.Unlock()
}

type engineState struct {
	identity domain.EngineIdentity
	status   domain.EngineStatus
	lastSeen time.Time
}

func New(db Store, options ...Option) *Hub {
	h := &Hub{store: db, engineTTL: DefaultEngineLeaseTTL, jobTTL: DefaultJobLeaseTTL, maxRunningJobs: DefaultMaxRunningJobs, now: time.Now, ready: make(map[string]bool)}
	for _, option := range options {
		if option != nil {
			option(h)
		}
	}
	return h
}

// Heartbeat records the engine's status and renews its lease. While another
// engine's lease is live the heartbeat is rejected with ErrLeaseConflict.
func (h *Hub) Heartbeat(ctx context.Context, id domain.EngineIdentity, status domain.EngineStatus) (time.Duration, error) {
	if id.EngineID == "" {
		return 0, errors.New("engine_id is required")
	}
	now := h.now().UTC()
	h.mu.Lock()
	// A live lease is held by one process: another engine id, or the same id
	// from another boot (a copied config), is rejected until the lease lapses.
	if h.engine != nil && now.Before(h.engine.lastSeen.Add(h.engineTTL)) &&
		(h.engine.identity.EngineID != id.EngineID || h.engine.identity.BootID != id.BootID) {
		h.mu.Unlock()
		return 0, fmt.Errorf("%w: %s (boot %s)", ErrLeaseConflict, h.engine.identity.EngineID, h.engine.identity.BootID)
	}
	h.engine = &engineState{identity: id, status: status, lastSeen: now}
	hook, ttl := h.onHeartbeat, h.engineTTL
	h.mu.Unlock()
	if err := h.persistHeartbeat(ctx, id, now); err != nil {
		log.WarnContextf(ctx, "factor engine heartbeat not persisted: %v", err)
	}
	if hook != nil {
		hook()
	}
	return ttl, nil
}

// SetResultReady records whether a set's result dataset accepted its columns
// in the latest reconciliation; the engine only computes ready sets.
func (h *Hub) SetResultReady(taskID string, ready bool) {
	h.mu.Lock()
	h.ready[taskID] = ready
	h.mu.Unlock()
}

// Engine returns the manager's view of the lease holder and its last status,
// with run lags recomputed against the manager clock. ok is false before the
// first heartbeat.
func (h *Hub) Engine(ctx context.Context) (domain.EngineInfo, domain.EngineStatus, bool, error) {
	now := h.now().UTC()
	h.mu.Lock()
	state := h.engine
	h.mu.Unlock()
	if state == nil {
		return domain.EngineInfo{}, domain.EngineStatus{}, false, nil
	}
	hash, _, _, err := h.Snapshot(ctx, "")
	if err != nil {
		return domain.EngineInfo{}, domain.EngineStatus{}, false, err
	}
	status := state.status
	status.RecentRuns = withLag(status.RecentRuns, now)
	info := domain.EngineInfo{
		EngineIdentity: state.identity, Online: now.Before(state.lastSeen.Add(h.engineTTL)),
		LastHeartbeatAt: state.lastSeen, CatalogHash: status.CatalogHash, CatalogSyncedAt: status.CatalogSyncedAt,
		CatalogInSync: status.CatalogHash != "" && status.CatalogHash == hash,
	}
	return info, status, true, nil
}

// LatestRun returns the engine's most recent live period of one set.
func (h *Hub) LatestRun(taskID string) domain.TaskRunSummary {
	now := h.now().UTC()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.engine != nil {
		for _, run := range withLag(h.engine.status.RecentRuns, now) {
			if run.TaskID == taskID {
				return run
			}
		}
	}
	return domain.TaskRunSummary{TaskID: taskID}
}

func withLag(runs []domain.TaskRunSummary, now time.Time) []domain.TaskRunSummary {
	out := make([]domain.TaskRunSummary, len(runs))
	for i, run := range runs {
		run.LagSeconds = max(0, now.Unix()-run.LastPeriodTime)
		out[i] = run
	}
	return out
}

// holdsLease 要求 engine_id 与 boot_id 都和当前持有租约的进程一致：同一 engine_id 的新进程接管后，旧进程的任务请求
// （心跳之外的 Pull/Report）不能再续租或完成任务，与心跳的实例隔离一致。
func (h *Hub) holdsLease(id domain.EngineIdentity, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.engine != nil && h.engine.identity.EngineID == id.EngineID && h.engine.identity.BootID == id.BootID &&
		now.Before(h.engine.lastSeen.Add(h.engineTTL))
}

// HoldsLease 报告 id 是否是当前持有引擎租约的进程（冻结参数等只允许持有租约的引擎调用）。
func (h *Hub) HoldsLease(id domain.EngineIdentity) bool { return h.holdsLease(id, h.now().UTC()) }

func (h *Hub) isReady(taskID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ready[taskID]
}
