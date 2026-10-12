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

// busyFunctions 返回仍有在途辅助作业的函数：先刷新各作业状态，已结束或超过最长存活的移除。
func (r *PlanReconciler) busyFunctions(ctx context.Context) map[string]struct{} {
	busy := map[string]struct{}{}
	for _, job := range r.aux.snapshot() {
		if r.now().Sub(job.at) > pendingJobMaxAge {
			log.WarnContextf(ctx, "collector fetch plan dropped a stuck auxiliary cloud job space=%s job=%s kind=%s", r.SpaceID, job.id, job.kind)
			r.aux.drop(job.id)
			continue
		}
		if _, terminal, err := r.checkJob(ctx, job.id); err == nil && terminal {
			r.aux.drop(job.id)
			continue
		}
		for _, name := range job.functions {
			busy[name] = struct{}{}
		}
	}
	return busy
}

// excludeBusy 去掉仍有在途辅助作业的函数。
func (r *PlanReconciler) excludeBusy(ctx context.Context, names []string) []string {
	if r.aux.count() == 0 || len(names) == 0 {
		return names
	}
	busy := r.busyFunctions(ctx)
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
