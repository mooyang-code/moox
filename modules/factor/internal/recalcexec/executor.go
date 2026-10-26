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

// ParamReader 读取已有结果行的 __params：补算按它还原每一期每个标的当时用的参数（设计 7.4）。
type ParamReader interface {
	// ReadRowParams 返回 [start, end) 内这些标的已有行的 __params（行 → 成员键 → 参数名 → 取值）；不存在的行不在结果里。
	ReadRowParams(ctx context.Context, set domain.FactorTask, subjects []string, start, end time.Time) (map[pipeline.RowKey]map[string]map[string]float64, error)
}

type config struct {
	params       ParamReader
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

// WithParamReader 设置已有行 __params 的读取方；没有设置时所有期次都按成员自己的参数计算，也不记录按默认参数新补的期次。
func WithParamReader(reader ParamReader) Option { return func(cfg *config) { cfg.params = reader } }

// WithGate 设置生命周期门禁：每个分块真正写结果之前（已持有任务锁）检查，任务不允许写结果时返回 ErrGated。
// 补算、缺口恢复、失效重算都经过这个执行器，所以暂停、恢复追赶、重置期间这些后台路径不会越过门禁写结果。
func WithGate(gate func(taskID string) bool) Option { return func(cfg *config) { cfg.gate = gate } }
func WithLocks(locks SetLocker) Option              { return func(cfg *config) { cfg.locks = locks } }

// Executor runs a recalc window in chunks without touching any job store; the
// caller persists progress through ProgressFunc.
type Executor struct {
	params       ParamReader
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
		params: cfg.params, gate: cfg.gate, runner: runner, locks: cfg.locks, clock: cfg.clock,
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
	base, err := e.readParams(ctx, selection, start, end)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	var merged pipeline.Outcome
	for at := start; at.Before(end); {
		segment := paramSegment(selection, base, at, end, duration)
		outcome, err := e.runner.Run(ctx, plan(selection, segment, at))
		if err == nil && e.params != nil {
			outcome.DefaultFilled = segment.defaultFilled
		}
		merged = mergeOutcome(merged, outcome)
		if err != nil {
			return merged, err
		}
		at = segment.end
	}
	return merged, nil
}

// readParams 读取 [start, end) 内已有行的 __params；没有设置读取方时返回空。
func (e *Executor) readParams(ctx context.Context, selection Selection, start, end time.Time) (paramRows, error) {
	if e.params == nil {
		return paramRows{}, nil
	}
	rows, err := e.params.ReadRowParams(ctx, selection.Set, selection.Subjects, start, end)
	if err != nil {
		return paramRows{}, fmt.Errorf("读取已有行的 __params：%w", err)
	}
	return newParamRows(rows), nil
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
	base, err := e.readParams(ctx, selection, start, chunkEnd)
	if err != nil {
		return time.Time{}, pipeline.Outcome{}, false, err
	}
	segment := paramSegment(selection, base, start, chunkEnd, duration)
	outcome, err := e.runner.Run(ctx, plan(selection, segment, start))
	if err == nil && e.params != nil {
		outcome.DefaultFilled = filterDefaultFilled(segment.defaultFilled, outcome.Items)
	}
	return segment.end, outcome, false, err
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

// paramRows 是已有行的 __params，按期次和标的整理：期次 → 标的 → 成员键 → 参数名 → 取值。
type paramRows struct {
	rows     map[pipeline.RowKey]map[string]map[string]float64
	byPeriod map[int64]map[string]map[string]map[string]float64
}

func newParamRows(rows map[pipeline.RowKey]map[string]map[string]float64) paramRows {
	byPeriod := make(map[int64]map[string]map[string]map[string]float64)
	for key, members := range rows {
		at := key.At.UTC().Unix()
		if byPeriod[at] == nil {
			byPeriod[at] = make(map[string]map[string]map[string]float64)
		}
		byPeriod[at][key.Subject] = members
	}
	return paramRows{rows: rows, byPeriod: byPeriod}
}

// segment 是从某期起参数不变的一段 [start, end)：各成员本段用的快照、本段按默认参数新补的期次，以及本段内已有行的 __params。
type segment struct {
	end           time.Time
	params        map[string]domain.ParamSnapshot
	defaultFilled map[string][]time.Time
	base          map[pipeline.RowKey]map[string]map[string]float64
}

// paramSegment 返回从 start 起参数不变的一段（不超过 limit）：段内每一期、每个标的、每个被选成员的已有参数都相同。
// 已有行里没有这个标的、或没有这个成员时，该成员在这个标的上取成员自己的参数，这一期记为按默认参数新补。
func paramSegment(selection Selection, base paramRows, start, limit time.Time, duration time.Duration) segment {
	first := base.byPeriod[start.UTC().Unix()]
	end := start.Add(duration)
	for end.Before(limit) && sameParams(selection, first, base.byPeriod[end.UTC().Unix()]) {
		end = end.Add(duration)
	}
	seg := segment{end: end, params: make(map[string]domain.ParamSnapshot, len(selection.Members)), defaultFilled: map[string][]time.Time{}}
	for _, member := range selection.Members {
		defaults := domain.MemberSnapshot(member)
		snapshot := make(domain.ParamSnapshot, len(defaults))
		for name, value := range defaults {
			snapshot[name] = domain.ParamValue{Default: value.Default}
		}
		for _, subject := range selection.Subjects {
			stored, ok := first[subject][member.Key]
			if !ok {
				continue
			}
			for name, value := range stored {
				declared, known := defaults[name]
				if !known || value == declared.Default {
					continue
				}
				entry := snapshot[name]
				if entry.Overrides == nil {
					entry.Overrides = map[string]float64{}
				}
				entry.Overrides[subject] = value
				snapshot[name] = entry
			}
		}
		seg.params[member.Key] = snapshot
		for at := start; at.Before(end); at = at.Add(duration) {
			if missingFor(selection, base.byPeriod[at.UTC().Unix()], member.Key) {
				seg.defaultFilled[member.Key] = append(seg.defaultFilled[member.Key], at.UTC())
			}
		}
	}
	seg.base = make(map[pipeline.RowKey]map[string]map[string]float64)
	for key, members := range base.rows {
		if !key.At.Before(start) && key.At.Before(end) {
			seg.base[key] = members
		}
	}
	return seg
}

// missingFor 报告这一期里有没有哪个被选标的缺少这个成员的已有参数（行不存在，或 __params 里没有这个成员）。
func missingFor(selection Selection, period map[string]map[string]map[string]float64, memberKey string) bool {
	for _, subject := range selection.Subjects {
		if _, ok := period[subject][memberKey]; !ok {
			return true
		}
	}
	return false
}

// sameParams 报告两期里每个被选标的、每个被选成员的已有参数是否完全相同（缺失与缺失相同，缺失与存在不同）。
func sameParams(selection Selection, left, right map[string]map[string]map[string]float64) bool {
	for _, subject := range selection.Subjects {
		for _, member := range selection.Members {
			l, lok := left[subject][member.Key]
			r, rok := right[subject][member.Key]
			if lok != rok || len(l) != len(r) {
				return false
			}
			for name, value := range l {
				if other, ok := r[name]; !ok || other != value {
					return false
				}
			}
		}
	}
	return true
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
	for key, periods := range next.DefaultFilled {
		if into.DefaultFilled == nil {
			into.DefaultFilled = map[string][]time.Time{}
		}
		into.DefaultFilled[key] = append(into.DefaultFilled[key], periods...)
	}
	byMember := make(map[string]int, len(into.Factors))
	for i, state := range into.Factors {
		byMember[state.MemberKey] = i
	}
	for _, state := range next.Factors {
		i, ok := byMember[state.MemberKey]
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

func plan(selection Selection, seg segment, start time.Time) pipeline.Plan {
	return pipeline.Plan{
		Mode: pipeline.ModeRecalc, Set: selection.Set, Members: selection.Members, Params: seg.params, BaseParams: seg.base,
		TargetStart: start, TargetEnd: seg.end,
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
			members = append(members, factor.MemberKey)
		}
	}
	note := fmt.Sprintf("%s failed_subjects=%d", degradedPrefix, len(outcome.FailedSubjects))
	if len(members) > 0 {
		note += " members=" + strings.Join(members, ",")
	}
	return note
}

// filterDefaultFilled 只保留实际有成员值写出的期次：整期所有项都失败的期次没有被补出，不能算作“按默认参数新补”。
// 没有逐项结果（Items 为空）时原样返回。
func filterDefaultFilled(filled map[string][]time.Time, items map[pipeline.ItemKey]pipeline.ItemStatus) map[string][]time.Time {
	if len(items) == 0 || len(filled) == 0 {
		return filled
	}
	written := make(map[string]map[int64]bool)
	for key, status := range items {
		if status != pipeline.ItemWritten {
			continue
		}
		if written[key.Member] == nil {
			written[key.Member] = map[int64]bool{}
		}
		written[key.Member][key.Period.Unix()] = true
	}
	out := make(map[string][]time.Time, len(filled))
	for member, periods := range filled {
		for _, at := range periods {
			if written[member][at.Unix()] {
				out[member] = append(out[member], at)
			}
		}
	}
	return out
}
