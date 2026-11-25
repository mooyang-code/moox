package replay

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/factorname"
)

// barSource 是回放主帧的输入来源：给出一段连续 bar 的行与年龄判定所需的历史，返回值按市场分。主循环、evaluateBar 与账本
// 不关心来源。View 来源（viewSource）一次返回一整段，每个市场一个 View；
// nextParams 是上一根策略求出的参数，留给按参数现算的来源，View 来源不用它。
type barSource interface {
	Load(ctx context.Context, bars []input.PeriodBoundaries, nextParams engine.ParamSet) (map[string]marketSegment, error)
	// End 是当前有效的终点（不含）：来源在读取过程中可能截短它（例如 View 重建了索引，按新索引复查剩余区间）。
	End() time.Time
	// Applied 是这一根（bar_start 的 Unix 秒）因子实际生效的、由回放逐期提交的可变成员参数，写进每期记录；
	// View 来源没有逐期提交的参数，返回 nil。
	Applied(key int64) engine.ParamSet
	// Rejected 是这一根被因子引擎拒绝的逐期提交参数的说明（成员与原因），并进这一期摘要的 notes；View 来源返回 nil。
	Rejected(key int64) []string
	Close(ctx context.Context) error
}

// marketSegment 是一个市场一段读取的结果：行与年龄判定的依据，以及各附加来源这一段的行（来源名 → 行）。
type marketSegment struct {
	rows    input.RangeRows
	ages    ageProber
	sources map[string]input.RangeRows
}

// ageProber 给出一根 bar 判定 n 根年龄的探针：View 来源按读到的历史行（presence），“因子现算”来源按引擎给的根数（engineAges）。
type ageProber interface {
	probe(n int, bar input.PeriodBoundaries) input.AgeProbe
}

// replayMarket 是回放的一个市场：市场名、数据集的全部标的绑定（含已停用，回放中都视为活跃）与列出的标的（年龄判定用，
// 构造时算一次）。与输入来源无关，求值与估值只用它。
type replayMarket struct {
	market   string
	subjects []input.Subject
	listed   map[string]struct{}
}

func newReplayMarket(market string, subjects []input.Subject) *replayMarket {
	listed := make(map[string]struct{}, len(subjects))
	for _, subject := range subjects {
		listed[subject.SubjectID] = struct{}{}
	}
	return &replayMarket{market: market, subjects: subjects, listed: listed}
}

// viewMarket 是 View 来源里的一个市场：这个市场结果 View 的行读取器、年龄判定用的历史读取器与各附加来源的行读取器。
type viewMarket struct {
	*replayMarket
	task    store.ReplayTask
	rows    *input.RangeLoader
	history *input.RangeLoader
	// passed 是整次回放内已通过年龄判定的标的（按判定根数 N 分开），各段共用（与实时一致：通过后不再因窗口缺口被重新剔除）。
	passed map[int]map[string]struct{}
	// sources 是各附加来源的行读取器（来源名 → 读取器）：每段按 as-of 读来源应读的那几根，并进主帧的行。
	sources map[string]*input.RangeLoader
}

func newViewMarket(market *replayMarket, task store.ReplayTask, rows, history *input.RangeLoader) *viewMarket {
	return &viewMarket{replayMarket: market, task: task, rows: rows, history: history, passed: make(map[int]map[string]struct{})}
}

// viewSource 是读 View 的输入来源：按段读取各市场回放的行与年龄判定所需的历史，跟随活动索引换代。某个市场读取期间活动索引换了
// 一代时，按新索引复查从本段起的剩余区间，通过才让这个市场的两个读取器一起切换过去、整段重读，并按新索引截短终点；否则失败，
// 不静默改读一代可能已经丢掉较早 bar 的新索引。一次回放内最多跟随 maxIndexSwitches 次（各市场合计）。
type viewSource struct {
	runner   *Runner
	job      store.Replay
	resolved input.Resolved
	program  *dsl.Program
	markets  []*viewMarket
	// refs 是参考市场的读取器：只读年龄判定的历史，不读行。
	refs []*viewMarket
	// acc 记录换代的局限说明：每次换代成功后立即写入，回放失败或取消时保存的部分指标也带着它；abort 在退避前后确认
	// 回放没有被取消。
	acc      *accumulator
	abort    func(context.Context) error
	switches int
	// end 是当前有效的终点（不含）：换代后可能被新索引截短。
	end time.Time
	// group 非空时换代由它统一跟随（策略池回放的全部成员共用一个计数，一起切换），switches 不用。
	group *readerGroup
}

// mainColumns 返回读主帧 View 的行要取的列：close（估值用）、当期列、bars[-1] 列；规则或参数表达式用了 params() 时
// 另取 __params 列（实时也读它），没用时不取，查询与改造前相同。
func mainColumns(program *dsl.Program) []string {
	columns := append(append([]string{"close"}, program.MainColumns()...), program.PreviousColumns...)
	if len(program.ParamRefs) > 0 {
		columns = append(columns, factorname.ParamsColumn)
	}
	return uniqueStrings(columns)
}

// newViewSource 构造单策略回放的 View 来源：每个市场一个行读取器与一个年龄判定的历史读取器，加上附加来源与参考市场的读取器。
// 计资金费时永续的行另读费率列。
func (r *Runner) newViewSource(ctx context.Context, job store.Replay, resolved input.Resolved, program *dsl.Program, views map[string]input.ViewInfo,
	markets []*replayMarket, end time.Time, acc *accumulator, abort func(context.Context) error) (*viewSource, error) {
	columns := mainColumns(program)
	columnsOf := func(market string) []string {
		if job.FundingColumn == "" || market != dsl.MarketSwap {
			return columns
		}
		return uniqueStrings(append(append([]string(nil), columns...), job.FundingColumn))
	}
	source := &viewSource{runner: r, job: job, resolved: resolved, program: program, end: end, acc: acc, abort: abort}
	for _, market := range markets {
		view := views[market.market]
		source.markets = append(source.markets, newViewMarket(market, job.Tasks[market.market],
			&input.RangeLoader{Client: r.Client, SpaceID: job.SpaceID, View: view, Subjects: market.subjects, Columns: columnsOf(market.market), Abort: abort},
			&input.RangeLoader{Client: r.Client, SpaceID: job.SpaceID, View: view, Subjects: market.subjects, Columns: []string{"close"}, Abort: abort}))
	}
	if err := attachSources(ctx, r.Client, job.SpaceID, resolved, program, source.markets, abort); err != nil {
		return nil, err
	}
	refs, err := referenceReaders(ctx, r.Client, job, resolved, abort)
	if err != nil {
		return nil, err
	}
	source.refs = refs
	return source, nil
}

// references 返回参考市场（只参与 age_markets: all 的年龄判定）。
func (s *viewSource) references() []*replayMarket {
	refs := make([]*replayMarket, 0, len(s.refs))
	for _, ref := range s.refs {
		refs = append(refs, ref.replayMarket)
	}
	return refs
}

func (s *viewSource) End() time.Time { return s.end }

// Applied 对 View 来源总是 nil：View 的行按当时提交的参数算好，回放不逐期提交参数。
func (s *viewSource) Applied(int64) engine.ParamSet { return nil }

// Rejected 对 View 来源总是 nil。
func (s *viewSource) Rejected(int64) []string { return nil }

// Close 无事可做：读取器不持有连接。
func (s *viewSource) Close(context.Context) error { return nil }

// usesTask 报告这个来源有没有读任务键（市场）taskKey 上的 View。
func (s *viewSource) usesTask(taskKey string) bool {
	for _, market := range s.markets {
		if market.market == taskKey {
			return true
		}
	}
	return false
}

// switchView 把任务键（市场）taskKey 的行与历史读取器切到新索引的 View，并清掉它的年龄判定结果：年龄证明属于旧索引，与实时的
// 缓存一致，换代后重新判定。
func (s *viewSource) switchView(taskKey string, view input.ViewInfo) {
	for _, market := range s.markets {
		if market.market == taskKey {
			market.rows.View, market.history.View = view, view
			clear(market.passed)
		}
	}
}

// Load 读一段 bar（View 来源不用 nextParams）。
func (s *viewSource) Load(ctx context.Context, segment []input.PeriodBoundaries, _ engine.ParamSet) (map[string]marketSegment, error) {
	readFrom := segment[0].StorageStart
	if s.program.UsesPreviousBar && !segment[0].PreviousStart.IsZero() {
		readFrom = segment[0].PreviousStart
	}
	revisionRereads := 0
reload:
	for {
		loaded := make(map[string]marketSegment, len(s.markets))
		for _, market := range s.markets {
			market.history.ExpectedRevision = 0
			rows, err := market.rows.Load(ctx, readFrom, segment[len(segment)-1].StorageStart.Add(time.Nanosecond))
			var ages *presence
			if err == nil {
				// 年龄探针固定到主数据读到的修订号：两次读取之间同一代索引发生原地补算时，丢弃整段重读，不混用两个修订号。
				market.history.ExpectedRevision = rows.Revision
				ages, err = agePresence(ctx, market.history, s.resolved, segment, market.passed)
				var changedRevision *input.RevisionChangedError
				if errors.As(err, &changedRevision) {
					if revisionRereads >= maxRevisionRereads {
						return nil, fmt.Errorf("View %s 在回放读取期间持续有新的写入，没有读到同一修订号的数据，请稍后重新发起回放", market.task.DatasetID)
					}
					revisionRereads++
					continue reload
				}
			}
			var sources map[string]input.RangeRows
			if err == nil {
				sources, err = loadSourceSegment(ctx, s.resolved, market, segment)
			}
			var changed *input.IndexChangedError
			if !errors.As(err, &changed) {
				if err != nil {
					return nil, err
				}
				loaded[market.market] = marketSegment{rows: rows, ages: ages, sources: sources}
				continue
			}
			if s.group != nil {
				if err := s.group.follow(ctx, market.market, changed.View, segment[0].StorageStart); err != nil {
					return nil, err
				}
				continue reload
			}
			if s.switches >= maxIndexSwitches {
				return nil, fmt.Errorf("View %s 的活动索引在回放过程中持续变化，请稍后重新发起回放", market.task.DatasetID)
			}
			s.switches++
			taskKey := market.market
			view, end, err := s.runner.recheckView(ctx, s.job, taskKey, s.resolved, s.program, changed.View, segment[0].StorageStart, s.end, true, s.abort)
			if err != nil {
				return nil, err
			}
			s.end = end
			s.switchView(taskKey, view)
			s.acc.setNote(noteIndexSwitch, fmt.Sprintf("回放过程中 View 重建了 %d 次索引，每次都按新索引复查了剩余区间后继续", s.switches))
			continue reload
		}
		// 参考市场只读年龄判定的历史（修订号不固定）。
		for _, ref := range s.refs {
			ages, err := agePresence(ctx, ref.history, s.resolved, segment, ref.passed)
			if err != nil {
				return nil, fmt.Errorf("参考市场 %s：%w", ref.market, err)
			}
			loaded[ref.market] = marketSegment{ages: ages}
		}
		return loaded, nil
	}
}

// attachSources 给每个市场的读取器加上各附加来源的行读取器：来源 View 是这个市场的来源任务，标的与主帧相同
// （不带 series_tag，读全部序列），只读策略引用的来源列。
func attachSources(ctx context.Context, client input.Client, spaceID string, resolved input.Resolved, program *dsl.Program, markets []*viewMarket, abort func(context.Context) error) error {
	if !resolved.HasSources() {
		return nil
	}
	views := map[string]input.ViewInfo{}
	for _, reader := range markets {
		reader.sources = map[string]*input.RangeLoader{}
		for _, name := range resolved.SourceNames() {
			columns := program.SourceColumns[name]
			if len(columns) == 0 {
				continue
			}
			task := resolved.Sources[name].Tasks[reader.market]
			view, ok := views[task.DatasetID]
			if !ok {
				var err error
				if view, err = client.GetView(ctx, spaceID, task.DatasetID); err != nil {
					return err
				}
				views[task.DatasetID] = view
			}
			subjects := make([]input.Subject, 0, len(reader.subjects))
			for _, subject := range reader.subjects {
				subject.SeriesTag = ""
				subjects = append(subjects, subject)
			}
			reader.sources[name] = &input.RangeLoader{Client: client, SpaceID: spaceID, View: view, Subjects: subjects, Columns: columns, Abort: abort}
		}
	}
	return nil
}

// loadSourceSegment 读一段 bar 要用到的来源行：从第一根主帧应读的来源 bar 的前一根（多取一根）到最后一根主帧应读的那一根。
// 来源 View 在回放过程中换了索引时直接失败（不跟随换代），请重新发起回放。
func loadSourceSegment(ctx context.Context, resolved input.Resolved, reader *viewMarket, segment []input.PeriodBoundaries) (map[string]input.RangeRows, error) {
	if len(reader.sources) == 0 {
		return nil, nil
	}
	out := make(map[string]input.RangeRows, len(reader.sources))
	for _, name := range sortedNames(reader.sources) {
		source := resolved.Sources[name]
		first, _, err := source.Period(segment[0].BarEnd)
		if err != nil {
			return nil, err
		}
		last, _, err := source.Period(segment[len(segment)-1].BarEnd)
		if err != nil {
			return nil, err
		}
		from := first.StorageStart
		if !first.PreviousStart.IsZero() {
			from = first.PreviousStart
		}
		rows, err := reader.sources[name].Load(ctx, from, last.StorageStart.Add(time.Nanosecond))
		var changed *input.IndexChangedError
		if errors.As(err, &changed) {
			return nil, fmt.Errorf("来源 %s 的 View %s 在回放过程中重建了索引，请重新发起回放", name, changed.View.DatasetID)
		}
		if err != nil {
			return nil, fmt.Errorf("读取来源 %s 的行：%w", name, err)
		}
		out[name] = rows
	}
	return out, nil
}

// referenceReaders 构造参考市场的读取器：只读年龄判定所需的 close 历史，列出的标的取参考数据集的全部标的绑定（回放中都视为活跃）。
func referenceReaders(ctx context.Context, client input.Client, job store.Replay, resolved input.Resolved, abort func(context.Context) error) ([]*viewMarket, error) {
	refs := make([]*viewMarket, 0, len(resolved.References))
	for _, market := range sortedNames(resolved.References) {
		task := resolved.References[market]
		view, err := client.GetView(ctx, job.SpaceID, task.DatasetID)
		if err != nil {
			return nil, err
		}
		bindings, err := client.ListDatasetSubjects(ctx, job.SpaceID, view.DatasetID)
		if err != nil {
			return nil, err
		}
		subjects := make([]input.Subject, 0, len(bindings))
		for _, subject := range bindings {
			subject.Active = true
			subjects = append(subjects, subject)
		}
		refs = append(refs, newViewMarket(newReplayMarket(market, subjects), job.Tasks[market], nil,
			&input.RangeLoader{Client: client, SpaceID: job.SpaceID, View: view, Subjects: subjects, Columns: []string{"close"}, Abort: abort}))
	}
	return refs, nil
}
