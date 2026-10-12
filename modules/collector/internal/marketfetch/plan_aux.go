package marketfetch

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"trpc.group/trpc-go/trpc-go/log"
)

// 辅助云端作业：代码升级、遗留预留清理、停用与漂移修复。它们不属于计划进度，但等待超时后作业仍在云端运行，
// 必须记住并在结束前避免对同一批函数重复提交，否则会争用节点、耗尽推进预算。
const (
	auxJobKindUpgrade = "upgrade"
	auxJobKindCleanup = "cleanup"
	auxJobKindRepair  = "repair"
	auxJobKindDisable = "disable"
)

type auxJob struct {
	id        string
	kind      string
	functions []string
	at        time.Time
}

type auxJobs struct {
	mu   sync.Mutex
	jobs map[string]auxJob
}

func (a *auxJobs) track(job auxJob) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.jobs == nil {
		a.jobs = map[string]auxJob{}
	}
	a.jobs[job.id] = job
}

func (a *auxJobs) snapshot() []auxJob {
	a.mu.Lock()
	defer a.mu.Unlock()
	jobs := make([]auxJob, 0, len(a.jobs))
	for _, job := range a.jobs {
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].id < jobs[j].id })
	return jobs
}

func (a *auxJobs) drop(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.jobs, id)
}

func (a *auxJobs) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.jobs)
}

// waitAux 等待辅助作业；超时或读取失败时作业仍可能在运行，记入在途集合。
func (r *PlanReconciler) waitAux(ctx context.Context, kind string, names []string, jobID string) ([]scfinvoker.BatchItemResult, bool, error) {
	items, terminal, err := r.waitJob(ctx, jobID)
	if err != nil || !terminal {
		r.aux.track(auxJob{id: jobID, kind: kind, functions: append([]string(nil), names...), at: r.now()})
	}
	return items, terminal, err
}

// auxJobMaxAge 是辅助作业的最长跟踪时间：CloudNode 单项执行最长 16 分钟，超过该时间作业必定已终止，
// 才能凭年龄解除互斥（同时告警）。
const auxJobMaxAge = 20 * time.Minute

// busyFunctions 返回仍有在途云端作业的函数（辅助作业与计划作业的并集），并刷新辅助作业：
// 已到终态的移除，仍在运行的续租发布租约，防止后续排队项因租约过期失败。
// settlePending 为 true 时（空间停用收尾，没有计划推进来结算）还会把已到终态或超龄的计划作业移出在途集合。
func (r *PlanReconciler) busyFunctions(ctx context.Context, settlePending bool) map[string]struct{} {
	busy := map[string]struct{}{}
	running := false
	for _, job := range r.aux.snapshot() {
		if r.now().Sub(job.at) > auxJobMaxAge {
			log.WarnContextf(ctx, "collector fetch plan auxiliary cloud job exceeded its maximum age space=%s job=%s kind=%s", r.SpaceID, job.id, job.kind)
			r.aux.drop(job.id)
			continue
		}
		if _, terminal, err := r.checkJob(ctx, job.id); err == nil && terminal {
			r.aux.drop(job.id)
			continue
		}
		running = true
		for _, name := range job.functions {
			busy[name] = struct{}{}
		}
	}
	if settlePending {
		kept := r.pending[:0]
		for _, job := range r.pending {
			if r.now().Sub(job.at) > auxJobMaxAge {
				continue
			}
			if _, terminal, err := r.checkJob(ctx, job.id); err == nil && terminal {
				continue
			}
			kept = append(kept, job)
		}
		r.pending = kept
	}
	for _, job := range r.pending {
		running = true
		for _, name := range job.functions {
			busy[name] = struct{}{}
		}
	}
	if running {
		if err := r.renewLease(ctx); err != nil {
			log.WarnContextf(ctx, "collector fetch plan renew publish lease for in-flight jobs space=%s: %v", r.SpaceID, err)
		}
	}
	return busy
}

// refreshAux 在每轮对账入口刷新在途作业：回收终态、续租；没有任何在途作业时释放租约。
func (r *PlanReconciler) refreshAux(ctx context.Context) {
	if r.aux.count() == 0 {
		return
	}
	r.busyFunctions(ctx, false)
	if r.aux.count() == 0 {
		r.releaseLease(ctx)
	}
}

// excludeBusy 去掉仍有在途云端作业的函数。
func (r *PlanReconciler) excludeBusy(ctx context.Context, names []string) []string {
	if len(names) == 0 || (r.aux.count() == 0 && len(r.pending) == 0) {
		return names
	}
	busy := r.busyFunctions(ctx, false)
	if len(busy) == 0 {
		return names
	}
	kept := make([]string, 0, len(names))
	for _, name := range names {
		if _, skip := busy[name]; !skip {
			kept = append(kept, name)
		}
	}
	return kept
}

// upgradeContext 给代码升级一个有界的等待预算：不超过 planUpgradeBudget，也不超过整轮剩余时间的三分之一，
// 其余时间留给计划推进。
func upgradeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	budget := planUpgradeBudget
	if deadline, ok := ctx.Deadline(); ok {
		if third := time.Until(deadline) / 3; third < budget {
			budget = third
		}
	}
	return context.WithTimeout(ctx, budget)
}
