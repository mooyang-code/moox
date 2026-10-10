package replay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	"github.com/mooyang-code/moox/packages/frequency"
	"github.com/mooyang-code/moox/packages/marketcalendar"
)

const (
	defaultChunkBars     = 50
	defaultLiquidateBars = 3
	pollInterval         = 30 * time.Second
	// finishAttempts 与 finishRetryDelay：终态写入遇到临时错误（锁、只读、磁盘）时的重试次数与初始间隔（逐次翻倍）。
	finishAttempts   = 6
	finishRetryDelay = 2 * time.Second
)

var errCancelled = errors.New("回放已取消")

// Runner 是进程内单工作协程的回放执行器：同一时刻只跑一个回放，pending 任务排队。
type Runner struct {
	Store                     *store.Store
	Client                    input.Client
	ChunkBars                 int
	MissingPriceLiquidateBars int
	MaxBars                   int
	InitialEquity             float64
	Now                       func() time.Time
	Logf                      func(format string, args ...any)
	// FinishRetryDelay 是终态写入失败后的初始重试间隔；零值取默认值。
	FinishRetryDelay time.Duration

	once sync.Once
	wake chan struct{}
}

func (r *Runner) init() {
	r.once.Do(func() { r.wake = make(chan struct{}, 1) })
}

// Wake 通知执行循环有新任务（不阻塞）。
func (r *Runner) Wake() {
	if r == nil {
		return
	}
	r.init()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Run 循环认领并执行 pending 回放，直到 ctx 结束。进程退出时运行中的任务保持 running，下次启动标记为 failed(interrupted)。
func (r *Runner) Run(ctx context.Context) {
	if r == nil || r.Store == nil {
		return
	}
	r.init()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		r.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		case <-ticker.C:
		}
	}
}

// drain 依次执行全部 pending 回放。
func (r *Runner) drain(ctx context.Context) {
	for ctx.Err() == nil {
		job, found, err := r.Store.ClaimNextReplay(ctx, r.now())
		if err != nil {
			r.logf("认领回放失败：%v", err)
			return
		}
		if !found {
			return
		}
		r.Execute(ctx, job)
	}
}

// Execute 执行一个已认领（running）的回放并写入终态。回放与实时求值在同一进程，任何 panic 都在这里收住，
// 记为 failed，不能拖垮实时求值。
func (r *Runner) Execute(ctx context.Context, job store.Replay) {
	defer func() {
		if recovered := recover(); recovered != nil {
			r.logf("回放 %s 执行异常：%v", job.ReplayID, recovered)
			if err := r.finish(ctx, job.ReplayID, store.ReplayFailed, nil, "回放执行异常，请查看策略模块日志"); err != nil && !errors.Is(err, store.ErrNotFound) {
				r.logf("写入回放 %s 的终态失败：%v", job.ReplayID, err)
			}
		}
	}()
	metrics, err := r.replay(ctx, job)
	if ctx.Err() != nil {
		return
	}
	raw, marshalErr := json.Marshal(metrics)
	if marshalErr != nil {
		r.logf("回放 %s 的指标无法编码：%v", job.ReplayID, marshalErr)
		raw = []byte(`{}`)
	}
	if errors.Is(err, errCancelled) {
		r.logf("回放 %s 已取消", job.ReplayID)
		if metrics.Bars > 0 {
			if recordErr := r.Store.RecordCancelledReplayMetrics(ctx, job.ReplayID, raw, r.now()); recordErr != nil {
				r.logf("写入回放 %s 取消前的指标失败：%v", job.ReplayID, recordErr)
			}
		}
		return
	}
	status, message := store.ReplayDone, ""
	if err != nil {
		// 记录里只保存不含服务地址、也不含数据库驱动英文原文的概述，原始错误写日志。
		friendly, _ := store.FriendlyMessage(err)
		if errors.Is(err, input.ErrViewNotFound) {
			friendly = fmt.Sprintf("View %s 已不存在，回放无法继续", job.ViewID)
		}
		status, message = store.ReplayFailed, friendly
		r.logf("回放 %s 失败：%v；原始错误：%v", job.ReplayID, err, input.RawCause(err))
	}
	finishErr := r.finish(ctx, job.ReplayID, status, raw, message)
	if errors.Is(finishErr, store.ErrNotFound) {
		// 结束前任务已被取消（取消落在最后一次状态检查之后，或取消后读取又失败）：保留已算出的部分指标。
		if current, err := r.Store.ReplayStatus(ctx, job.ReplayID); err == nil && current == store.ReplayCancelled && metrics.Bars > 0 {
			if recordErr := r.Store.RecordCancelledReplayMetrics(ctx, job.ReplayID, raw, r.now()); recordErr != nil {
				r.logf("写入回放 %s 取消前的指标失败：%v", job.ReplayID, recordErr)
			}
		}
		return
	}
	if finishErr != nil {
		r.logf("写入回放 %s 的终态失败：%v", job.ReplayID, finishErr)
	}
}

// finish 写入回放终态。临时写入错误按退避重试：执行器只认领 pending，终态没写进去的任务会一直停在 running，
// 占用活动任务额度，直到下次启动才被标成 interrupted 并丢掉已算出的指标。任务已不在 running（取消）返回 ErrNotFound。
func (r *Runner) finish(ctx context.Context, replayID, status string, metricsJSON json.RawMessage, errText string) error {
	delay := r.FinishRetryDelay
	if delay <= 0 {
		delay = finishRetryDelay
	}
	var err error
	for attempt := 0; attempt < finishAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return err
			case <-time.After(delay):
			}
			delay *= 2
		}
		err = r.Store.FinishReplay(ctx, replayID, status, metricsJSON, errText, r.now())
		if err == nil || errors.Is(err, store.ErrNotFound) || store.IsPermanentWriteError(err) {
			return err
		}
		r.logf("写入回放 %s 的终态失败（第 %d 次）：%v", replayID, attempt+1, err)
	}
	return err
}

func (r *Runner) replay(ctx context.Context, job store.Replay) (Metrics, error) {
	if r.Client == nil {
		return Metrics{}, errors.New("Storage 与 Factor 依赖未配置，不能回放")
	}
	strategy, err := dsl.Parse([]byte(job.DSLYaml))
	if err != nil {
		return Metrics{}, err
	}
	resolved, program, err := input.Resolve(ctx, r.Client, job.SpaceID, job.ViewID, strategy)
	if err != nil {
		return Metrics{}, err
	}
	if !resolved.Spot {
		return Metrics{}, fmt.Errorf("View %s 的源数据集 market_type=%s；第一版回放只支持现货", job.ViewID, resolved.MarketType)
	}
	if drift := factorDrift(job.Factors, resolved.Factors); drift != "" {
		return Metrics{}, fmt.Errorf("因子定义在排队期间发生变化（%s），请重新发起回放", drift)
	}
	view, err := r.Client.GetView(ctx, job.SpaceID, job.ViewID)
	if err != nil {
		return Metrics{}, err
	}
	hasClose := false
	for _, column := range view.Columns {
		if column.Name == "close" {
			hasClose = true
		}
	}
	if !hasClose {
		return Metrics{}, fmt.Errorf("View %s 没有 close 列，无法估值", job.ViewID)
	}
	bindings, err := r.Client.ListDatasetSubjects(ctx, job.SpaceID, view.DatasetID)
	if err != nil {
		return Metrics{}, err
	}
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].SubjectID < bindings[j].SubjectID })
	subjects := make([]input.Subject, 0, len(bindings))
	for _, subject := range bindings {
		subject.Active = true
		subjects = append(subjects, subject)
	}
	if len(subjects) == 0 {
		return Metrics{}, fmt.Errorf("View %s 的数据集没有标的绑定", job.ViewID)
	}
	// 退避之前和之后都确认回放没有被取消：写入密集的 View 上一段读取可能退避很久，取消要尽快生效。
	abort := r.abortHook(job.ReplayID)
	// 活动索引的代次没变：同一代索引只追加、不删除行，提交时校验过的区间仍然有效（覆盖统计随新 bar 前移不影响已有的行）。
	// 代次变了（排队期间重建过索引，新一代只保留最近的根数）才按新索引的精确统计复查起点并截断终点。
	end := job.EndTime
	if view.Generation != job.ViewGeneration {
		if view, end, err = r.recheckWindow(ctx, job, resolved, program, view, job.StartTime, end, false, abort); err != nil {
			return Metrics{}, err
		}
	}
	bars, err := input.ReplayBars(resolved.Calendar, resolved.Bar, job.StartTime, end, r.maxBars())
	if err != nil {
		return Metrics{}, err
	}
	if len(bars) == 0 {
		return Metrics{}, errors.New("回放区间内没有完整的 bar")
	}
	freq, err := frequency.Parse(resolved.Bar)
	if err != nil {
		return Metrics{}, err
	}
	acc := newAccumulator(r.initialEquity(), periodsPerYear(resolved.Calendar, freq.NominalDuration()), minAnnualizedBars(resolved.Calendar, freq.NominalDuration()))
	acc.metrics.Factors = resolved.Factors
	acc.metrics.Limitations = limitations(r.liquidateAfter(), usesTags(strategy))
	if end.Before(job.EndTime) {
		acc.setNote(noteTruncated, fmt.Sprintf("排队期间 View 重建了索引，回放区间的终点按新索引截短：区间内最后一根 K 线结束于 %s", input.BarEndLabel(resolved.Calendar, resolved.Bar, bars[len(bars)-1].BarEnd)))
	}
	columns := uniqueStrings(append(append([]string{"close"}, program.Columns...), program.PreviousColumns...))
	reader := &segmentReader{
		runner: r, job: job, resolved: resolved, program: program, end: end, acc: acc, abort: abort,
		rows:    &input.RangeLoader{Client: r.Client, SpaceID: job.SpaceID, View: view, Subjects: subjects, Columns: columns, Abort: abort},
		history: &input.RangeLoader{Client: r.Client, SpaceID: job.SpaceID, View: view, Subjects: subjects, Columns: []string{"close"}, Abort: abort},
	}
	members := cachedMembership(r.Client, job.SpaceID)
	ledger := NewLedger(r.initialEquity(), job.FeeBps, r.liquidateAfter())
	state := engine.State{}
	previousEquity := r.initialEquity()
	chunk := r.chunkBars()
	for start := 0; start < len(bars); start += chunk {
		segment := bars[start:min(start+chunk, len(bars))]
		rows, presence, err := reader.load(ctx, segment)
		if err != nil {
			return acc.finish(), err
		}
		// 换代后新索引可能截短了终点：丢掉终点之后的 bar（复查保证本段至少还剩第一根）。
		if cut := sort.Search(len(bars), func(i int) bool { return !bars[i].StorageStart.Before(reader.end) }); cut < len(bars) {
			bars = bars[:cut]
			segment = bars[start:min(start+chunk, len(bars))]
			acc.setNote(noteTruncated, fmt.Sprintf("回放过程中 View 重建了索引，回放区间的终点按新索引截短：区间内最后一根 K 线结束于 %s", input.BarEndLabel(resolved.Calendar, resolved.Bar, bars[cut-1].BarEnd)))
		}
		for _, bar := range segment {
			if err := abort(ctx); err != nil {
				return acc.finish(), err
			}
			decision, err := r.evaluate(ctx, program, resolved, subjects, members, presence, rows, bar, state)
			if err != nil {
				return acc.finish(), err
			}
			// 同一标的同一周期有多个序列时，这根 bar 上它的价格不可信，按缺价处理。
			key := bar.StorageStart.Unix()
			prices := make(map[string]float64)
			for subjectID, row := range rows.Bars[key] {
				if _, ambiguous := rows.Ambiguous[key][subjectID]; ambiguous {
					continue
				}
				if price, ok := row.Values["close"]; ok && price > 0 {
					prices[subjectID] = price
				}
			}
			ok := decision.Status == engine.StatusOK
			targets := make(map[string]float64, len(decision.Targets))
			for _, target := range decision.Targets {
				weight, _ := strconv.ParseFloat(target.Weight.String(), 64)
				targets[target.InstrumentID] = weight
			}
			outcome := ledger.Step(Step{Prices: prices, Targets: targets, OK: ok})
			if ok {
				state = decision.State
			}
			barReturn := 0.0
			if previousEquity > 0 {
				barReturn = outcome.EquityAfter/previousEquity - 1
			}
			previousEquity = outcome.EquityAfter
			// 先写入周期再累计指标：写入失败时，保留的部分指标与已写入的周期一致。
			if err := r.writeBar(ctx, job.ReplayID, bar.BarEnd, decision, ledger, outcome, barReturn); err != nil {
				return acc.finish(), err
			}
			acc.add(bar.BarEnd, ok, decision.SkipReason, outcome, ledger.Holdings())
		}
		if err := r.Store.UpdateReplayProgress(ctx, job.ReplayID, segment[len(segment)-1].BarEnd, r.now()); err != nil {
			return acc.finish(), err
		}
	}
	return acc.finish(), nil
}

// 按 key 替换的局限说明：同一件事只保留最新的一条。
const (
	noteTruncated   = "truncated"
	noteIndexSwitch = "index_switch"
)

// maxIndexSwitches 是一次回放内允许跟随的索引换代次数（整个回放累计，不按段计）。
const maxIndexSwitches = 3

// maxRecheckRereads 是复查时读取覆盖统计遇到活动索引再次变化、按最新 View 重读的次数上限；第 n 次重读前等待
// n 倍 recheckRereadBackoff（Storage 先改元数据、再切换内存里的活动索引，两者之间短暂不一致）。
const maxRecheckRereads = 3

// maxRevisionRereads 是同一段内主数据与年龄探针修订号不一致时整体重读的最多次数。
const maxRevisionRereads = 5

var recheckRereadBackoff = time.Second

// pause 等待 d，等待前后都确认回放没有被取消（abort 为空时只等待）；ctx 结束时返回 ctx 的错误。
func pause(ctx context.Context, abort func(context.Context) error, d time.Duration) error {
	check := func() error {
		if abort == nil {
			return nil
		}
		return abort(ctx)
	}
	if err := check(); err != nil {
		return err
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	return check()
}

// withTransportRetry 调用 call，遇到传输失败（Storage 暂时不可用）时按 1、2、3 倍 recheckRereadBackoff 退避重试，
// 最多 3 次，与分段读取一致：长回放不因复查时的一次瞬时失败而失败。
func withTransportRetry[T any](ctx context.Context, abort func(context.Context) error, call func() (T, error)) (T, error) {
	for retries := 0; ; retries++ {
		value, err := call()
		var transportErr *input.TransportError
		if err == nil || !errors.As(err, &transportErr) || retries >= maxRecheckRereads {
			return value, err
		}
		if err := pause(ctx, abort, time.Duration(retries+1)*recheckRereadBackoff); err != nil {
			var zero T
			return zero, err
		}
	}
}

// abortHook 返回确认回放是否已被取消的钩子：已取消返回 errCancelled；读取状态失败按错误返回（与逐期检查一致，数据库
// 持续异常时不会在退避里空等）。
func (r *Runner) abortHook(replayID string) func(context.Context) error {
	return func(ctx context.Context) error {
		status, err := r.Store.ReplayStatus(ctx, replayID)
		if err != nil {
			return fmt.Errorf("读取回放状态：%w", err)
		}
		if status == store.ReplayCancelled {
			return errCancelled
		}
		return nil
	}
}

// recheckWindow 在活动索引换了一代后，按新索引的精确统计复查 [start, end) 的起点并截断终点；midRun 说明换代发生在
// 回放过程中（start 是剩余区间的起点）还是排队期间（start 是回放起点）。新索引的统计未知时无法确认区间仍然有效，拒绝继续。
func (r *Runner) recheckWindow(ctx context.Context, job store.Replay, resolved input.Resolved, program *dsl.Program, view input.ViewInfo, start, end time.Time, midRun bool, abort func(context.Context) error) (input.ViewInfo, time.Time, error) {
	when := "在排队期间"
	if midRun {
		when = "在回放过程中"
	}
	for attempt := 0; ; attempt++ {
		covered, err := withTransportRetry(ctx, abort, func() (input.ViewInfo, error) {
			return input.WithCoverage(ctx, r.Client, job.SpaceID, view, true)
		})
		if err == nil {
			view = covered
			break
		}
		if !errors.Is(err, input.ErrStale) {
			return input.ViewInfo{}, time.Time{}, err
		}
		// 读取覆盖统计时活动索引又换了槽位：稍等后按最新的 View 重新复查。
		if attempt >= maxRecheckRereads {
			return input.ViewInfo{}, time.Time{}, fmt.Errorf("View %s %s重建了索引，读取新索引的覆盖统计时索引仍在变化：%w；请稍后重新发起回放", job.ViewID, when, err)
		}
		if err := pause(ctx, abort, time.Duration(attempt+1)*recheckRereadBackoff); err != nil {
			return input.ViewInfo{}, time.Time{}, err
		}
		if view, err = withTransportRetry(ctx, abort, func() (input.ViewInfo, error) {
			return r.Client.GetView(ctx, job.SpaceID, job.ViewID)
		}); err != nil {
			return input.ViewInfo{}, time.Time{}, err
		}
	}
	// 覆盖统计未知时无法确认区间仍然有效；其余覆盖问题（行全在非交易日上、超出内嵌日历）由 ReplayWindow 说明具体原因。
	if _, err := input.CoverageBounds(view, resolved, r.now()); errors.Is(err, input.ErrCoverageUnknown) {
		return input.ViewInfo{}, time.Time{}, fmt.Errorf("View %s %s重建了索引，新索引的覆盖范围暂时未知，无法确认回放区间仍然有效；请稍后重新发起回放", job.ViewID, when)
	}
	newEnd, err := input.ReplayWindow(resolved, program, view, start, end, r.now())
	var early *input.EarlyStartError
	if midRun && errors.As(err, &early) {
		// 回放过程中复查的是剩余区间，起点是本段的第一根，不是回放起点。
		label := func(at time.Time) string { return input.PeriodLabel(resolved.Calendar, resolved.Bar, at) }
		requirement := ""
		if early.History > 0 {
			requirement = fmt.Sprintf("，%s 要求它之前还有 %d 根历史", early.Source, early.History)
		}
		err = fmt.Errorf("剩余区间从 %s 起，但新索引的活跃序列从 %s 起才有数据%s（新索引的可用起点为 %s）", label(early.First), label(early.Coverage), requirement, label(early.Earliest))
	}
	if err != nil {
		return input.ViewInfo{}, time.Time{}, fmt.Errorf("View %s %s重建了索引：%w；请重新发起回放", job.ViewID, when, err)
	}
	r.logf("回放 %s：View %s %s重建了索引（→ %s），按新索引复查区间通过", job.ReplayID, job.ViewID, when, view.Generation)
	return view, newEnd, nil
}

// segmentReader 按段读取回放的行与年龄判定所需的历史，跟随活动索引换代：读取期间活动索引换了一代时，按新索引复查从本段
// 起的剩余区间，通过才让两个读取器一起切换过去重读本段，并按新索引截短终点；否则失败，不静默改读一代可能已经丢掉较早
// bar 的新索引。一次回放内最多跟随 maxIndexSwitches 次。
type segmentReader struct {
	runner   *Runner
	job      store.Replay
	resolved input.Resolved
	program  *dsl.Program
	rows     *input.RangeLoader
	history  *input.RangeLoader
	// acc 记录换代的局限说明：每次换代成功后立即写入，回放失败或取消时保存的部分指标也带着它；abort 在退避前后确认
	// 回放没有被取消。
	acc      *accumulator
	abort    func(context.Context) error
	switches int
	// end 是当前有效的终点（不含）：换代后可能被新索引截短。
	end time.Time
}

func (s *segmentReader) load(ctx context.Context, segment []input.PeriodBoundaries) (input.RangeRows, *presence, error) {
	readFrom := segment[0].StorageStart
	if s.program.UsesPreviousBar && !segment[0].PreviousStart.IsZero() {
		readFrom = segment[0].PreviousStart
	}
	revisionRereads := 0
	for {
		s.history.ExpectedRevision = 0
		rows, err := s.rows.Load(ctx, readFrom, segment[len(segment)-1].StorageStart.Add(time.Nanosecond))
		var ages *presence
		if err == nil {
			// 年龄探针固定到主数据读到的修订号：两次读取之间同一代索引发生原地补算时，丢弃整段重读，不混用两个修订号。
			s.history.ExpectedRevision = rows.Revision
			ages, err = agePresence(ctx, s.history, s.resolved, segment)
			var changedRevision *input.RevisionChangedError
			if errors.As(err, &changedRevision) {
				if revisionRereads >= maxRevisionRereads {
					return input.RangeRows{}, nil, fmt.Errorf("View %s 在回放读取期间持续有新的写入，没有读到同一修订号的数据，请稍后重新发起回放", s.job.ViewID)
				}
				revisionRereads++
				continue
			}
		}
		var changed *input.IndexChangedError
		if !errors.As(err, &changed) {
			return rows, ages, err
		}
		if s.switches >= maxIndexSwitches {
			return input.RangeRows{}, nil, fmt.Errorf("View %s 的活动索引在回放过程中持续变化，请稍后重新发起回放", s.job.ViewID)
		}
		s.switches++
		view, end, err := s.runner.recheckWindow(ctx, s.job, s.resolved, s.program, changed.View, segment[0].StorageStart, s.end, true, s.abort)
		if err != nil {
			return input.RangeRows{}, nil, err
		}
		s.rows.View, s.history.View, s.end = view, view, end
		s.acc.setNote(noteIndexSwitch, fmt.Sprintf("回放过程中 View 重建了 %d 次索引，每次都按新索引复查了剩余区间后继续", s.switches))
	}
}

// evaluate 装配一期的帧并求值。与实时一致：只有策略涉及的标的（∪E(r)，年龄剔除之前）在本期（用到 bars[-1]
// 时还有上一根）出现多个序列才整期跳过；无关标的的歧义不影响本期，只是它们的价格不用于估值。
func (r *Runner) evaluate(ctx context.Context, program *dsl.Program, resolved input.Resolved, subjects []input.Subject, members input.Membership, presence *presence, rows input.RangeRows, bar input.PeriodBoundaries, state engine.State) (engine.Decision, error) {
	key := bar.StorageStart.Unix()
	current := rows.Bars[key]
	universe := make([]string, 0, len(current))
	for subjectID := range current {
		universe = append(universe, subjectID)
	}
	sort.Strings(universe)
	sets, err := input.BuildSets(ctx, members, program.Strategy, subjects, universe)
	if err != nil {
		return engine.Decision{}, err
	}
	// 判定顺序与实时一致：当期的歧义 → 年龄探针 → 上一根的歧义。
	involved := sets.Instruments()
	if detail := firstAmbiguous(involved, rows.Ambiguous[key]); detail != "" {
		return skippedDecision(state, input.SkipAmbiguousSeries, detail), nil
	}
	var probe input.AgeProbe
	if resolved.MinAgeBars > 0 {
		probe = presence.probe(resolved, bar)
	}
	if err := sets.ApplyAge(ctx, resolved.MinAgeBars, probe); err != nil {
		var skip *input.SkipError
		if errors.As(err, &skip) {
			return skippedDecision(state, skip.Reason, skip.Detail), nil
		}
		return engine.Decision{}, err
	}
	if program.UsesPreviousBar {
		if detail := firstAmbiguous(involved, rows.Ambiguous[bar.PreviousStart.Unix()]); detail != "" {
			return skippedDecision(state, input.SkipAmbiguousSeries, "上一根："+detail), nil
		}
	}
	previous := rows.Bars[bar.PreviousStart.Unix()]
	frame := engine.Frame{BarEnd: bar.BarEnd, BarIndex: bar.BarIndex, Spot: true, Rows: make(map[string]engine.Row, len(current)), Universe: sets.Universe, Expected: sets.Expected, AgedOut: sets.AgedOut}
	for subjectID, row := range current {
		engineRow := engine.Row{Values: row.Values}
		if program.UsesPreviousBar {
			if prev, ok := previous[subjectID]; ok {
				engineRow.Previous = prev.Values
			}
		}
		frame.Rows[subjectID] = engineRow
	}
	return engine.Evaluate(program, frame, state)
}

// firstAmbiguous 返回 involved 中第一个出现多个序列的标的的说明；没有时返回空串。
func firstAmbiguous(involved []string, ambiguous map[string]string) string {
	for _, id := range involved {
		if detail, ok := ambiguous[id]; ok {
			return detail
		}
	}
	return ""
}

// skippedDecision 构造一个跳过的决策：没有目标，规则状态沿用前序。
func skippedDecision(state engine.State, reason, detail string) engine.Decision {
	return engine.Decision{Status: engine.StatusSkipped, SkipReason: reason, State: state, Summary: engine.Summary{Notes: []string{detail}}}
}

// factorDrift 比对发起回放时固化的因子指纹与执行时的定义，返回第一处差异（包括排队期间新引用或不再引用的因子）。
func factorDrift(pinned, current map[string]string) string {
	ids := make([]string, 0, len(pinned)+len(current))
	for id := range pinned {
		ids = append(ids, id)
	}
	for id := range current {
		if _, ok := pinned[id]; !ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		if pinned[id] != current[id] {
			return fmt.Sprintf("%s：%s → %s", id, pinned[id], current[id])
		}
	}
	return ""
}

func (r *Runner) writeBar(ctx context.Context, replayID string, barEnd time.Time, decision engine.Decision, ledger *Ledger, outcome Outcome, barReturn float64) error {
	targets := decision.Targets
	if decision.Status != engine.StatusOK {
		targets = nil
	}
	_, targetsJSON, err := trigger.EncodeTargets(targets)
	if err != nil {
		return err
	}
	positions, err := json.Marshal(ledger.Snapshot())
	if err != nil {
		return err
	}
	summary, err := json.Marshal(map[string]any{"skip_reason": decision.SkipReason, "decision": decision.Summary, "ledger": outcome})
	if err != nil {
		return err
	}
	frozen := 0
	for _, position := range ledger.Positions {
		if position.Frozen {
			frozen++
		}
	}
	return r.Store.AppendReplayBar(ctx, store.ReplayBar{
		ReplayID: replayID, BarEndTime: barEnd, Status: decision.Status, TargetsJSON: targetsJSON, PositionsJSON: positions, SummaryJSON: summary,
		Return: barReturn, Equity: outcome.EquityAfter, Turnover: outcome.Turnover, Fee: outcome.Fee,
		Holdings: ledger.Holdings(), Frozen: frozen, SkipReason: decision.SkipReason, Unfilled: len(outcome.Unfilled), Liquidated: len(outcome.Liquidated),
	})
}

// agePresence 为一段 bar 读取年龄判定所需的窗口 [首根 − (N+1), 末根 − (N−1)]（只读 close），
// 每段重新构造，内存只与段长和标的数有关，不随 min_age_bars 或回放长度增长。
func agePresence(ctx context.Context, history *input.RangeLoader, resolved input.Resolved, segment []input.PeriodBoundaries) (*presence, error) {
	result := newPresence()
	if resolved.MinAgeBars <= 0 {
		return result, nil
	}
	from, err := input.HistoryStart(resolved.Calendar, resolved.Bar, segment[0].StorageStart, resolved.MinAgeBars+2)
	if errors.Is(err, marketcalendar.ErrNoPreviousTradingDay) {
		// A 股内嵌日历起点附近没有目标根之前的两根容忍窗口，从目标根读起。
		from, err = input.HistoryStart(resolved.Calendar, resolved.Bar, segment[0].StorageStart, resolved.MinAgeBars)
	}
	if err != nil {
		return nil, err
	}
	to, err := input.HistoryStart(resolved.Calendar, resolved.Bar, segment[len(segment)-1].StorageStart, resolved.MinAgeBars)
	if err != nil {
		return nil, err
	}
	rows, err := history.Load(ctx, from, to.Add(time.Nanosecond))
	if err != nil {
		return nil, fmt.Errorf("读取年龄判定所需的历史：%w", err)
	}
	result.add(resolved, rows)
	return result, nil
}

func usesTags(strategy dsl.Strategy) bool {
	if len(strategy.Universe.Tags) > 0 || len(strategy.Universe.ExcludeTags) > 0 {
		return true
	}
	for _, rule := range strategy.Rules {
		if len(rule.Pool.Tags) > 0 {
			return true
		}
	}
	return false
}

// cachedMembership 在一次回放内缓存标签成员（标签只有当前成员关系）。
func cachedMembership(client input.Client, spaceID string) input.Membership {
	cache := make(map[string][]string)
	return func(ctx context.Context, tagID string) ([]string, error) {
		if members, ok := cache[tagID]; ok {
			return members, nil
		}
		members, err := client.ListTagMembers(ctx, spaceID, tagID)
		if err != nil {
			return nil, err
		}
		cache[tagID] = members
		return members, nil
	}
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok || value == "" {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *Runner) maxBars() int {
	if r.MaxBars > 0 {
		return r.MaxBars
	}
	return input.DefaultReplayMaxBars
}

func (r *Runner) chunkBars() int {
	if r.ChunkBars > 0 {
		return r.ChunkBars
	}
	return defaultChunkBars
}

func (r *Runner) liquidateAfter() int {
	if r.MissingPriceLiquidateBars > 0 {
		return r.MissingPriceLiquidateBars
	}
	return defaultLiquidateBars
}

func (r *Runner) initialEquity() float64 {
	if r.InitialEquity > 0 {
		return r.InitialEquity
	}
	return 1
}

func (r *Runner) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}
