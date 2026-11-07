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

// RunPool 执行历史推进。成员本期求值被跳过（缺数、守门等）记为没通过，沿用上一期的目标权重，与实时一致；
// 所有成员都被跳过的期，池 skipped、状态不变。读取期间活动索引换了一代时不跟随，直接返回错误，由调用方整体重试。
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
	// 读取的列取全体成员的并集：每个成员的行读取器发出完全相同的查询，成员之间共享读取。
	columns := []string{"close"}
	for _, program := range run.Programs {
		columns = append(columns, program.Columns...)
		columns = append(columns, program.PreviousColumns...)
	}
	columns = uniqueStrings(columns)
	job := store.Replay{ReplayID: "pool", SpaceID: run.SpaceID, Tasks: make(map[string]store.ReplayTask, len(resolved.Tasks))}
	for market, task := range resolved.Tasks {
		job.Tasks[market] = store.ReplayTask{FactorTaskID: task.FactorTaskID, ViewID: task.ViewID, ViewGeneration: views[market].Generation}
	}
	members := make([]*poolMemberRun, 0, len(run.Members))
	for i, member := range run.Members {
		item := &poolMemberRun{id: member.ID, resolved: member.Resolved, program: run.Programs[i]}
		for _, market := range member.Resolved.Markets() {
			view := views[market]
			item.readers = append(item.readers, &marketReader{
				market: market, task: job.Tasks[market], subjects: subjects[market], passed: make(map[string]struct{}),
				rows:    &input.RangeLoader{Client: shared, SpaceID: run.SpaceID, View: view, Subjects: subjects[market], Columns: columns, Abort: abort},
				history: &input.RangeLoader{Client: shared, SpaceID: run.SpaceID, View: view, Subjects: subjects[market], Columns: []string{"close"}, Abort: abort},
			})
		}
		item.reader = &segmentReader{runner: runner, job: job, resolved: item.resolved, program: item.program, end: end, acc: newAccumulator(1, 1, 1), abort: abort, markets: item.readers, switches: maxIndexSwitches}
		members = append(members, item)
	}
	membership := cachedMembership(shared, run.SpaceID)
	chunk := runner.chunkBars()
	state := run.State
	result := PoolRunResult{}
	for start := 0; start < len(bars); start += chunk {
		segment := bars[start:min(start+chunk, len(bars))]
		priceSegments := make(map[string]marketSegment, len(resolved.Tasks))
		for _, member := range members {
			if member.segments, err = member.reader.load(ctx, segment); err != nil {
				return PoolRunResult{}, fmt.Errorf("成员 %s：%w", member.id, err)
			}
			for market, loaded := range member.segments {
				if _, ok := priceSegments[market]; !ok {
					priceSegments[market] = loaded
				}
			}
		}
		shared.reset()
		priceReaders := make([]*marketReader, 0, len(priceSegments))
		for _, market := range resolved.Markets() {
			priceReaders = append(priceReaders, &marketReader{market: market})
		}
		for _, bar := range segment {
			if err := abort(ctx); err != nil {
				return PoolRunResult{}, err
			}
			results := make([]pool.MemberResult, 0, len(members))
			for _, member := range members {
				decision, err := evaluateBar(ctx, member.program, member.resolved, member.readers, membership, member.segments, bar, state.Members[member.id])
				if err != nil {
					return PoolRunResult{}, fmt.Errorf("成员 %s：%w", member.id, err)
				}
				results = append(results, pool.MemberResult{ID: member.id, Decision: &decision})
			}
			decision, err := pool.Step(run.Config, state, pool.Input{BarIndex: bar.BarIndex, Prices: barPrices(priceReaders, priceSegments, bar.StorageStart.Unix()), Members: results})
			if err != nil {
				return PoolRunResult{}, fmt.Errorf("%s：%w", input.BarEndLabel(resolved.Calendar, resolved.Bar, bar.BarEnd), err)
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

// checkWindow 复查历史推进的区间（见 input.PoolWindow）。
func (run PoolRun) checkWindow(ctx context.Context, views map[string]input.ViewInfo) (time.Time, error) {
	now := time.Now().UTC()
	if run.Now != nil {
		now = run.Now().UTC()
	}
	return input.PoolWindow(ctx, run.Client, run.SpaceID, run.Resolved, run.Members, run.Programs, views, run.Start, run.End, now, run.IncludeLatest)
}
