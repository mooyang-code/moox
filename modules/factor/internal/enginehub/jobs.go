package enginehub

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
)

// maxPullAttempts bounds how many unrunnable jobs one pull may fail before it
// gives up for this round.
const maxPullAttempts = 16

// Pull leases the next runnable recalc job to the engine holding the lease.
// Jobs of enabled sets whose result dataset is not ready are left queued; jobs
// whose set is no longer enabled or whose selected factors are no longer all
// enabled fail immediately, as they could never run.
func (h *Hub) Pull(ctx context.Context, id domain.EngineIdentity) (store.RecalcJob, domain.EngineTask, bool, error) {
	now := h.now().UTC()
	if !h.holdsLease(id, now) {
		return store.RecalcJob{}, domain.EngineTask{}, false, ErrLeaseConflict
	}
	skip, err := h.unreadyTasks(ctx)
	if err != nil {
		return store.RecalcJob{}, domain.EngineTask{}, false, err
	}
	for attempt := 0; attempt < maxPullAttempts; attempt++ {
		job, found, err := h.store.PullRecalcJob(ctx, id.EngineID, now, h.jobTTL, skip, h.maxRunningJobs)
		if err != nil || !found {
			return store.RecalcJob{}, domain.EngineTask{}, false, err
		}
		// 绑定在首次领取时固化：续跑、租约过期后重新领取、管理端重启后都用同一份，不再按当前启用版本解析。
		// 固化发生在任何分块下发之前；切换自定义算子版本要求没有已领取的作业，所以领取与固化之间不会发生切换。
		pinned, err := h.jobBindings(ctx, job)
		if err != nil {
			return store.RecalcJob{}, domain.EngineTask{}, false, err
		}
		set, err := h.runnableTask(store.WithUDFPins(ctx, pinned), job)
		if err != nil {
			if _, failErr := h.store.ReportRecalcProgress(ctx, job.JobID, job.LeaseToken, job.ProgressTime,
				store.RecalcStatusFailed, err.Error(), nil, now, h.jobTTL); failErr != nil {
				return store.RecalcJob{}, domain.EngineTask{}, false, failErr
			}
			continue
		}
		return job, set, true, nil
	}
	return store.RecalcJob{}, domain.EngineTask{}, false, nil
}

// Report records a chunk of a leased job and returns the job's current state.
// Only the engine holding the engine lease may report, so an engine that lost
// the lease cannot keep a long job alive next to the new lease holder.
func (h *Hub) Report(ctx context.Context, id domain.EngineIdentity, jobID, leaseToken string, progress time.Time, status, errText string, defaultFilled map[string][]store.TimeRange) (store.RecalcJob, error) {
	now := h.now().UTC()
	if !h.holdsLease(id, now) {
		return store.RecalcJob{}, ErrLeaseConflict
	}
	if status == store.RecalcStatusRunning {
		// 分块之间复核：停用因子成员或计算任务后，已领取的补算作业不能继续写入后续分块
		job, err := h.store.GetRecalcJob(ctx, jobID)
		if err != nil {
			return store.RecalcJob{}, err
		}
		if job.LeaseToken == leaseToken && job.Status == store.RecalcStatusRunning {
			pinned, _, bindErr := h.store.JobBindings(ctx, job.JobID)
			if bindErr != nil {
				return store.RecalcJob{}, bindErr
			}
			if _, runnableErr := h.runnableTask(store.WithUDFPins(ctx, pinned), job); runnableErr != nil {
				return h.store.ReportRecalcProgress(ctx, jobID, leaseToken, progress.Unix(), store.RecalcStatusFailed, runnableErr.Error(), nil, now, h.jobTTL)
			}
		}
	}
	return h.store.ReportRecalcProgress(ctx, jobID, leaseToken, progress.Unix(), status, errText, defaultFilled, now, h.jobTTL)
}

// jobBindings 返回作业固化的绑定；第一次领取时按当前启用版本解析并写下。
func (h *Hub) jobBindings(ctx context.Context, job store.RecalcJob) (map[string]string, error) {
	if bindings, found, err := h.store.JobBindings(ctx, job.JobID); err != nil || found {
		return bindings, err
	}
	live := map[string]string{}
	if set, err := h.runnableTask(ctx, job); err == nil {
		live = store.UDFBindingsOf(set.Members)
	}
	return h.store.SaveJobBindings(ctx, job.JobID, live)
}

func (h *Hub) runnableTask(ctx context.Context, job store.RecalcJob) (domain.EngineTask, error) {
	set, err := h.store.GetTask(ctx, job.TaskID)
	if err != nil {
		return domain.EngineTask{}, err
	}
	if set.Status != domain.TaskStatusEnabled {
		return domain.EngineTask{}, fmt.Errorf("factor task %q is not enabled", set.TaskID)
	}
	members, err := h.store.ListMembers(ctx, set.TaskID, "")
	if err != nil {
		return domain.EngineTask{}, err
	}
	selected, err := domain.SelectEnabledMembers(members, job.Members)
	if err != nil {
		return domain.EngineTask{}, err
	}
	return domain.EngineTask{Set: set, Members: selected, ResultReady: h.isReady(set.TaskID)}, nil
}

func (h *Hub) unreadyTasks(ctx context.Context) (map[string]bool, error) {
	sets, err := h.store.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	skip := make(map[string]bool)
	for _, set := range sets {
		if set.Status == domain.TaskStatusEnabled && !h.isReady(set.TaskID) {
			skip[set.TaskID] = true
		}
	}
	return skip, nil
}
