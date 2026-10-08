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
)

const (
	defaultChunkBars     = 50
	defaultLiquidateBars = 3
	pollInterval         = 30 * time.Second
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
			if err := r.Store.FinishReplay(ctx, job.ReplayID, store.ReplayFailed, nil, "回放执行异常，请查看策略模块日志", r.now()); err != nil && !errors.Is(err, store.ErrNotFound) {
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
		// 记录里只保存不含服务地址的概述，原始错误写日志。
		status, message = store.ReplayFailed, err.Error()
		r.logf("回放 %s 失败：%v；原始错误：%v", job.ReplayID, err, input.RawCause(err))
	}
	if finishErr := r.Store.FinishReplay(ctx, job.ReplayID, status, raw, message, r.now()); finishErr != nil && !errors.Is(finishErr, store.ErrNotFound) {
		r.logf("写入回放 %s 的终态失败：%v", job.ReplayID, finishErr)
	}
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
	instrumentOf := make(map[string]string, len(bindings))
	for _, subject := range bindings {
		subject.Active = true
		subjects = append(subjects, subject)
		instrumentOf[subject.SubjectID] = subject.InstrumentID
	}
	if len(subjects) == 0 {
		return Metrics{}, fmt.Errorf("View %s 的数据集没有标的绑定", job.ViewID)
	}
	// 排队期间 View 可能已推进或重建：执行时复查起点并重新截断终点。
	end, err := input.ReplayWindow(resolved, program, view, job.StartTime, job.EndTime)
	if err != nil {
		return Metrics{}, err
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
	columns := uniqueStrings(append(append([]string{"close"}, program.Columns...), program.PreviousColumns...))
	loader := &input.RangeLoader{Client: r.Client, SpaceID: job.SpaceID, View: view, Subjects: subjects, Columns: columns}
	members := cachedMembership(r.Client, job.SpaceID)
	history := &input.RangeLoader{Client: r.Client, SpaceID: job.SpaceID, View: view, Subjects: subjects, Columns: []string{"close"}}
	ledger := NewLedger(r.initialEquity(), job.FeeBps, r.liquidateAfter())
	acc := newAccumulator(r.initialEquity(), periodsPerYear(resolved.Calendar, freq.NominalDuration()), freq.NominalDuration())
	acc.metrics.Factors = resolved.Factors
	acc.metrics.Limitations = limitations(r.liquidateAfter(), usesTags(strategy))
	state := engine.State{}
	previousEquity := r.initialEquity()
	chunk := r.chunkBars()
	for start := 0; start < len(bars); start += chunk {
		end := min(start+chunk, len(bars))
		segment := bars[start:end]
		readFrom := segment[0].StorageStart
		if program.UsesPreviousBar {
			readFrom = segment[0].PreviousStart
		}
		rows, err := loader.Load(ctx, readFrom, segment[len(segment)-1].StorageStart.Add(time.Nanosecond))
		if err != nil {
			return acc.finish(), err
		}
		presence, err := agePresence(ctx, history, resolved, segment)
		if err != nil {
			return acc.finish(), err
		}
		for _, bar := range segment {
			if status, err := r.Store.ReplayStatus(ctx, job.ReplayID); err != nil {
				return acc.finish(), err
			} else if status == store.ReplayCancelled {
				return acc.finish(), errCancelled
			}
			decision, err := r.evaluate(ctx, program, resolved, subjects, instrumentOf, members, presence, rows, bar, state)
			if err != nil {
				return acc.finish(), err
			}
			// 同一标的同一周期有多个序列时，这根 bar 上它的价格不可信，按缺价处理。
			key := bar.StorageStart.Unix()
			prices := make(map[string]float64)
			for subjectID, row := range rows.Bars[key] {
				if _, ambiguous := rows.AmbiguousSubjects[key][subjectID]; ambiguous {
					continue
				}
				if price, ok := row.Values["close"]; ok && price > 0 {
					prices[instrumentOf[subjectID]] = price
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
			acc.add(bar.BarEnd, ok, decision.SkipReason, outcome, ledger.Holdings())
			barReturn := 0.0
			if previousEquity > 0 {
				barReturn = outcome.EquityAfter/previousEquity - 1
			}
			previousEquity = outcome.EquityAfter
			if err := r.writeBar(ctx, job.ReplayID, bar.BarEnd, decision, ledger, outcome, barReturn); err != nil {
				return acc.finish(), err
			}
		}
		if err := r.Store.UpdateReplayProgress(ctx, job.ReplayID, segment[len(segment)-1].BarEnd, r.now()); err != nil {
			return acc.finish(), err
		}
	}
	return acc.finish(), nil
}

// evaluate 装配一期的帧并求值；同一标的同一周期有多个序列时整期跳过。
func (r *Runner) evaluate(ctx context.Context, program *dsl.Program, resolved input.Resolved, subjects []input.Subject, instrumentOf map[string]string, members input.Membership, presence *presence, rows input.RangeRows, bar input.PeriodBoundaries, state engine.State) (engine.Decision, error) {
	key := bar.StorageStart.Unix()
	if detail, ambiguous := rows.Ambiguous[key]; ambiguous {
		return skippedDecision(state, input.SkipAmbiguousSeries, detail), nil
	}
	// 与实时一致：用到 bars[-1] 时，上一根的歧义同样让本期跳过。
	if program.UsesPreviousBar {
		if detail, ambiguous := rows.Ambiguous[bar.PreviousStart.Unix()]; ambiguous {
			return skippedDecision(state, input.SkipAmbiguousSeries, "上一根："+detail), nil
		}
	}
	current := rows.Bars[key]
	universe := make([]string, 0, len(current))
	for subjectID := range current {
		universe = append(universe, subjectID)
	}
	sort.Strings(universe)
	var probe input.AgeProbe
	if resolved.MinAgeBars > 0 {
		probe = presence.probe(resolved, bar, instrumentOf)
	}
	sets, err := input.BuildSets(ctx, members, program.Strategy, subjects, universe)
	if err != nil {
		return engine.Decision{}, err
	}
	if err := sets.ApplyAge(ctx, resolved.MinAgeBars, probe); err != nil {
		var skip *input.SkipError
		if errors.As(err, &skip) {
			return skippedDecision(state, skip.Reason, skip.Detail), nil
		}
		return engine.Decision{}, err
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
		frame.Rows[instrumentOf[subjectID]] = engineRow
	}
	return engine.Evaluate(program, frame, state)
}

// skippedDecision 构造一个跳过的决策：没有目标，规则状态沿用前序。
func skippedDecision(state engine.State, reason, detail string) engine.Decision {
	return engine.Decision{Status: engine.StatusSkipped, SkipReason: reason, State: state, Summary: engine.Summary{Notes: []string{detail}}}
}

// factorDrift 比对发起回放时固化的因子指纹与执行时的定义，返回第一处差异；没有固化指纹时不比对。
func factorDrift(pinned, current map[string]string) string {
	if len(pinned) == 0 {
		return ""
	}
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
	if err := result.add(resolved, rows); err != nil {
		return nil, err
	}
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
