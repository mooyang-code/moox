package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/recalcexec"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"trpc.group/trpc-go/trpc-go/log"
)

// errReportFailed marks a job abandoned because its progress could not be
// reported; the job lease expires and the job resumes from its last progress.
var errReportFailed = errors.New("recalc progress report failed")

const (
	jobStatusRunning   = "running"
	jobStatusSucceeded = "succeeded"
	jobStatusFailed    = "failed"
)

// JobClient is the manager surface of the recalc loop.
type JobClient interface {
	PullRecalcJob(ctx context.Context) (PulledJob, bool, error)
	ReportRecalcProgress(ctx context.Context, jobID, leaseToken string, progress time.Time, status, errText string) (string, error)
}

// SourceDataset reads the source dataset facts a recalc chunk needs.
type SourceDataset interface {
	DatasetColumns(ctx context.Context, spaceID, datasetID string) ([]string, error)
	ListDatasetSubjects(ctx context.Context, spaceID, datasetID string) ([]string, error)
}

type ChunkExecutor interface {
	Run(ctx context.Context, window recalcexec.Window, source recalcexec.SelectionSource, report recalcexec.ProgressFunc) error
}

// recalcLoop pulls leased jobs from the manager and runs them chunk by chunk,
// reporting every chunk (which also renews the job lease).
type recalcLoop struct {
	client   JobClient
	executor ChunkExecutor
	source   SourceDataset
	active   func() bool
	poll     time.Duration
	// params 提供补算区间的冻结参数（可为 nil：全部按默认参数）。
	params paramIntervalSource
	// history 在补算前把本地时序存储的历史延长到能覆盖补算区间加回看（可为 nil：只读已有的本地数据）。
	history historyExtender
	// now 取当前时间（测试可替换）。
	now func() time.Time
	// gateWait、gateRetry：集合还没放行（刚建立、自动 init/start 还没走完）时任务会被门禁挡住；等放行的最长时间与重试间隔，
	// 为 0 时取默认值（5 分钟、10 秒）。不等的话任务带着租约被丢下，要等租约（15 分钟）过期才会被重新领取。
	gateWait, gateRetry time.Duration
}

const (
	defaultGateWait  = 5 * time.Minute
	defaultGateRetry = 10 * time.Second
)

// historyExtender 把集合源数据每个标的的本地历史从 Storage 补到最多 bars 根（以本地最新一根往前数）。
type historyExtender func(ctx context.Context, set domain.EngineSet, subjects []string, bars int) error

// RunOnce pulls and runs at most one job; it reports whether a job was found.
func (l *recalcLoop) RunOnce(ctx context.Context) (bool, error) {
	if l.active != nil && !l.active() {
		return false, nil
	}
	job, found, err := l.client.PullRecalcJob(ctx)
	if err != nil || !found {
		return false, err
	}
	log.InfoContextf(ctx, "factor_recalc_start job_id=%s set_id=%s start=%s end=%s progress=%s", job.JobID, job.Set.Set.SetID,
		job.Window.Start.Format(time.RFC3339), job.Window.End.Format(time.RFC3339), job.Window.Progress.Format(time.RFC3339))
	err = l.runJob(ctx, &job)
	switch {
	case errors.Is(err, recalcexec.ErrGated):
		// 集合被暂停（或在恢复追赶、重置）：不报失败，任务租约到期后由放行后的引擎继续；按没有任务处理，避免立刻重新领取。
		log.InfoContextf(ctx, "factor_recalc_gated job_id=%s set_id=%s", job.JobID, job.Set.Set.SetID)
		return false, nil
	case err == nil:
		log.InfoContextf(ctx, "factor_recalc_done job_id=%s", job.JobID)
	case errors.Is(err, ErrLeaseConflict), errors.Is(err, errReportFailed):
		log.WarnContextf(ctx, "factor_recalc_abandoned job_id=%s error=%v", job.JobID, err)
	case ctx.Err() != nil:
		// Shutdown: the lease expires and the job resumes from its progress.
	default:
		log.ErrorContextf(ctx, "factor_recalc_failed job_id=%s error=%v", job.JobID, err)
		if _, reportErr := l.client.ReportRecalcProgress(ctx, job.JobID, job.LeaseToken, job.Window.Progress, jobStatusFailed, err.Error()); reportErr != nil {
			log.ErrorContextf(ctx, "factor_recalc_report_failed job_id=%s error=%v", job.JobID, reportErr)
		}
	}
	return true, nil
}

// runJob advances job.Window.Progress as chunks are reported, so a failure
// report carries the progress the manager already holds.
func (l *recalcLoop) runJob(ctx context.Context, job *PulledJob) error {
	set := job.Set
	// 补算任务自带定义，领取时现场编译（设计 8）。
	set.Members = CompileMembers(set.Members)
	l.extendHistory(ctx, job)
	lastNote := ""
	report := func(ctx context.Context, progress time.Time, outcome pipeline.Outcome) (bool, error) {
		status := jobStatusRunning
		if !progress.Before(job.Window.End) {
			status = jobStatusSucceeded
		}
		if note := recalcexec.DegradedNote(outcome); note != "" {
			lastNote = note
		}
		current, err := l.client.ReportRecalcProgress(ctx, job.JobID, job.LeaseToken, progress, status, lastNote)
		if err != nil {
			return true, fmt.Errorf("%w: %w", errReportFailed, err)
		}
		job.Window.Progress = progress
		if l.active != nil && !l.active() {
			// Another engine took the lease: stop at this chunk boundary and let
			// the job lease expire so the lease holder resumes it.
			return true, nil
		}
		return current != jobStatusRunning, nil
	}
	source := func(ctx context.Context) (recalcexec.Selection, error) {
		subjects, err := setSubjects(ctx, l.source, set, job.Subjects)
		if err != nil {
			return recalcexec.Selection{}, err
		}
		columns, err := l.source.DatasetColumns(ctx, set.Set.SpaceID, set.Set.SourceDatasetID)
		if err != nil {
			return recalcexec.Selection{}, fmt.Errorf("list source dataset columns: %w", err)
		}
		// 补算按冻结记录计算（因子动态调参执行计划 2.7）。冻结区间在每个分块持锁后重新读取：补算期间实时线程可能
		// 又冻结了新的期次，用任务开始时的旧区间会把它们当成“没有冻结记录”而错用沿用的参数。
		var intervals map[string][]domain.ParamInterval
		if l.params != nil {
			if intervals, err = l.params.Intervals(ctx, set.Set.SetID, job.Window.Start, job.Window.End); err != nil {
				return recalcexec.Selection{}, fmt.Errorf("read frozen params: %w", err)
			}
		}
		return recalcexec.Selection{Set: set.Set, Members: set.Members, Subjects: subjects, Columns: columns, Intervals: intervals}, nil
	}
	return l.runWhenOpen(ctx, func() error { return l.executor.Run(ctx, job.Window, source, report) })
}

// runWhenOpen 执行补算；被生命周期门禁挡住（集合刚建立，自动 init、start 还在路上）时在限定时间内重试，等集合放行后继续，
// 超时仍被挡住就按原样返回 ErrGated（集合被暂停等情况：不报失败，租约到期后由放行后的引擎继续）。
func (l *recalcLoop) runWhenOpen(ctx context.Context, run func() error) error {
	wait, retry := l.gateWait, l.gateRetry
	if wait <= 0 {
		wait = defaultGateWait
	}
	if retry <= 0 {
		retry = defaultGateRetry
	}
	deadline := time.Now().Add(wait)
	for {
		err := run()
		if !errors.Is(err, recalcexec.ErrGated) || !time.Now().Before(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retry):
		}
	}
}

// extendHistory 补算只读本地时序存储：补算区间（加回看）早于本地最早数据时，那些周期所有标的都“没有行”，整段按失败计算，
// 任务还会标成功。例如新建私有因子集时的初始回填（几千根）远早于只预载了回看根数的本地数据。读之前先把本地历史从 Storage 补到
// 覆盖 [区间起点 − 回看, 现在]；Storage 保留期内没有那么早的数据时有多少补多少，补不上的周期照旧按失败记录。
// 补历史失败不终止任务（只有已有的本地数据可算），记日志。
func (l *recalcLoop) extendHistory(ctx context.Context, job *PulledJob) {
	if l.history == nil {
		return
	}
	step, err := seriesstore.Step(job.Set.Set.Freq)
	if err != nil {
		return
	}
	now := time.Now
	if l.now != nil {
		now = l.now
	}
	bars := int(now().Sub(job.Window.Start)/step) + maxLookbackOf(job.Set) + 1
	if bars < 1 {
		return
	}
	log.InfoContextf(ctx, "factor_recalc_extend_history job_id=%s set_id=%s bars=%d", job.JobID, job.Set.Set.SetID, bars)
	if err := l.history(ctx, job.Set, job.Subjects, bars); err != nil {
		log.WarnContextf(ctx, "factor_recalc_extend_history_failed job_id=%s set_id=%s error=%v", job.JobID, job.Set.Set.SetID, err)
	}
}

// setSubjects resolves an empty selection the way the manager accepted it:
// an include set uses its own subjects, an all set the source dataset's.
func setSubjects(ctx context.Context, source SourceDataset, set domain.EngineSet, requested []string) ([]string, error) {
	if len(requested) > 0 {
		return requested, nil
	}
	if set.Set.SubjectMode == domain.SubjectModeInclude {
		return set.Set.Subjects, nil
	}
	subjects, err := source.ListDatasetSubjects(ctx, set.Set.SpaceID, set.Set.SourceDatasetID)
	if err != nil {
		return nil, fmt.Errorf("list source dataset subjects: %w", err)
	}
	return subjects, nil
}

func (l *recalcLoop) Run(ctx context.Context) {
	for ctx.Err() == nil {
		found, err := l.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			log.WarnContextf(ctx, "factor_recalc_pull_failed error=%v", err)
		}
		if found {
			continue
		}
		timer := time.NewTimer(l.poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
