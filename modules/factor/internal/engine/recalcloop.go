package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/recalcexec"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"trpc.group/trpc-go/trpc-go/log"
)

// errReportFailed marks a job abandoned because its progress could not be
// reported; the job lease expires and the job resumes from its last progress.
var errReportFailed = errors.New("recalc progress report failed")

// giveBackTimeout 是归还作业的上报超时（引擎停止时上下文已取消，归还用单独的超时）。
const giveBackTimeout = 5 * time.Second

const (
	// jobStatusAccepted 由引擎上报时表示归还：作业回到待领取，进度保留。
	jobStatusAccepted  = "accepted"
	jobStatusRunning   = "running"
	jobStatusSucceeded = "succeeded"
	jobStatusFailed    = "failed"
)

// JobClient is the manager surface of the recalc loop.
type JobClient interface {
	PullRecalcJob(ctx context.Context) (PulledJob, bool, error)
	ReportRecalcProgress(ctx context.Context, jobID, leaseToken string, progress time.Time, status, errText string, defaultFilled map[string][]domain.TimeRange) (string, error)
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
	// prepare 在作业执行之前备齐它绑定的自定义算子模块；失败时作业报告失败，不写任何分块。
	prepare func(ctx context.Context, set domain.EngineTask) (release func(), err error)
	// leaseAllowed 非空时，模块级生命周期放行才领取作业（暂停、恢复追赶、重置期间不领取，也不续跑）。
	leaseAllowed func() bool
	// pulling 登记在途的领取请求：暂停的等待收敛要等它返回；领取回来的作业写分块时被门禁挡住，随即归还。
	pulling func() func()
	// moduleOpen 报告模块级生命周期是否放行；为空时视为一直放行。runWhenOpen 据此区分“任务刚建立、门禁稍后放行”（可以等）
	// 与“模块暂停了”（立刻放弃作业）。
	moduleOpen func() bool
	// history 在补算前把本地时序存储的历史延长到能覆盖补算区间加回看（可为 nil：只读已有的本地数据）。
	history historyExtender
	// catchUp 在补算前把本地时序存储往后拉到补算区间的终点（可为 nil）：Storage 里比本地更新的数据平时留给实时计算去拉，
	// 补算区间落在这段上时要自己先拉，否则读不到源数据。
	catchUp func(ctx context.Context, set domain.EngineTask, subjects []string, end time.Time) (int, error)
	// now 取当前时间（测试可替换）。
	now func() time.Time
	// gateWait、gateRetry：任务还没放行（刚建立、自动 init/start 还没走完）时任务会被门禁挡住；等放行的最长时间与重试间隔，
	// 为 0 时取默认值（5 分钟、10 秒）。等不到就归还作业，下一轮重新领取。
	gateWait, gateRetry time.Duration
}

const (
	defaultGateWait  = 5 * time.Minute
	defaultGateRetry = 10 * time.Second
)

// historyExtender 把任务源数据每个标的的本地历史从 Storage 补到最多 bars 根（以本地最新一根往前数）。
type historyExtender func(ctx context.Context, set domain.EngineTask, subjects []string, bars int) error

// RunOnce pulls and runs at most one job; it reports whether a job was found.
func (l *recalcLoop) RunOnce(ctx context.Context) (bool, error) {
	if l.active != nil && !l.active() {
		return false, nil
	}
	// 先登记领取在途、再检查模块门禁：登记之后门禁才关闭的，暂停的收敛会等这次领取返回；已经关闭的不领取。
	done := func() {}
	if l.pulling != nil {
		done = l.pulling()
	}
	if l.leaseAllowed != nil && !l.leaseAllowed() {
		done()
		return false, nil
	}
	job, found, err := l.client.PullRecalcJob(ctx)
	// 领取返回就注销在途登记：作业此后的写入由分块各自登记并检查门禁。
	done()
	if err != nil || !found {
		return false, err
	}
	log.InfoContextf(ctx, "factor_recalc_start job_id=%s task_id=%s start=%s end=%s progress=%s", job.JobID, job.Set.Set.TaskID,
		job.Window.Start.Format(time.RFC3339), job.Window.End.Format(time.RFC3339), job.Window.Progress.Format(time.RFC3339))
	realProgress := job.Window.Progress
	axis, err := jobToAxis(&job)
	if err != nil {
		log.ErrorContextf(ctx, "factor_recalc_failed job_id=%s error=%v", job.JobID, err)
		if _, reportErr := l.client.ReportRecalcProgress(ctx, job.JobID, job.LeaseToken, realProgress, jobStatusFailed, err.Error(), nil); reportErr != nil {
			log.ErrorContextf(ctx, "factor_recalc_report_failed job_id=%s error=%v", job.JobID, reportErr)
		}
		return true, nil
	}
	err = l.runJob(ctx, &job, axis)
	switch {
	case errors.Is(err, recalcexec.ErrGated):
		// 任务被暂停（或在恢复追赶、重置）：不报失败，归还作业，放行后重新领取并从已上报的进度续跑；
		// 按没有任务处理，等一个轮询间隔再领取。
		log.InfoContextf(ctx, "factor_recalc_gated job_id=%s task_id=%s", job.JobID, job.Set.Set.TaskID)
		l.giveBack(ctx, axis, job)
		return false, nil
	case err == nil:
		log.InfoContextf(ctx, "factor_recalc_done job_id=%s", job.JobID)
	case ctx.Err() != nil:
		// 引擎停止（排在上报失败之前：停止时正在上报的分块也会因上下文取消而失败）：归还作业，
		// 重启后立刻重新领取并从已上报的进度续跑；归还失败则等作业租约过期。
		l.giveBack(ctx, axis, job)
	case errors.Is(err, ErrLeaseConflict), errors.Is(err, errReportFailed):
		log.WarnContextf(ctx, "factor_recalc_abandoned job_id=%s error=%v", job.JobID, err)
	default:
		log.ErrorContextf(ctx, "factor_recalc_failed job_id=%s error=%v", job.JobID, err)
		if _, reportErr := l.report(ctx, axis, job, job.Window.Progress, jobStatusFailed, err.Error(), nil); reportErr != nil {
			log.ErrorContextf(ctx, "factor_recalc_report_failed job_id=%s error=%v", job.JobID, reportErr)
		}
	}
	return true, nil
}

// jobToAxis 把领取到的作业换到轴上（引擎对外接口的入参）：Start 是真实的桶起点，End、Progress 是真实的排他终点
// （Progress 为零表示还没开始），DefaultFilled 是真实周期的闭区间。
func jobToAxis(job *PulledJob) (periodclock.Axis, error) {
	axis, err := periodclock.AxisFor(job.Set.Set.SpaceID, job.Set.Set.Freq)
	if err != nil {
		return periodclock.Axis{}, err
	}
	if job.Window.Start, err = axis.ToAxis(job.Window.Start); err != nil {
		return periodclock.Axis{}, fmt.Errorf("补算起点：%w", err)
	}
	if job.Window.End, err = axis.EndToAxis(job.Window.End); err != nil {
		return periodclock.Axis{}, fmt.Errorf("补算终点：%w", err)
	}
	if !job.Window.Progress.IsZero() {
		if job.Window.Progress, err = axis.EndToAxis(job.Window.Progress); err != nil {
			return periodclock.Axis{}, fmt.Errorf("补算进度：%w", err)
		}
	}
	if job.DefaultFilled, err = convertRanges(job.DefaultFilled, axis.ToAxis); err != nil {
		return periodclock.Axis{}, fmt.Errorf("按默认参数新补的期次：%w", err)
	}
	return axis, nil
}

// report 上报作业进度（引擎对外接口的出参）：进度与按默认参数新补的期次换回真实时间。进度是排他终点，换成最后一根已算完的
// K 线的收盘时刻（与领取时的 EndToAxis 对称），不取下一根的起点：日历最后一根之后没有下一根。
func (l *recalcLoop) report(ctx context.Context, axis periodclock.Axis, job PulledJob, progress time.Time, status, errText string, filled map[string][]domain.TimeRange) (string, error) {
	// 零值表示还没有任何进度（第一个分块之前就失败）：原样上报。
	var realProgress time.Time
	if !progress.IsZero() {
		var err error
		if realProgress, err = axis.FromAxisEnd(progress); err != nil {
			return "", err
		}
	}
	realFilled, err := convertRanges(filled, axis.FromAxis)
	if err != nil {
		return "", err
	}
	return l.client.ReportRecalcProgress(ctx, job.JobID, job.LeaseToken, realProgress, status, errText, realFilled)
}

// giveBack 把放弃的作业归还给管理端（回到待领取、进度保留），下一轮领取立刻能拿到，不用等作业租约过期。
// 归还失败只记日志：作业租约过期后照旧会被重新领取。
func (l *recalcLoop) giveBack(ctx context.Context, axis periodclock.Axis, job PulledJob) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), giveBackTimeout)
	defer cancel()
	if _, err := l.report(ctx, axis, job, job.Window.Progress, jobStatusAccepted, "", nil); err != nil {
		log.WarnContextf(ctx, "factor_recalc_give_back_failed job_id=%s error=%v", job.JobID, err)
	}
}

// convertRanges 逐个换算闭区间的两端。
func convertRanges(ranges map[string][]domain.TimeRange, convert func(time.Time) (time.Time, error)) (map[string][]domain.TimeRange, error) {
	if ranges == nil {
		return nil, nil
	}
	out := make(map[string][]domain.TimeRange, len(ranges))
	for key, items := range ranges {
		for _, item := range items {
			start, err := convert(time.Unix(item.Start, 0))
			if err != nil {
				return nil, err
			}
			end, err := convert(time.Unix(item.End, 0))
			if err != nil {
				return nil, err
			}
			out[key] = append(out[key], domain.TimeRange{Start: start.Unix(), End: end.Unix()})
		}
	}
	return out, nil
}

// runJob advances job.Window.Progress as chunks are reported, so a failure
// report carries the progress the manager already holds.
func (l *recalcLoop) runJob(ctx context.Context, job *PulledJob, axis periodclock.Axis) error {
	set := job.Set
	// 补算作业自带定义，领取时现场编译（设计 8）。
	set.Members = CompileMembers(set.Members)
	if l.prepare != nil {
		release, err := l.prepare(ctx, set)
		if err != nil {
			return fmt.Errorf("准备自定义算子模块：%w", err)
		}
		defer release()
	}
	l.extendHistory(ctx, job, axis)
	l.catchUpLatest(ctx, job)
	lastNote := ""
	step, stepErr := seriesstore.Step(set.Set.Freq)
	if stepErr != nil {
		return stepErr
	}
	// 按默认参数新补的期次：接着管理端已记下的累计，每个分块把新补的期次并进去，再整体上报。
	filled := make(map[string][]domain.TimeRange, len(job.DefaultFilled))
	for key, ranges := range job.DefaultFilled {
		filled[key] = append([]domain.TimeRange(nil), ranges...)
	}
	report := func(ctx context.Context, progress time.Time, outcome pipeline.Outcome) (bool, error) {
		status := jobStatusRunning
		if !progress.Before(job.Window.End) {
			status = jobStatusSucceeded
		}
		if note := recalcexec.DegradedNote(outcome); note != "" {
			lastNote = note
		}
		var changed map[string][]domain.TimeRange
		for key, periods := range outcome.DefaultFilled {
			for _, period := range periods {
				filled[key] = append(filled[key], domain.TimeRange{Start: period.Unix(), End: period.Unix()})
			}
			filled[key] = domain.MergeTimeRanges(filled[key], int64(step/time.Second))
			changed = filled
		}
		current, err := l.report(ctx, axis, *job, progress, status, lastNote, changed)
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
		// 领取之后的准备（备模块、补历史、分块重试的等待）不在在途登记里，期间模块可能已经暂停、收敛并进入恢复追赶；
		// 恢复追赶的门禁会放行分块，所以这里按领取的条件再查一次：模块没有放行就不续跑，由调用方归还作业。
		if l.leaseAllowed != nil && !l.leaseAllowed() {
			return recalcexec.Selection{}, recalcexec.ErrGated
		}
		subjects, err := setSubjects(ctx, l.source, set, job.Subjects)
		if err != nil {
			return recalcexec.Selection{}, err
		}
		columns, err := l.source.DatasetColumns(ctx, set.Set.SpaceID, set.Set.SourceDatasetID)
		if err != nil {
			return recalcexec.Selection{}, fmt.Errorf("list source dataset columns: %w", err)
		}
		return recalcexec.Selection{Set: set.Set, Members: set.Members, Subjects: subjects, Columns: columns}, nil
	}
	return l.runWhenOpen(ctx, func() error { return l.executor.Run(ctx, job.Window, source, report) })
}

// runWhenOpen 执行补算；被生命周期门禁挡住（任务刚建立，自动 init、start 还在路上）时在限定时间内重试，等任务放行后继续，
// 超时仍被挡住就按原样返回 ErrGated（任务被暂停等情况：不报失败，由调用方归还作业）。
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
		// 模块暂停：立刻放弃，不占着作业等恢复；放行后重新领取（用固化的绑定）。
		if l.moduleOpen != nil && !l.moduleOpen() {
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
// 任务还会标成功。例如新建私有因子任务时的初始回填（几千根）远早于只预载了回看根数的本地数据。读之前先把本地历史从 Storage 补到
// 覆盖 [区间起点 − 回看, 现在]；Storage 保留期内没有那么早的数据时有多少补多少，补不上的周期照旧按失败记录。
// 补历史失败不终止任务（只有已有的本地数据可算），记日志。
func (l *recalcLoop) extendHistory(ctx context.Context, job *PulledJob, axis periodclock.Axis) {
	if l.history == nil {
		return
	}
	now := time.Now
	if l.now != nil {
		now = l.now
	}
	end, err := axis.AxisEnd(now())
	if err != nil {
		return
	}
	bars := int(end.Sub(job.Window.Start)/axis.Step()) + maxLookbackOf(job.Set) + 1
	if bars < 1 {
		return
	}
	log.InfoContextf(ctx, "factor_recalc_extend_history job_id=%s task_id=%s bars=%d", job.JobID, job.Set.Set.TaskID, bars)
	if err := l.history(ctx, job.Set, job.Subjects, bars); err != nil {
		log.WarnContextf(ctx, "factor_recalc_extend_history_failed job_id=%s task_id=%s error=%v", job.JobID, job.Set.Set.TaskID, err)
	}
}

// catchUpLatest 把本地时序存储往后拉到补算区间的终点。失败不终止作业（只有已有的本地数据可算），记日志。
func (l *recalcLoop) catchUpLatest(ctx context.Context, job *PulledJob) {
	if l.catchUp == nil {
		return
	}
	pulled, err := l.catchUp(ctx, job.Set, job.Subjects, job.Window.End)
	if err != nil {
		log.WarnContextf(ctx, "factor_recalc_catch_up_failed job_id=%s task_id=%s error=%v", job.JobID, job.Set.Set.TaskID, err)
		return
	}
	if pulled > 0 {
		log.InfoContextf(ctx, "factor_recalc_catch_up job_id=%s task_id=%s subjects=%d", job.JobID, job.Set.Set.TaskID, pulled)
	}
}

// setSubjects resolves an empty selection the way the manager accepted it:
// the source dataset's current subjects.
func setSubjects(ctx context.Context, source SourceDataset, set domain.EngineTask, requested []string) ([]string, error) {
	if len(requested) > 0 {
		return requested, nil
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
