// Package recalcexec runs a recalc window in chunks through the factor
// pipeline. It is store-free: moox-factor-engine persists progress through the
// manager, the manager never executes jobs.
package recalcexec

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"trpc.group/trpc-go/trpc-go/log"
)

// Runner computes one pipeline plan.
type Runner interface {
	Run(context.Context, pipeline.Plan) (pipeline.Outcome, error)
}

// SetLocker serializes recalc chunks with live periods of the same set.
type SetLocker interface {
	LockContext(context.Context, string) (func(), error)
}

// Selection is the input of one recalc chunk: the set, the members to compute,
// the resolved subjects and the source columns carried into the result.
type Selection struct {
	Set      domain.FactorTask
	Members  []domain.MemberDef
	Subjects []string
	Columns  []string
	// Intervals 是各成员的冻结区间（别名 → 按起点排序），补算按它分参数段：段内各期每个成员的快照都相同，
	// 一段一次计算。没有冻结记录的期次沿用它之前最近的区间，再之前没有就用默认参数。
	Intervals map[string][]domain.ParamInterval
	// FillOnly 只填充：窗口检查通过才计算并写入，有洞与失败都不写任何字段，也不写 carry 列。用于 K 线空洞补齐后的恢复。
	FillOnly bool
	// FillCarry 让只填充同时按源数据写 carry 列：预热中被跳过的周期在实时路径上一行都没写，恢复时要把 carry 列一并补上。
	FillCarry bool
}

// SelectionSource is consulted under the set lock before every chunk attempt,
// so a caller can re-read membership or observe a cancellation between chunks.
type SelectionSource func(context.Context) (Selection, error)

// ProgressFunc records a completed chunk. Returning stop=true ends the run
// without error, e.g. because the job was cancelled.
type ProgressFunc func(ctx context.Context, progress time.Time, outcome pipeline.Outcome) (stop bool, err error)

// Window is the aligned [Start, End) range of a job and the resume point.
type Window struct {
	Start    time.Time
	End      time.Time
	Progress time.Time
}

const defaultChunkPeriods = 2000

// maxChunkRetryBackoff 是同一个补算分块两次重试之间等待的上限。
const maxChunkRetryBackoff = 2 * time.Minute

// ErrGated 表示任务被生命周期门禁挡住（暂停、重置……），这一块没有运行；调用方不应把它当作失败。
var ErrGated = errors.New("factor task is gated by lifecycle")

type config struct {
	gate         func(taskID string) bool
	chunkPeriods int
	memoryBudget int64
	chunkRetries int
	retryBackoff time.Duration
	clock        periodclock.Clock
	locks        SetLocker
}

type Option func(*config)

func WithChunkPeriods(n int) Option { return func(cfg *config) { cfg.chunkPeriods = n } }

// WithMemoryBudget 让分块在读取之前按估算峰值内存收窄（设计 7.5）：取配置的分块周期数与“估算不超过 budget 的最大周期数”
// 中较小的一个；1 个周期也超限时该补算以资源错误失败，不尝试执行。budget ≤ 0 表示不限制。
func WithMemoryBudget(budget int64) Option { return func(cfg *config) { cfg.memoryBudget = budget } }
func WithChunkRetry(attempts int, backoff time.Duration) Option {
	return func(cfg *config) { cfg.chunkRetries, cfg.retryBackoff = attempts, backoff }
}
func WithClock(clock periodclock.Clock) Option { return func(cfg *config) { cfg.clock = clock } }

// WithGate 设置生命周期门禁：每个分块真正写结果之前（已持有任务锁）检查，任务不允许写结果时返回 ErrGated。
// 补算、缺口恢复、失效重算都经过这个执行器，所以暂停、恢复追赶、重置期间这些后台路径不会越过门禁写结果。
func WithGate(gate func(taskID string) bool) Option { return func(cfg *config) { cfg.gate = gate } }
func WithLocks(locks SetLocker) Option              { return func(cfg *config) { cfg.locks = locks } }

// Executor runs a recalc window in chunks without touching any job store; the
// caller persists progress through ProgressFunc.
type Executor struct {
	gate         func(taskID string) bool
	runner       Runner
	locks        SetLocker
	clock        periodclock.Clock
	chunkPeriods int
	memoryBudget int64
	chunkRetries int
	retryBackoff time.Duration
}

func NewExecutor(runner Runner, options ...Option) *Executor {
	cfg := config{chunkPeriods: defaultChunkPeriods, chunkRetries: 3, retryBackoff: 5 * time.Second}
	for _, option := range options {
		if option != nil {
			option(&cfg)
		}
	}
	if cfg.chunkPeriods <= 0 {
		cfg.chunkPeriods = defaultChunkPeriods
	}
	if cfg.chunkRetries <= 0 {
		cfg.chunkRetries = 1
	}
	return &Executor{
		gate: cfg.gate, runner: runner, locks: cfg.locks, clock: cfg.clock,
		chunkPeriods: cfg.chunkPeriods, memoryBudget: cfg.memoryBudget, chunkRetries: cfg.chunkRetries, retryBackoff: cfg.retryBackoff,
	}
}

// Run executes the window from its resume point. An empty subject selection
// completes the window at once. Every finished chunk is reported before the
// next one starts; the final report carries progress == window.End.
func (e *Executor) Run(ctx context.Context, window Window, source SelectionSource, report ProgressFunc) error {
	if e == nil || e.runner == nil {
		return errors.New("factor recalc runner is required")
	}
	if source == nil || report == nil {
		return errors.New("recalc selection source and progress reporter are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	progress := window.Progress
	if progress.Before(window.Start) {
		progress = window.Start
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !progress.Before(window.End) {
			_, err := report(ctx, window.End, pipeline.Outcome{})
			return err
		}
		chunkEnd, outcome, empty, err := e.runChunkWithRetry(ctx, source, progress, window.End)
		if err != nil {
			return err
		}
		if empty {
			_, err := report(ctx, window.End, pipeline.Outcome{})
			return err
		}
		stop, err := report(ctx, chunkEnd, outcome)
		if err != nil || stop || !chunkEnd.Before(window.End) {
			return err
		}
		progress = chunkEnd
	}
}

// RunChunk computes one explicit range with a fixed selection.
func (e *Executor) RunChunk(ctx context.Context, selection Selection, start, end time.Time) (pipeline.Outcome, error) {
	if e == nil || e.runner == nil {
		return pipeline.Outcome{}, errors.New("factor recalc runner is required")
	}
	unlock, err := e.lock(ctx, selection.Set.TaskID)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	defer unlock()
	return e.runSegments(ctx, selection, start, end)
}

// runSegments 把 [start, end) 按参数段依次计算，合并各段的结果。
func (e *Executor) runSegments(ctx context.Context, selection Selection, start, end time.Time) (pipeline.Outcome, error) {
	duration, err := e.duration(selection.Set)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	var merged pipeline.Outcome
	for at := start; at.Before(end); {
		segmentEnd, params := paramSegment(selection, at, end, duration)
		outcome, err := e.runner.Run(ctx, plan(selection, params, at, segmentEnd))
		merged = mergeOutcome(merged, outcome)
		if err != nil {
			return merged, err
		}
		at = segmentEnd
	}
	return merged, nil
}

// RunChunkWith 先取任务锁，再在锁内调用 source 重新取得任务、因子、标的与源列，运行一个显式区间，并在仍持有锁时调用
// after（可为 nil）。恢复用它在锁内重读待办、写入并确认，锁释放之后发生的新失效不会被这次确认移除。
// source 返回空标的集合时不运行。
func (e *Executor) RunChunkWith(ctx context.Context, source SelectionSource, start, end time.Time, after func(Selection, pipeline.Outcome, error)) (pipeline.Outcome, error) {
	if e == nil || e.runner == nil {
		return pipeline.Outcome{}, errors.New("factor recalc runner is required")
	}
	if source == nil {
		return pipeline.Outcome{}, errors.New("recalc selection source is required")
	}
	// 先在锁外取一次，只为知道要锁哪个任务；真正使用的选择在锁内重读。
	probe, err := source(ctx)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	unlock, err := e.lock(ctx, probe.Set.TaskID)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return pipeline.Outcome{}, err
	}
	selection, err := source(ctx)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	if len(selection.Subjects) == 0 {
		return pipeline.Outcome{}, nil
	}
	if e.gate != nil && !e.gate(selection.Set.TaskID) {
		return pipeline.Outcome{}, ErrGated
	}
	outcome, runErr := e.runSegments(ctx, selection, start, end)
	if after != nil {
		after(selection, outcome, runErr)
	}
	return outcome, runErr
}

// runChunkWithRetry retries a chunk only for transient Storage failures. Progress
// is committed after a successful chunk, so a final failure still leaves the job
// resumable from the last completed chunk.
func (e *Executor) runChunkWithRetry(ctx context.Context, source SelectionSource, start, windowEnd time.Time) (time.Time, pipeline.Outcome, bool, error) {
	var lastErr error
	for attempt := 1; attempt <= e.chunkRetries; attempt++ {
		chunkEnd, outcome, empty, err := e.runChunk(ctx, source, start, windowEnd)
		if err == nil {
			return chunkEnd, outcome, empty, nil
		}
		lastErr = err
		if !errors.Is(err, storageio.ErrInfra) || attempt == e.chunkRetries {
			break
		}
		// 指数退避（5s、10s、20s…，最长 2 分钟）：Storage 读不动时，线性的短等待只会让重试堆在一起。
		timer := time.NewTimer(min(e.retryBackoff<<(attempt-1), maxChunkRetryBackoff))
		select {
		case <-ctx.Done():
			timer.Stop()
			return time.Time{}, pipeline.Outcome{}, false, ctx.Err()
		case <-timer.C:
		}
	}
	return time.Time{}, pipeline.Outcome{}, false, lastErr
}

func (e *Executor) runChunk(ctx context.Context, source SelectionSource, start, windowEnd time.Time) (time.Time, pipeline.Outcome, bool, error) {
	selection, err := source(ctx)
	if err != nil {
		return time.Time{}, pipeline.Outcome{}, false, err
	}
	if len(selection.Subjects) == 0 {
		return windowEnd, pipeline.Outcome{}, true, nil
	}
	unlock, err := e.lock(ctx, selection.Set.TaskID)
	if err != nil {
		return time.Time{}, pipeline.Outcome{}, false, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return time.Time{}, pipeline.Outcome{}, false, err
	}
	// The selection may have changed while waiting for the lock (a member was
	// disabled or the job cancelled), so it is consulted again under the lock.
	selection, err = source(ctx)
	if err != nil {
		return time.Time{}, pipeline.Outcome{}, false, err
	}
	if len(selection.Subjects) == 0 {
		return windowEnd, pipeline.Outcome{}, true, nil
	}
	// 分块大小按锁内重读的选择计算：等锁期间标的或成员可能变化，内存预算要按实际要算的范围收窄。
	chunkEnd, err := e.chunkEnd(selection, start, windowEnd)
	if err != nil {
		return time.Time{}, pipeline.Outcome{}, false, err
	}
	if e.gate != nil && !e.gate(selection.Set.TaskID) {
		return time.Time{}, pipeline.Outcome{}, false, ErrGated
	}
	// 一次只算一个参数段：分块在参数变化处截断，进度按段推进，段内各期的快照相同。
	duration, err := e.duration(selection.Set)
	if err != nil {
		return time.Time{}, pipeline.Outcome{}, false, err
	}
	segmentEnd, params := paramSegment(selection, start, chunkEnd, duration)
	outcome, err := e.runner.Run(ctx, plan(selection, params, start, segmentEnd))
	return segmentEnd, outcome, false, err
}

func (e *Executor) duration(set domain.FactorTask) (time.Duration, error) {
	clock := e.clock
	if clock == nil {
		var err error
		if clock, err = periodclock.ForSpace(set.SpaceID); err != nil {
			return 0, err
		}
	}
	return clock.Duration(set.Freq)
}

func (e *Executor) chunkEnd(selection Selection, start, windowEnd time.Time) (time.Time, error) {
	set := selection.Set
	duration, err := e.duration(set)
	if err != nil {
		return time.Time{}, err
	}
	periods := e.chunkPeriods
	if e.memoryBudget > 0 {
		// 序列数按标的数估计（crypto 每个标的一个 series_tag）。
		fit, err := pipeline.MaxPeriodsWithin(selection.Members, len(selection.Subjects), len(selection.Columns), periods, e.memoryBudget)
		if err != nil {
			return time.Time{}, err
		}
		if fit < periods {
			log.Infof("factor_recalc_chunk_narrowed task_id=%s periods=%d configured=%d budget=%d", set.TaskID, fit, periods, e.memoryBudget)
			periods = fit
		}
	}
	if int64(periods) > math.MaxInt64/int64(duration) {
		return time.Time{}, errors.New("recalc chunk duration overflows")
	}
	end := start.Add(time.Duration(periods) * duration)
	if end.After(windowEnd) {
		end = windowEnd
	}
	return end, nil
}

func (e *Executor) lock(ctx context.Context, taskID string) (func(), error) {
	if e.locks == nil {
		return func() {}, nil
	}
	unlock, err := e.locks.LockContext(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("lock factor task %s: %w", taskID, err)
	}
	return unlock, nil
}

// paramSegment 返回从 start 起参数不变的一段 [start, segmentEnd)（不超过 limit）与这一段各成员的冻结参数。
// 某成员在 start 有冻结区间时，段在区间结束处截断；没有时沿用之前最近的区间（没有就用默认参数），段在下一个区间开始处截断。
func paramSegment(selection Selection, start, limit time.Time, duration time.Duration) (time.Time, map[string]domain.ParamFreeze) {
	end := limit
	params := make(map[string]domain.ParamFreeze, len(selection.Members))
	for _, member := range selection.Members {
		var previous *domain.ParamInterval
		covered := false
		for i := range selection.Intervals[member.Alias] {
			interval := selection.Intervals[member.Alias][i]
			switch {
			case interval.Covers(start):
				covered = true
				params[member.Alias] = domain.ParamFreeze{TaskID: selection.Set.TaskID, MemberAlias: member.Alias, PeriodTime: start,
					Snapshot: interval.Snapshot, SnapshotHash: interval.SnapshotHash, Source: interval.Source}
				if stop := interval.End.Add(duration); stop.Before(end) {
					end = stop
				}
			case interval.End.Before(start):
				previous = &selection.Intervals[member.Alias][i]
			case interval.Start.After(start) && !covered:
				if interval.Start.Before(end) {
					end = interval.Start
				}
			}
			if covered || interval.Start.After(start) {
				break
			}
		}
		if !covered && previous != nil {
			params[member.Alias] = domain.ParamFreeze{TaskID: selection.Set.TaskID, MemberAlias: member.Alias, PeriodTime: start,
				Snapshot: previous.Snapshot, SnapshotHash: previous.SnapshotHash, Source: carriedSource(previous.Source)}
		}
	}
	if !end.After(start) {
		end = start.Add(duration)
	}
	return end, params
}

// carriedSource 是沿用 previous 来源的区间时的来源：从未提交过（默认值）沿用的仍是默认值。
func carriedSource(previous string) string {
	if previous == domain.ParamsDefault {
		return domain.ParamsDefault
	}
	return domain.ParamsCarried
}

// mergeOutcome 合并两个参数段的结果：失败标的取并集、状态取较差的一个、逐项结果合并。
func mergeOutcome(into, next pipeline.Outcome) pipeline.Outcome {
	if into.Status == "" {
		return next
	}
	if next.Status == "degraded" {
		into.Status = "degraded"
	}
	into.FailedSubjects = append(into.FailedSubjects, next.FailedSubjects...)
	into.RowsWritten += next.RowsWritten
	into.Warming += next.Warming
	if into.Items == nil && next.Items != nil {
		into.Items = map[pipeline.ItemKey]pipeline.ItemStatus{}
	}
	for key, status := range next.Items {
		into.Items[key] = status
	}
	byMember := make(map[string]int, len(into.Factors))
	for i, state := range into.Factors {
		byMember[state.Member] = i
	}
	for _, state := range next.Factors {
		i, ok := byMember[state.Member]
		if !ok {
			into.Factors = append(into.Factors, state)
			continue
		}
		if state.Status != "complete" {
			into.Factors[i].Status = state.Status
		}
		into.Factors[i].FailedSubjects = append(into.Factors[i].FailedSubjects, state.FailedSubjects...)
	}
	return into
}

func plan(selection Selection, params map[string]domain.ParamFreeze, start, end time.Time) pipeline.Plan {
	return pipeline.Plan{
		Mode: pipeline.ModeRecalc, Set: selection.Set, Members: selection.Members, Params: params,
		TargetStart: start, TargetEnd: end,
		Expected:     append([]string(nil), selection.Subjects...),
		Available:    append([]string(nil), selection.Subjects...),
		CarryColumns: selection.Columns, WriteCarry: !selection.FillOnly || selection.FillCarry, FillOnly: selection.FillOnly,
	}
}

const degradedPrefix = "degraded:"

// DegradedNote summarises partial failures of a chunk; the job still succeeds
// because the factor values of unaffected subjects were written.
func DegradedNote(outcome pipeline.Outcome) string {
	if outcome.Status != "degraded" {
		return ""
	}
	var members []string
	for _, factor := range outcome.Factors {
		if factor.Status != "complete" {
			members = append(members, factor.Member)
		}
	}
	note := fmt.Sprintf("%s failed_subjects=%d", degradedPrefix, len(outcome.FailedSubjects))
	if len(members) > 0 {
		note += " members=" + strings.Join(members, ",")
	}
	return note
}
