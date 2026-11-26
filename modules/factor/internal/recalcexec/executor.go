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

// SetLocker 提供一个任务的两把锁（setlock.Locks）：补算锁让写历史周期的后台路径（补算作业、缺口恢复、失效重算）彼此串行；
// 任务锁与实时周期、连续水位追赶共用，后台路径只在写到水位之后的周期时才取（见 Executor.lock）。
type SetLocker interface {
	LockRecalcContext(context.Context, string) (func(), error)
	LockContext(context.Context, string) (func(), error)
}

// Selection is the input of one recalc chunk: the set, the members to compute,
// the resolved subjects and the source columns carried into the result.
type Selection struct {
	Set      domain.FactorTask
	Members  []domain.MemberDef
	Subjects []string
	// FrozenUniverse 为真表示 Subjects 是从周期 marker 读到的冻结全集（已按本批标的过滤）；为假表示只是调用方选的标的
	// （人工补算、失效重算、读不到 marker 的恢复），由数据层收窄期望标的（pipeline.SubjectExpecter）。
	FrozenUniverse bool
	Columns        []string
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

// ErrGated 表示任务被生命周期门禁挡住（暂停、恢复追赶……），这一块没有运行；调用方不应把它当作失败。
var ErrGated = errors.New("factor task is gated by lifecycle")

// ParamReader 读取已有结果行的 __params：补算按它还原每一期每个标的当时用的可变成员参数（设计 7.4）。__params 只记可变成员，
// 被选成员里没有可变成员时不读。
type ParamReader interface {
	// ReadRowParams 返回 [start, end) 内这些标的已有行的 __params（行 → 成员键 → 参数名 → 取值）；不存在的行不在结果里。
	ReadRowParams(ctx context.Context, set domain.FactorTask, subjects []string, start, end time.Time) (map[pipeline.RowKey]map[string]map[string]float64, error)
}

type config struct {
	params       ParamReader
	history      pipeline.HistorySource
	gate         func(taskID string) bool
	begin        func(ctx context.Context, taskID string) (func(), bool)
	chunkPeriods int
	memoryBudget int64
	chunkRetries int
	retryBackoff time.Duration
	locks        SetLocker
	committed    func(taskID string) (time.Time, bool)
}

type Option func(*config)

func WithChunkPeriods(n int) Option { return func(cfg *config) { cfg.chunkPeriods = n } }

// WithMemoryBudget 让分块在读取之前按估算峰值内存收窄（设计 7.5）：取配置的分块周期数与“估算不超过 budget 的最大周期数”
// 中较小的一个；1 个周期也超限时该补算以资源错误失败，不尝试执行。budget ≤ 0 表示不限制。
func WithMemoryBudget(budget int64) Option { return func(cfg *config) { cfg.memoryBudget = budget } }
func WithChunkRetry(attempts int, backoff time.Duration) Option {
	return func(cfg *config) { cfg.chunkRetries, cfg.retryBackoff = attempts, backoff }
}

// WithParamReader 设置已有行 __params 的读取方；没有设置时所有期次都按成员自己的参数计算，也不记录按默认参数新补的期次。
func WithParamReader(reader ParamReader) Option { return func(cfg *config) { cfg.params = reader } }

// WithGate 设置生命周期门禁：每个分块真正写结果之前（已取得 lock 要的锁）检查，任务不允许写结果时返回 ErrGated。
// 补算、缺口恢复、失效重算都经过这个执行器，所以暂停、恢复追赶期间这些后台路径不会越过门禁写结果。
func WithGate(gate func(taskID string) bool) Option { return func(cfg *config) { cfg.gate = gate } }

// WithWriteScope 设置写结果的登记式门禁，设置后取代 WithGate：begin 先登记在途、再检查门禁，不允许写时返回 false，
// 允许时返回的函数在这次写结果的操作结束后注销登记。暂停的等待收敛据此知道还有写操作在途。
func WithWriteScope(begin func(ctx context.Context, taskID string) (func(), bool)) Option {
	return func(cfg *config) { cfg.begin = begin }
}
func WithLocks(locks SetLocker) Option { return func(cfg *config) { cfg.locks = locks } }

// WithWatermark 设置连续水位的来源：committed 返回任务已经连续算完的最后一期（轴上，引擎的 Continuity 给出）。
// 实时周期与追赶只写水位之后的周期，所以只写水位以内周期的运行不取任务锁、不挡实时；没有设置或任务还没有水位时一律取任务锁。
func WithWatermark(committed func(taskID string) (time.Time, bool)) Option {
	return func(cfg *config) { cfg.committed = committed }
}

// WithHistory 设置历史截面标的的来源（数据层）：有 cross_section_history: observed 成员时，分块按“标的 + 历史截面标的”
// 估算内存并收窄，分块的计划带上同一份名单，管线的内存预检与选块用的序列数一致。
func WithHistory(source pipeline.HistorySource) Option {
	return func(cfg *config) { cfg.history = source }
}

// Executor runs a recalc window in chunks without touching any job store; the
// caller persists progress through ProgressFunc.
type Executor struct {
	params       ParamReader
	history      pipeline.HistorySource
	gate         func(taskID string) bool
	begin        func(ctx context.Context, taskID string) (func(), bool)
	runner       Runner
	locks        SetLocker
	committed    func(taskID string) (time.Time, bool)
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
		params: cfg.params, history: cfg.history, gate: cfg.gate, begin: cfg.begin, runner: runner, locks: cfg.locks, committed: cfg.committed,
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
	history, err := e.historyFor(ctx, selection, start, end)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	var merged pipeline.Outcome
	for at := start; at.Before(end); {
		segment := paramSegment(selection, base, at, end, duration)
		outcome, err := e.runner.Run(ctx, plan(selection, segment, at, history))
		if err == nil && e.params != nil {
			outcome.DefaultFilled = filterDefaultFilled(selection, base, segment.defaultFilled, outcome.Items)
		}
		merged = mergeOutcome(merged, outcome)
		if err != nil {
			return merged, err
		}
		at = segment.end
	}
	return merged, nil
}

// readParams 读取 [start, end) 内已有行的 __params；没有设置读取方、或被选成员里没有可变成员（固定成员直接用自己的参数，
// __params 也不记它们）时不读，返回空。
func (e *Executor) readParams(ctx context.Context, selection Selection, start, end time.Time) (paramRows, error) {
	if e.params == nil || len(varMembers(selection.Members)) == 0 {
		return paramRows{}, nil
	}
	rows, err := e.params.ReadRowParams(ctx, selection.Set, selection.Subjects, start, end)
	if err != nil {
		return paramRows{}, fmt.Errorf("读取已有行的 __params：%w", err)
	}
	return newParamRows(rows), nil
}

// RunChunkWith 先取锁（见 lock），再在锁内调用 source 重新取得任务、因子、标的与源列，运行一个显式区间，并在仍持有锁时调用
// after（可为 nil）。恢复用它在锁内重读待办、写入并确认，锁释放之后发生的新失效不会被这次确认移除。
// source 返回空标的集合时不运行。
func (e *Executor) RunChunkWith(ctx context.Context, source SelectionSource, start, end time.Time, after func(Selection, pipeline.Outcome, error)) (pipeline.Outcome, error) {
	if e == nil || e.runner == nil {
		return pipeline.Outcome{}, errors.New("factor recalc runner is required")
	}
	if source == nil {
		return pipeline.Outcome{}, errors.New("recalc selection source is required")
	}
	// 先在锁外取一次，只为知道要锁哪个任务；真正使用的选择在登记在途之后重读。
	probe, err := source(ctx)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	unlock, err := e.lock(ctx, probe.Set, end)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return pipeline.Outcome{}, err
	}
	// 先登记在途并检查门禁，再读取任务的定义与标的：读取之后才暂停的，暂停会等这次写入结束；
	// 读取在门禁检查之前发生的话，暂停、切换版本、恢复都可能夹在读取与写入之间，写出用旧定义算的结果。
	release, ok := e.enter(ctx, probe.Set.TaskID)
	if !ok {
		return pipeline.Outcome{}, ErrGated
	}
	defer release()
	selection, err := source(ctx)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	if len(selection.Subjects) == 0 {
		return pipeline.Outcome{}, nil
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
	// 本块最远到 limit（内存预算只会让它更短）：按它决定要不要取任务锁，锁在登记在途、重读选择之前取齐。
	limit, err := e.chunkLimit(selection.Set, start, windowEnd)
	if err != nil {
		return time.Time{}, pipeline.Outcome{}, false, err
	}
	unlock, err := e.lock(ctx, selection.Set, limit)
	if err != nil {
		return time.Time{}, pipeline.Outcome{}, false, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return time.Time{}, pipeline.Outcome{}, false, err
	}
	// 先登记在途并检查门禁，再读取定义与标的（理由同 Run）。
	release, ok := e.enter(ctx, selection.Set.TaskID)
	if !ok {
		return time.Time{}, pipeline.Outcome{}, false, ErrGated
	}
	defer release()
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
	// observed 成员：历史截面标的按本块的区间列出，选块与管线的内存预检用同一份名单。先不计历史标的选出一块，按它的区间
	// 列出历史标的再收窄；收窄后的区间更短，历史标的只会更少，所以再列一次之后一定装得下。
	chunkEnd, err := e.chunkEnd(selection, start, limit, 0)
	if err != nil {
		return time.Time{}, pipeline.Outcome{}, false, err
	}
	history, err := e.historyFor(ctx, selection, start, chunkEnd)
	if err != nil {
		return time.Time{}, pipeline.Outcome{}, false, err
	}
	if len(history) > 0 {
		narrowed, err := e.chunkEnd(selection, start, chunkEnd, len(history))
		if err != nil {
			return time.Time{}, pipeline.Outcome{}, false, err
		}
		if narrowed.Before(chunkEnd) {
			chunkEnd = narrowed
			if history, err = e.historyFor(ctx, selection, start, chunkEnd); err != nil {
				return time.Time{}, pipeline.Outcome{}, false, err
			}
		}
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
	outcome, err := e.runner.Run(ctx, plan(selection, segment, start, history))
	if err == nil && e.params != nil {
		outcome.DefaultFilled = filterDefaultFilled(selection, base, segment.defaultFilled, outcome.Items)
	}
	return segment.end, outcome, false, err
}

// enter 在写结果之前登记并检查门禁；没有配置任何门禁时直接放行。
func (e *Executor) enter(ctx context.Context, taskID string) (func(), bool) {
	switch {
	case e.begin != nil:
		return e.begin(ctx, taskID)
	case e.gate != nil && !e.gate(taskID):
		return nil, false
	}
	return func() {}, true
}

// duration 是轴上的周期：补算的区间都在轴上，轴是连续的。
func (e *Executor) duration(set domain.FactorTask) (time.Duration, error) {
	return periodclock.Continuous{}.Duration(set.Freq)
}

// historyFor 返回有 observed 成员时 [start, end) 的历史截面标的；没有 observed 成员或没有配置来源时为 nil。
func (e *Executor) historyFor(ctx context.Context, selection Selection, start, end time.Time) ([]string, error) {
	probe := pipeline.Plan{Mode: pipeline.ModeRecalc, Set: selection.Set, Members: selection.Members, TargetStart: start, TargetEnd: end,
		Available: selection.Subjects}
	if e.history == nil || !probe.Observed() {
		return nil, nil
	}
	history, err := e.history.HistorySubjects(ctx, probe)
	if err != nil {
		return nil, fmt.Errorf("列出历史截面的标的: %w", err)
	}
	return history, nil
}

// chunkEnd 返回分块的终点；history 是 observed 成员额外放进面板的历史截面标的个数（每个按一条序列估计）。
func (e *Executor) chunkEnd(selection Selection, start, windowEnd time.Time, history int) (time.Time, error) {
	set := selection.Set
	duration, err := e.duration(set)
	if err != nil {
		return time.Time{}, err
	}
	periods := e.chunkPeriods
	if e.memoryBudget > 0 {
		// 序列数按标的数估计（每个标的一个 series_tag）。
		fit, err := pipeline.MaxPeriodsWithin(selection.Members, len(selection.Subjects)+history, len(selection.Columns), periods, e.memoryBudget)
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

// chunkLimit 是从 start 起一块最远的终点：配置的分块周期数，不超过窗口终点。
func (e *Executor) chunkLimit(set domain.FactorTask, start, windowEnd time.Time) (time.Time, error) {
	duration, err := e.duration(set)
	if err != nil {
		return time.Time{}, err
	}
	if int64(e.chunkPeriods) > math.MaxInt64/int64(duration) {
		return time.Time{}, errors.New("recalc chunk duration overflows")
	}
	end := start.Add(time.Duration(e.chunkPeriods) * duration)
	if end.After(windowEnd) {
		end = windowEnd
	}
	return end, nil
}

// lock 按顺序取写结果要的锁。先取任务的补算锁：补算作业、缺口恢复、失效重算彼此串行，读了修正前数据的补算块不会盖掉失效重算刚写的新值。
// 要写的周期（终点 end 之前）里最后一期晚于连续水位、或任务还没有水位时，再取任务锁与实时周期、追赶串行：它们只写水位之后的周期，
// 可变成员 __params 的先读后写不能和它们交错。只写水位以内周期的运行不取任务锁，补算块算得再久也不挡实时。
func (e *Executor) lock(ctx context.Context, set domain.FactorTask, end time.Time) (func(), error) {
	if e.locks == nil {
		return func() {}, nil
	}
	unlockRecalc, err := e.locks.LockRecalcContext(ctx, set.TaskID)
	if err != nil {
		return nil, fmt.Errorf("取因子任务 %s 的补算锁：%w", set.TaskID, err)
	}
	if e.withinWatermark(set, end) {
		return unlockRecalc, nil
	}
	unlockTask, err := e.locks.LockContext(ctx, set.TaskID)
	if err != nil {
		unlockRecalc()
		return nil, fmt.Errorf("取因子任务 %s 的任务锁：%w", set.TaskID, err)
	}
	return func() {
		unlockTask()
		unlockRecalc()
	}, nil
}

// withinWatermark 报告终点 end 之前的最后一期是否不晚于任务的连续水位。水位只前进，这些周期实时与追赶都不会再写。
func (e *Executor) withinWatermark(set domain.FactorTask, end time.Time) bool {
	if e.committed == nil {
		return false
	}
	committed, ok := e.committed(set.TaskID)
	if !ok || committed.IsZero() {
		return false
	}
	duration, err := e.duration(set)
	if err != nil {
		return false
	}
	return !end.Add(-duration).After(committed)
}

// paramRows 是已有行的 __params，按期次和标的整理：期次 → 标的 → 成员键 → 参数名 → 取值。
type paramRows struct {
	rows     map[pipeline.RowKey]map[string]map[string]float64
	byPeriod map[int64]map[string]map[string]map[string]float64
}

// newParamRows 按期次和标的整理已有行的 __params（一个标的一期只有一行）。
func newParamRows(rows map[pipeline.RowKey]map[string]map[string]float64) paramRows {
	byPeriod := make(map[int64]map[string]map[string]map[string]float64)
	for key, params := range rows {
		at := key.At.UTC().Unix()
		if byPeriod[at] == nil {
			byPeriod[at] = make(map[string]map[string]map[string]float64)
		}
		byPeriod[at][key.Subject] = params
	}
	return paramRows{rows: rows, byPeriod: byPeriod}
}

// segment 是从某期起参数不变的一段 [start, end)：各成员本段用的快照、本段按默认参数新补的可变成员期次，以及本段内已有行的 __params。
type segment struct {
	end           time.Time
	params        map[string]domain.ParamSnapshot
	defaultFilled map[string][]time.Time
	base          map[pipeline.RowKey]map[string]map[string]float64
}

// paramSegment 返回从 start 起参数不变的一段（不超过 limit）：段内每一期、每个标的、每个被选可变成员的已有参数都相同。
// 已有行里没有这个标的、或没有这个成员时，该成员在这个标的上取成员自己的参数，这一期记为按默认参数新补。
// 固定成员一律用自己的参数，不看已有行；被选成员里没有可变成员时整段就是 [start, limit)，也不带已有行的 __params。
func paramSegment(selection Selection, base paramRows, start, limit time.Time, duration time.Duration) segment {
	tuned := varMembers(selection.Members)
	first := base.byPeriod[start.UTC().Unix()]
	end := start.Add(duration)
	for end.Before(limit) && (len(tuned) == 0 || sameParams(selection.Subjects, tuned, first, base.byPeriod[end.UTC().Unix()])) {
		end = end.Add(duration)
	}
	seg := segment{end: end, params: make(map[string]domain.ParamSnapshot, len(selection.Members)), defaultFilled: map[string][]time.Time{}}
	for _, member := range selection.Members {
		if !member.IsVar() {
			seg.params[member.Key] = domain.MemberSnapshot(member)
			continue
		}
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
			if missingFor(selection.Subjects, base.byPeriod[at.UTC().Unix()], member.Key) {
				seg.defaultFilled[member.Key] = append(seg.defaultFilled[member.Key], at.UTC())
			}
		}
	}
	if len(tuned) == 0 {
		return seg
	}
	seg.base = make(map[pipeline.RowKey]map[string]map[string]float64)
	for key, members := range base.rows {
		if !key.At.Before(start) && key.At.Before(end) {
			seg.base[key] = members
		}
	}
	return seg
}

// varMembers 返回成员里的可变成员（保持原顺序）。
func varMembers(members []domain.MemberDef) []domain.MemberDef {
	var out []domain.MemberDef
	for _, member := range members {
		if member.IsVar() {
			out = append(out, member)
		}
	}
	return out
}

// missingFor 报告这一期里有没有哪个被选标的缺少这个可变成员的已有参数（行不存在，或 __params 里没有这个成员）。
func missingFor(subjects []string, period map[string]map[string]map[string]float64, memberKey string) bool {
	for _, subject := range subjects {
		if _, ok := period[subject][memberKey]; !ok {
			return true
		}
	}
	return false
}

// sameParams 报告两期里每个被选标的、每个给定可变成员的已有参数是否完全相同（缺失与缺失相同，缺失与存在不同）。
func sameParams(subjects []string, members []domain.MemberDef, left, right map[string]map[string]map[string]float64) bool {
	for _, subject := range subjects {
		for _, member := range members {
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

func plan(selection Selection, seg segment, start time.Time, history []string) pipeline.Plan {
	return pipeline.Plan{
		HistorySubjects: history,
		Mode:            pipeline.ModeRecalc, Set: selection.Set, Members: selection.Members, Params: seg.params, BaseParams: seg.base,
		TargetStart: start, TargetEnd: seg.end,
		Expected:       append([]string(nil), selection.Subjects...),
		Available:      append([]string(nil), selection.Subjects...),
		FrozenUniverse: selection.FrozenUniverse,
		CarryColumns:   selection.Columns, WriteCarry: !selection.FillOnly || selection.FillCarry, FillOnly: selection.FillOnly,
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

// filterDefaultFilled 只保留确实按默认参数新补出值的期次：该期有某个标的写出了这个可变成员的值，并且这个标的的已有行里没有它的参数
// （行不存在，或 __params 里没有这个成员）。候选期次只对可变成员、按“任一被选标的缺参数”生成（固定成员用自己的参数，谈不上按默认参数新补），
// 计算失败的标的、原来就有参数的标的都不算新补。没有逐项结果（Items 为空）说明没有任何值写出，结果为空。
func filterDefaultFilled(selection Selection, base paramRows, filled map[string][]time.Time, items map[pipeline.ItemKey]pipeline.ItemStatus) map[string][]time.Time {
	if len(items) == 0 || len(filled) == 0 {
		return nil
	}
	out := make(map[string][]time.Time, len(filled))
	for member, periods := range filled {
		for _, at := range periods {
			for _, subject := range selection.Subjects {
				if items[pipeline.ItemKey{Member: member, Subject: subject, Period: at.UTC()}] != pipeline.ItemWritten {
					continue
				}
				if _, had := base.byPeriod[at.UTC().Unix()][subject][member]; !had {
					out[member] = append(out[member], at)
					break
				}
			}
		}
	}
	return out
}
