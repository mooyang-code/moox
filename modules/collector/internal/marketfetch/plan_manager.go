package marketfetch

import (
	"context"
	"fmt"
	"sync"
	"time"

	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"trpc.group/trpc-go/trpc-go/log"
)

// DisableAll 停用该空间全部函数的定时器：已生效计划与未生效计划里出现过的函数都在内。
// 返回 true 表示云端已确认全部停用。
func (r *PlanReconciler) DisableAll(ctx context.Context) (bool, error) {
	if !r.mu.TryLock() {
		return false, fmt.Errorf("plan reconciliation already running")
	}
	defer r.mu.Unlock()
	names := map[string]string{}
	if applied, err := r.Store.LatestApplied(ctx, r.SpaceID); err != nil {
		return false, err
	} else if applied != nil {
		content, decodeErr := decodeFetchPlanContent(applied.Plan)
		if decodeErr != nil {
			return false, decodeErr
		}
		for name, function := range content.Functions {
			names[name] = function.Region
		}
	}
	if open, err := r.Store.Open(ctx, r.SpaceID); err != nil {
		return false, err
	} else if open != nil {
		content, decodeErr := decodeFetchPlanContent(open.Plan)
		if decodeErr != nil {
			return false, decodeErr
		}
		for name, function := range content.Functions {
			names[name] = function.Region
		}
	}
	if len(names) == 0 {
		return true, nil
	}
	release, err := r.acquireOperation(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	nodes, err := r.Cloud.ListTimerMarketFetchers(ctx, r.SpaceID)
	if err != nil {
		return false, err
	}
	var patches []*cloudnodepb.NodeRuntimeConfigPatch
	exists := map[string]scfinvoker.Node{}
	for _, node := range nodes {
		exists[node.NodeID] = node
	}
	lease, err := r.ensureLease(ctx)
	if err != nil {
		return false, err
	}
	for name := range names {
		node, ok := exists[name]
		if !ok {
			continue
		}
		if enabled, known := metadataBoolValue(node.Metadata, "timer_actual_enabled"); known && !enabled {
			continue
		}
		patches = append(patches, &cloudnodepb.NodeRuntimeConfigPatch{NodeId: name, TimerEnabled: false, TimerCron: "0 * * * * * *", CollectorPublishLeaseId: lease.LeaseID, CollectorPublishFencingToken: lease.FencingToken})
	}
	if len(patches) == 0 {
		r.releaseLease(ctx)
		return true, nil
	}
	jobID, err := r.Cloud.SubmitRuntimeConfigs(ctx, r.SpaceID, patches)
	if err != nil {
		return false, err
	}
	items, terminal, err := r.waitJob(ctx, jobID)
	if err != nil || !terminal {
		return false, err
	}
	for _, item := range items {
		if !item.Success {
			return false, fmt.Errorf("disable timer of %s: %s", item.NodeID, item.Message)
		}
	}
	r.releaseLease(ctx)
	return true, nil
}

// PlanManager 为每个启用采集计划的空间维持一个独立的对账协程，每分钟第 Offset 秒执行一轮。
// 空间不再启用时，先停用其全部函数的定时器，确认后才退出该空间的协程。
type PlanManager struct {
	// Spaces 返回当前应当运行对账的空间。
	Spaces func(ctx context.Context) ([]string, error)
	// Factory 为空间创建对账器。
	Factory  func(spaceID string) (*PlanReconciler, error)
	Interval time.Duration
	Offset   time.Duration
	// Timeout 是单轮对账的超时。
	Timeout time.Duration
	// AfterTick 在每轮对账之后调用（推进补采作业、自动补采扫描），错误只记录日志。
	AfterTick func(ctx context.Context, spaceID string) error

	mu      sync.Mutex
	workers map[string]*planWorker
	wg      sync.WaitGroup
}

type planWorker struct {
	cancel context.CancelFunc
	stop   chan struct{}
	done   chan struct{}
}

func (m *PlanManager) interval() time.Duration {
	if m.Interval > 0 {
		return m.Interval
	}
	return time.Minute
}

func (m *PlanManager) offset() time.Duration {
	if m.Offset > 0 {
		return m.Offset
	}
	return 10 * time.Second
}

func (m *PlanManager) timeout() time.Duration {
	if m.Timeout > 0 {
		return m.Timeout
	}
	return 55 * time.Second
}

// untilNext 返回距离下一个“整分钟 + Offset”的时间。
func untilNext(now time.Time, interval, offset time.Duration) time.Duration {
	base := now.Truncate(interval).Add(offset)
	if !base.After(now) {
		base = base.Add(interval)
	}
	return base.Sub(now)
}

// Run 阻塞运行直到 ctx 结束：每个周期同步一次空间集合，并等待所有协程退出。
func (m *PlanManager) Run(ctx context.Context) {
	m.sync(ctx)
	timer := time.NewTimer(untilNext(time.Now(), m.interval(), 0))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			m.wg.Wait()
			return
		case <-timer.C:
			m.sync(ctx)
			timer.Reset(untilNext(time.Now(), m.interval(), 0))
		}
	}
}

func (m *PlanManager) sync(ctx context.Context) {
	spaces, err := m.Spaces(ctx)
	if err != nil {
		log.WarnContextf(ctx, "collector fetch plan space list failed: %v", err)
		return
	}
	wanted := make(map[string]struct{}, len(spaces))
	for _, space := range spaces {
		wanted[space] = struct{}{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.workers == nil {
		m.workers = map[string]*planWorker{}
	}
	for space, worker := range m.workers {
		select {
		case <-worker.done:
			delete(m.workers, space)
			continue
		default:
		}
		if _, ok := wanted[space]; !ok {
			select {
			case worker.stop <- struct{}{}:
			default:
			}
		}
	}
	for space := range wanted {
		if _, running := m.workers[space]; running {
			continue
		}
		reconciler, err := m.Factory(space)
		if err != nil {
			log.WarnContextf(ctx, "collector fetch plan reconciler for space %s is unavailable: %v", space, err)
			continue
		}
		workerCtx, cancel := context.WithCancel(ctx)
		worker := &planWorker{cancel: cancel, stop: make(chan struct{}, 1), done: make(chan struct{})}
		m.workers[space] = worker
		m.wg.Add(1)
		go m.runSpace(workerCtx, space, reconciler, worker)
	}
}

// runSpace 是一个空间的对账协程；上一轮未结束时下一轮自然被推迟，不会重叠。
func (m *PlanManager) runSpace(ctx context.Context, space string, reconciler *PlanReconciler, worker *planWorker) {
	defer m.wg.Done()
	defer close(worker.done)
	stopping := false
	for {
		timer := time.NewTimer(untilNext(time.Now(), m.interval(), m.offset()))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-worker.stop:
			stopping = true
			timer.Stop()
		case <-timer.C:
		}
		tickCtx, cancel := context.WithTimeout(ctx, m.timeout())
		if stopping {
			done, err := reconciler.DisableAll(tickCtx)
			cancel()
			if err != nil {
				log.WarnContextf(ctx, "collector fetch plan disabling space=%s failed: %v", space, err)
				continue
			}
			if done {
				log.InfoContextf(ctx, "collector fetch plan space stopped space=%s", space)
				return
			}
			continue
		}
		if err := reconciler.Reconcile(tickCtx); err != nil {
			log.WarnContextf(ctx, "collector fetch plan reconciliation failed space=%s: %v", space, err)
		}
		if m.AfterTick != nil {
			// 补采推进与对账各用各的超时，对账等云端作业久了不会饿死补采。
			followCtx, followCancel := context.WithTimeout(ctx, 30*time.Second)
			err := m.AfterTick(followCtx, space)
			followCancel()
			if err != nil {
				log.WarnContextf(ctx, "collector fetch plan follow-up failed space=%s: %v", space, err)
			}
		}
		cancel()
	}
}
