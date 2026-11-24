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
		// 定义按当前启用的自定义算子版本下发：切换版本与改写定义都要求受影响任务没有已经开始的作业，
		// 所以一个作业从头到尾用的是同一份定义。
		set, err := h.runnableTask(ctx, job)
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

// Report 记录已领取作业的一个分块并返回作业的当前状态；status 为 accepted 时是引擎归还作业（回到待领取）。
// 只有当前登记的引擎进程可以上报：被重启后的新进程顶替的旧进程不能再给作业续租。
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
			if _, runnableErr := h.runnableTask(ctx, job); runnableErr != nil {
				return h.store.ReportRecalcProgress(ctx, jobID, leaseToken, progress.Unix(), store.RecalcStatusFailed, runnableErr.Error(), nil, now, h.jobTTL)
			}
		}
	}
	return h.store.ReportRecalcProgress(ctx, jobID, leaseToken, progress.Unix(), status, errText, defaultFilled, now, h.jobTTL)
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
