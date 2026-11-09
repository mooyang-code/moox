package replay

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/pool"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/frequency"
)

// PoolRun 是策略池的一次历史推进（设计 3.5 预热）：复用研究回放的历史读取，按真实历史依次求值所有成员、推进成员账本，
// 每一期都调用与实时求值同一个 pool.Step。启用后的预热就是用它从启用时刻往前推进到最新已收盘的一期。
type PoolRun struct {
	Client   input.Client
	SpaceID  string
	Resolved input.Resolved
	Members  []input.PoolMember
	Programs []*dsl.Program
	Config   pool.Config
	// State 是推进的起点状态（零值表示从头开始）。
	State engine.State
	// Start 与 End 是 bar_start 的区间 [Start, End)。
	Start, End time.Time
	Now        func() time.Time
	// ChunkBars 是每段读取的 bar 数，零值取默认值。
	ChunkBars int
	Logf      func(format string, args ...any)
	// Abort 在每期之前与每次退避重读之前调用，返回非空错误时放弃（例如会话已被停用）；可以为空。
	Abort func(context.Context) error
	// IncludeLatest 为真时终点可以包含 View 里已闭合的最新一根：研究回放把最新一根当作可能还没写完而排除在外，但预热的终点之后
	// 不会再有这一根的事件（它的就绪事件在启用之前就发出了），排除它会在池的历史上留下一期空洞。调用方保证区间的终点已留过宽限。
	IncludeLatest bool
	// OnBar 非空时每推进一期调用一次（测试与观测用）。
	OnBar func(bar input.PeriodBoundaries, decision engine.Decision)
}

// PoolRunResult 是历史推进的结果。
type PoolRunResult struct {
	// State 是推进到最后一期之后的完整引擎状态；Through 是最后一期的 bar_end；Bars 是推进的期数。
	State   engine.State
	Through time.Time
	Bars    int
	// Reads 是向 Storage 读取行的次数（成员之间共享读取，同一段同一市场的同一次查询只读一次）。
	Reads int
}

// sharedReads 在一段之内共享行读取：策略池的成员读同一个 View 的同一段数据，读取的列取全体成员的并集后，每个成员的读取器发出的查询完全相同，
// 第一个成员读 Storage，其余命中缓存。每段读完调用 reset，缓存只活一段。
type sharedReads struct {
	input.Client
	mu    sync.Mutex
	cache map[string]cachedRows
	reads int
}

type cachedRows struct {
	rows     []input.Row
	revision uint64
}

func (c *sharedReads) QueryRows(ctx context.Context, spaceID string, query input.Query) ([]input.Row, uint64, error) {
	subjects := make([]string, 0, len(query.Subjects))
	for _, subject := range query.Subjects {
		subjects = append(subjects, subject.SubjectID+"\x00"+subject.SeriesTag)
	}
	key := fmt.Sprintf("%s|%d|%d|%s|%s|%d|%s", query.ViewID, query.Start.UnixNano(), query.End.UnixNano(), strings.Join(query.Columns, ","), query.ExpectedIndexID, query.ExpectedRevision, strings.Join(subjects, ","))
	c.mu.Lock()
	if hit, ok := c.cache[key]; ok {
		c.mu.Unlock()
		return hit.rows, hit.revision, nil
	}
	c.mu.Unlock()
	rows, revision, err := c.Client.QueryRows(ctx, spaceID, query)
	if err != nil {
		return nil, 0, err
	}
	c.mu.Lock()
	if c.cache == nil {
		c.cache = map[string]cachedRows{}
	}
	c.cache[key] = cachedRows{rows: rows, revision: revision}
	c.reads++
	c.mu.Unlock()
	return rows, revision, nil
}

func (c *sharedReads) reset() {
	c.mu.Lock()
	c.cache = nil
	c.mu.Unlock()
}

// poolMemberRun 是一个成员在历史推进里的读取与求值上下文：成员自己的绑定与程序、读取器（年龄判定的历史读取与已通过集合各自独立）。
type poolMemberRun struct {
	id       string
	resolved input.Resolved
	program  *dsl.Program
	readers  []*marketReader
	reader   *segmentReader
	segments map[string]marketSegment
}

// poolPass 是策略池按历史逐期推进的公共部分，预热（RunPool）与策略池的研究回放（Runner.replayPool）共用：每段先读全部成员的行
// （成员之间共享读取），再逐期求值所有成员、调用与实时求值同一个 pool.Step。
type poolPass struct {
	cfg        pool.Config
	resolved   input.Resolved
	members    []*poolMemberRun
	shared     *sharedReads
	membership input.Membership
	// funding 是池 DSL 的 fees.funding_column（成员账本与池回放的池级账本都按它结算资金费）；prices 只带市场名，按市场取本段的行。
	funding string
	prices  []*marketReader
	// segments 是当前段各市场的行（取第一个读这个市场的成员读到的，成员之间读到的相同），估值与资金费按它取。
	segments map[string]marketSegment
	// group 非空时读取期间的索引换代由它统一跟随（策略池回放）；为空时不跟随（预热：换代后由调用方整体重试）。
	group *readerGroup
}

// newPoolPass 构造各成员的读取器：读取的列取全体成员的并集，每个成员的行读取器发出完全相同的查询，成员之间经 shared 共享读取；
// 写了 fees.funding_column 时永续的行另读这一列。follow 为真时读取期间的索引换代按整个池跟随（见 readerGroup），否则遇到换代直接失败。
func newPoolPass(shared *sharedReads, spaceID string, resolved input.Resolved, members []input.PoolMember, programs []*dsl.Program, cfg pool.Config,
	views map[string]input.ViewInfo, subjects map[string][]input.Subject, job store.Replay, runner *Runner, end time.Time, acc *accumulator, abort func(context.Context) error, follow bool) *poolPass {
	columns := []string{"close"}
	for _, program := range programs {
		columns = append(columns, program.Columns...)
		columns = append(columns, program.PreviousColumns...)
	}
	columns = uniqueStrings(columns)
	fundingColumn := cfg.Spec.Fees.FundingColumn
	columnsOf := func(market string) []string {
		if fundingColumn == "" || market != dsl.MarketSwap {
			return columns
		}
		return uniqueStrings(append(append([]string(nil), columns...), fundingColumn))
	}
	pass := &poolPass{cfg: cfg, resolved: resolved, shared: shared, membership: cachedMembership(shared, spaceID), funding: fundingColumn}
	if follow {
		pass.group = &readerGroup{end: end, acc: acc}
	}
	switches := maxIndexSwitches
	if follow {
		switches = 0
	}
	for i, member := range members {
		item := &poolMemberRun{id: member.ID, resolved: member.Resolved, program: programs[i]}
		for _, market := range member.Resolved.Markets() {
			view := views[market]
			item.readers = append(item.readers, newMarketReader(market, job.Tasks[market], subjects[market],
				&input.RangeLoader{Client: shared, SpaceID: spaceID, View: view, Subjects: subjects[market], Columns: columnsOf(market), Abort: abort},
				&input.RangeLoader{Client: shared, SpaceID: spaceID, View: view, Subjects: subjects[market], Columns: []string{"close"}, Abort: abort}))
		}
		item.reader = &segmentReader{runner: runner, job: job, resolved: item.resolved, program: item.program, end: end, acc: acc, abort: abort, markets: item.readers, switches: switches, group: pass.group}
		pass.members = append(pass.members, item)
		if pass.group != nil {
			pass.group.readers = append(pass.group.readers, item.reader)
		}
	}
	for _, market := range resolved.Markets() {
		pass.prices = append(pass.prices, &marketReader{market: market})
	}
	return pass
}

// load 读一段：每个成员读自己市场的行与年龄判定的历史（成员之间共享读取，读完清掉缓存）。
func (p *poolPass) load(ctx context.Context, segment []input.PeriodBoundaries) error {
	p.segments = make(map[string]marketSegment, len(p.resolved.Tasks))
	for _, member := range p.members {
		var err error
		if member.segments, err = member.reader.load(ctx, segment); err != nil {
			return fmt.Errorf("成员 %s：%w", member.id, err)
		}
		for market, loaded := range member.segments {
			if _, ok := p.segments[market]; !ok {
				p.segments[market] = loaded
			}
		}
	}
	p.shared.reset()
	return nil
}

// end 是当前有效的终点（不含）：读取期间跟随换代后可能被新索引截短，取各成员中最早的。
func (p *poolPass) end() time.Time {
	end := time.Time{}
	for _, member := range p.members {
		if end.IsZero() || member.reader.end.Before(end) {
			end = member.reader.end
		}
	}
	return end
}

// step 推进一期：成员本期求值被跳过（缺数、守门等）记为没通过，沿用上一期的目标权重，与实时一致；所有成员都被跳过的期，池 skipped、
// 状态不变。返回池的决策，以及这一期的收盘价与资金费率（池回放的池级账本也按它们估值、结算）。
func (p *poolPass) step(ctx context.Context, bar input.PeriodBoundaries, state engine.State) (engine.Decision, map[string]float64, map[string]float64, error) {
	results := make([]pool.MemberResult, 0, len(p.members))
	for _, member := range p.members {
		decision, err := evaluateBar(ctx, member.program, member.resolved, member.readers, nil, p.membership, member.segments, bar, state.Members[member.id])
		if err != nil {
			return engine.Decision{}, nil, nil, fmt.Errorf("成员 %s：%w", member.id, err)
		}
		results = append(results, pool.MemberResult{ID: member.id, Decision: &decision})
	}
	key := bar.StorageStart.Unix()
	prices, funding := barPrices(p.prices, p.segments, key), barFunding(p.prices, p.segments, key, p.funding)
	decision, err := pool.Step(p.cfg, state, pool.Input{BarIndex: bar.BarIndex, Prices: prices, Funding: funding, Members: results})
	if err != nil {
		return engine.Decision{}, nil, nil, fmt.Errorf("%s：%w", input.BarEndLabel(p.resolved.Calendar, p.resolved.Bar, bar.BarEnd), err)
	}
	return decision, prices, funding, nil
}

// readerGroup 让策略池回放的全部成员一起跟随索引换代：一次回放累计最多跟随 maxIndexSwitches 次（整个池共用一个计数）；某个成员读到换代时，
// 按每个用到这个市场的成员各自的回看要求依次复查剩余区间，通过后把同一任务键上所有成员的读取器都切到新索引、清掉它们的年龄判定结果，
// 终点取复查后最早的，其余成员不会再为同一次换代复查一遍。
type readerGroup struct {
	readers  []*segmentReader
	switches int
	end      time.Time
	acc      *accumulator
}

func (g *readerGroup) follow(ctx context.Context, taskKey string, changed input.ViewInfo, from time.Time) error {
	if g.switches >= maxIndexSwitches {
		return fmt.Errorf("View %s 的活动索引在回放过程中持续变化，请稍后重新发起回放", changed.ViewID)
	}
	g.switches++
	view := changed
	for _, reader := range g.readers {
		if !reader.usesTask(taskKey) {
			continue
		}
		var err error
		if view, g.end, err = reader.runner.recheckView(ctx, reader.job, taskKey, reader.resolved, reader.program, view, from, g.end, true, reader.abort); err != nil {
			return err
		}
	}
	for _, reader := range g.readers {
		reader.end = g.end
		for _, market := range reader.markets {
			if reader.resolved.TaskKey(market.market) == taskKey {
				market.rows.View, market.history.View = view, view
				clear(market.passed)
			}
		}
	}
	g.acc.setNote(noteIndexSwitch, fmt.Sprintf("回放过程中 View 重建了 %d 次索引，每次都按新索引复查了剩余区间后继续", g.switches))
	return nil
}

// RunPool 执行历史推进（逐期逻辑见 poolPass.step）。读取期间活动索引换了一代时不跟随，直接返回错误，由调用方整体重试。
func RunPool(ctx context.Context, run PoolRun) (PoolRunResult, error) {
	if run.Client == nil {
		return PoolRunResult{}, errors.New("Storage 与 Factor 依赖未配置，不能推进策略池")
	}
	if len(run.Members) == 0 || len(run.Members) != len(run.Programs) {
		return PoolRunResult{}, errors.New("策略池的成员与程序不一致")
	}
	shared := &sharedReads{Client: run.Client}
	runner := &Runner{Client: shared, Now: run.Now, Logf: run.Logf}
	resolved := run.Resolved
	views, subjects, err := loadMarketViews(ctx, shared, run.SpaceID, resolved)
	if err != nil {
		return PoolRunResult{}, err
	}
	if err := checkFundingColumn(views, run.Config.Spec.Fees.FundingColumn); err != nil {
		return PoolRunResult{}, err
	}
	end, err := run.checkWindow(ctx, views)
	if err != nil {
		return PoolRunResult{}, err
	}
	bars, err := input.ReplayBars(resolved.Calendar, resolved.Bar, run.Start, end, runner.maxBars())
	if err != nil {
		return PoolRunResult{}, err
	}
	if len(bars) == 0 {
		return PoolRunResult{}, errors.New("历史推进的区间内没有完整的 bar")
	}
	abort := run.Abort
	if abort == nil {
		abort = func(context.Context) error { return nil }
	}
	job := store.Replay{ReplayID: "pool", SpaceID: run.SpaceID, Tasks: make(map[string]store.ReplayTask, len(resolved.Tasks))}
	for market, task := range resolved.Tasks {
		job.Tasks[market] = store.ReplayTask{FactorTaskID: task.FactorTaskID, ViewID: task.ViewID, ViewGeneration: views[market].Generation}
	}
	pass := newPoolPass(shared, run.SpaceID, resolved, run.Members, run.Programs, run.Config, views, subjects, job, runner, end, newAccumulator(1, 1, 1), abort, false)
	chunk := runner.chunkBars()
	state := run.State
	result := PoolRunResult{}
	for start := 0; start < len(bars); start += chunk {
		segment := bars[start:min(start+chunk, len(bars))]
		if err := pass.load(ctx, segment); err != nil {
			return PoolRunResult{}, err
		}
		for _, bar := range segment {
			if err := abort(ctx); err != nil {
				return PoolRunResult{}, err
			}
			decision, _, _, err := pass.step(ctx, bar, state)
			if err != nil {
				return PoolRunResult{}, err
			}
			state = decision.State
			result.Through, result.Bars = bar.BarEnd, result.Bars+1
			if run.OnBar != nil {
				run.OnBar(bar, decision)
			}
		}
	}
	result.State, result.Reads = state, shared.reads
	return result, nil
}

// replayPool 执行策略池的研究回放（设计 V1）：成员取回放记录里固化的成员快照（不重读成员定义），按快照里的 DSL 重新解析绑定并核对因子指纹；
// 逐期复用预热的推进逻辑（poolPass），合成后的池目标由一个池级账本承接（费率用回放任务的，资金费按池 DSL 的 fees.funding_column）；
// 进度写入、换代截断与指标累计与单策略回放一致。回放从空状态开始，与从同一起点开始的预热逐期一致。
func (r *Runner) replayPool(ctx context.Context, job store.Replay, strategy dsl.Strategy) (Metrics, error) {
	defs, err := input.PoolMemberDefs(job.MembersJSON)
	if err != nil {
		return Metrics{}, fmt.Errorf("策略池回放：%w", err)
	}
	tasks := replayTaskIDs(job)
	pooled, err := input.ResolvePool(ctx, r.Client, job.SpaceID, input.TaskIDs(tasks), strategy, defs)
	if err != nil {
		return Metrics{}, err
	}
	resolved := pooled.Resolved
	if err := input.CheckResolvedMarkets(resolved, tasks); err != nil {
		return Metrics{}, err
	}
	if drift := factorDrift(job.Factors, resolved.Factors); drift != "" {
		return Metrics{}, fmt.Errorf("因子定义在排队期间发生变化（%s），请重新发起回放", drift)
	}
	clock, err := input.PoolClock(resolved.Calendar, resolved.Bar, *strategy.Pool)
	if err != nil {
		return Metrics{}, err
	}
	cfg, err := pool.NewConfig(strategy, resolved.Market, input.MemberStrategies(pooled.Members, pooled.Programs), clock)
	if err != nil {
		return Metrics{}, err
	}
	shared := &sharedReads{Client: r.Client}
	views, subjects, err := loadMarketViews(ctx, shared, job.SpaceID, resolved)
	if err != nil {
		return Metrics{}, err
	}
	if err := checkFundingColumn(views, cfg.Spec.Fees.FundingColumn); err != nil {
		return Metrics{}, err
	}
	abort := r.abortHook(job.ReplayID)
	end := job.EndTime
	if views, end, err = r.recheckPoolWindow(ctx, job, pooled, views, job.StartTime, end, abort); err != nil {
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
	acc := r.newAccumulator(resolved, freq.NominalDuration(), job, end, bars)
	acc.metrics.Factors = resolved.Factors
	tags := false
	for _, program := range pooled.Programs {
		tags = tags || usesTags(program.Strategy)
	}
	acc.metrics.Limitations = append(limitations(r.liquidateAfter(), tags, resolved.Market.Has(dsl.MarketSwap), cfg.Spec.Fees.FundingColumn),
		"策略池：成员按回放发起时固化的成员版本求值，成员账本是按收盘价成交的模拟曲线（费率用池 DSL 的 fees），只用来分配资金；池目标由池级账本按回放任务的费率承接")
	pass := newPoolPass(shared, job.SpaceID, resolved, pooled.Members, pooled.Programs, cfg, views, subjects, job, r, end, acc, abort, true)
	book := r.newBook(job, resolved, acc)
	state := engine.State{}
	chunk := r.chunkBars()
	for start := 0; start < len(bars); start += chunk {
		segment := bars[start:min(start+chunk, len(bars))]
		if err := pass.load(ctx, segment); err != nil {
			return acc.finish(), err
		}
		bars, segment = truncateBars(bars, start, chunk, pass.end(), resolved, acc)
		for _, bar := range segment {
			if err := abort(ctx); err != nil {
				return acc.finish(), err
			}
			decision, prices, funding, err := pass.step(ctx, bar, state)
			if err != nil {
				return acc.finish(), err
			}
			if err := book.settle(ctx, bar, decision, prices, funding); err != nil {
				return acc.finish(), err
			}
			state = decision.State
		}
		if err := r.Store.UpdateReplayProgress(ctx, job.ReplayID, segment[len(segment)-1].BarEnd, r.now()); err != nil {
			return acc.finish(), err
		}
	}
	return acc.finish(), nil
}

// recheckPoolWindow 是策略池回放开始前的复查（排队期间可能换了索引或定时删除了最旧数据）：每个市场按每个用到它的成员各自的回看要求
// 依次复查起点并截断终点（见 recheckView），复查后的 View 由这些成员共用；终点取各市场、各成员中最早的。
func (r *Runner) recheckPoolWindow(ctx context.Context, job store.Replay, pooled input.PoolResolved, views map[string]input.ViewInfo, start, end time.Time, abort func(context.Context) error) (map[string]input.ViewInfo, time.Time, error) {
	checked := make(map[string]input.ViewInfo, len(views))
	for _, market := range pooled.Resolved.Markets() {
		view := views[market]
		for i, member := range pooled.Members {
			if _, ok := member.Resolved.Tasks[market]; !ok {
				continue
			}
			var err error
			if view, end, err = r.recheckView(ctx, job, market, member.Resolved, pooled.Programs[i], view, start, end, false, abort); err != nil {
				return nil, time.Time{}, fmt.Errorf("成员 %s：%w", member.ID, err)
			}
		}
		checked[market] = view
	}
	return checked, end, nil
}

// checkWindow 复查历史推进的区间（见 input.PoolWindow）。
func (run PoolRun) checkWindow(ctx context.Context, views map[string]input.ViewInfo) (time.Time, error) {
	now := time.Now().UTC()
	if run.Now != nil {
		now = run.Now().UTC()
	}
	return input.PoolWindow(ctx, run.Client, run.SpaceID, run.Resolved, run.Members, run.Programs, views, run.Start, run.End, now, run.IncludeLatest)
}
