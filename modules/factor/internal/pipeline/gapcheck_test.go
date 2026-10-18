package pipeline

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

var gapBase = time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)

func minute(n int) time.Time { return gapBase.Add(time.Duration(n) * time.Minute) }

// gapFrame 构造一个标的的源帧：minutes 是有数据的分钟序号，tag 是序列标签。
func gapFrame(subject, tag string, minutes ...int) *storageio.Frame {
	frame := &storageio.Frame{SubjectID: subject, Columns: []string{"data_time", "series_tag", "close"}}
	for _, m := range minutes {
		frame.Rows = append(frame.Rows, []any{minute(m), tag, 10.0 + float64(m)})
	}
	return frame
}

func rangeMinutes(from, to int, skip ...int) []int {
	skipped := map[int]bool{}
	for _, m := range skip {
		skipped[m] = true
	}
	var out []int
	for m := from; m <= to; m++ {
		if !skipped[m] {
			out = append(out, m)
		}
	}
	return out
}

// earliestStore 在 outputStore 之外提供 EarliestPeriod，模拟标的的首根 K 线记录。
type earliestStore struct {
	outputStore
	at     time.Time
	found  bool
	failed bool
	calls  int
}

func (s *earliestStore) EarliestPeriod(context.Context, string, string, string, []string, []string) (time.Time, bool, error) {
	s.calls++
	if s.failed {
		return time.Time{}, false, context.DeadlineExceeded
	}
	return s.at, s.found, nil
}

func gapRunner(store storageio.Store) *Runner {
	return NewRunner(store, periodclock.Continuous{}, Config{GapCheck: true})
}

func livePlanAt(target time.Time, factors []domain.FactorDef, subjects ...string) Plan {
	plan := computePlan(target, factors, subjects)
	maxL := maxLookback(factors)
	plan.TargetStart = target.Add(-time.Duration(maxL-1) * time.Minute)
	plan.TargetEnd = target.Add(time.Minute)
	plan.PeriodTime = target
	return plan
}

func loadedOf(frames map[string]*storageio.Frame) LoadResult {
	loaded := LoadResult{Frames: frames}
	for subject := range frames {
		loaded.Available = append(loaded.Available, subject)
	}
	return loaded
}

// 窗口中间缺一根：回看覆盖它的因子对该周期失败，回看更短的因子不受影响。
func TestCheckGapsFlagsOnlyFactorsWhoseWindowCoversTheHole(t *testing.T) {
	long := testPipelineFactor("long", domain.FactorTypeTimeSeries, 5, false)
	short := testPipelineFactor("short", domain.FactorTypeTimeSeries, 2, false)
	plan := livePlanAt(minute(10), []domain.FactorDef{long, short}, "BTC")
	frames := map[string]*storageio.Frame{"BTC": gapFrame("BTC", "", rangeMinutes(6, 10, 8)...)}

	gaps := gapRunner(&outputStore{}).checkGaps(context.Background(), plan, loadedOf(frames))

	require.True(t, gaps.Invalid("long", "BTC", minute(10)))
	require.False(t, gaps.Invalid("short", "BTC", minute(10)), "短回看因子的窗口 09、10 完整")
	require.Equal(t, []string{"BTC"}, gaps.Subjects("long", minute(10)))
	require.Empty(t, gaps.Subjects("short", minute(10)))
	entries := gaps.entries4Ledger(plan)
	require.Len(t, entries, 1)
	require.Equal(t, []time.Time{minute(8)}, entries[0].Missing)
}

// 目标周期本身缺失（上游没有报失败）同样是空洞。
func TestCheckGapsTreatsMissingTargetBarAsGap(t *testing.T) {
	factor := testPipelineFactor("f", domain.FactorTypeTimeSeries, 3, false)
	plan := livePlanAt(minute(10), []domain.FactorDef{factor}, "BTC")
	frames := map[string]*storageio.Frame{"BTC": gapFrame("BTC", "", 8, 9)}
	gaps := gapRunner(&outputStore{}).checkGaps(context.Background(), plan, loadedOf(frames))
	require.True(t, gaps.Invalid("f", "BTC", minute(10)))
}

// 头部缺失：有上市依据才放行；首根 K 线早于缺失点（真的断了）、读取失败（保守）都算空洞。
func TestCheckGapsHeadMissingNeedsListingEvidence(t *testing.T) {
	factor := testPipelineFactor("f", domain.FactorTypeTimeSeries, 5, false)
	plan := livePlanAt(minute(10), []domain.FactorDef{factor}, "NEW")
	frames := map[string]*storageio.Frame{"NEW": gapFrame("NEW", "", 8, 9, 10)} // 06、07 缺失

	listedAt08 := &earliestStore{at: minute(8), found: true}
	require.Zero(t, gapRunner(listedAt08).checkGaps(context.Background(), plan, loadedOf(frames)).Len(), "08 才上市：06、07 不是空洞")

	existedBefore := &earliestStore{at: minute(0), found: true}
	require.True(t, gapRunner(existedBefore).checkGaps(context.Background(), plan, loadedOf(frames)).Invalid("f", "NEW", minute(10)), "早就有数据，06、07 是真的缺")

	failing := &earliestStore{failed: true}
	require.True(t, gapRunner(failing).checkGaps(context.Background(), plan, loadedOf(frames)).Invalid("f", "NEW", minute(10)), "读取失败时保守当作空洞")

	require.True(t, gapRunner(&outputStore{}).checkGaps(context.Background(), plan, loadedOf(frames)).Invalid("f", "NEW", minute(10)), "存储不支持首根 K 线查询时保守当作空洞")
}

// 首根 K 线只在需要时才查，并且按标的缓存。
func TestFirstBarLookupIsLazyAndCached(t *testing.T) {
	factor := testPipelineFactor("f", domain.FactorTypeTimeSeries, 3, false)
	store := &earliestStore{at: minute(0), found: true}
	runner := gapRunner(store)
	plan := livePlanAt(minute(10), []domain.FactorDef{factor}, "A")
	complete := map[string]*storageio.Frame{"A": gapFrame("A", "", 8, 9, 10)}
	runner.checkGaps(context.Background(), plan, loadedOf(complete))
	require.Zero(t, store.calls, "窗口完整、没有头部缺失时不查首根 K 线")

	headMissing := map[string]*storageio.Frame{"A": gapFrame("A", "", 10)}
	runner.checkGaps(context.Background(), plan, loadedOf(headMissing))
	runner.checkGaps(context.Background(), plan, loadedOf(headMissing))
	require.Equal(t, 1, store.calls, "已找到的首根 K 线缓存，不重复查询")
}

// 多个序列分别比较：一个序列有洞就算有洞，交错存在不能互相掩盖。
func TestCheckGapsComparesEachSeriesSeparately(t *testing.T) {
	factor := testPipelineFactor("f", domain.FactorTypeTimeSeries, 3, false)
	plan := livePlanAt(minute(10), []domain.FactorDef{factor}, "BTC")
	frame := gapFrame("BTC", "binance", 8, 9, 10)
	other := gapFrame("BTC", "okx", 8, 10)
	frame.Rows = append(frame.Rows, other.Rows...)
	gaps := gapRunner(&outputStore{}).checkGaps(context.Background(), plan, loadedOf(map[string]*storageio.Frame{"BTC": frame}))
	require.True(t, gaps.Invalid("f", "BTC", minute(10)))
	entries := gaps.entries4Ledger(plan)
	require.Equal(t, []string{"okx"}, entries[0].Tags)
}

// 空帧：存在过的标的窗口整段缺失算空洞；从未有过数据沿用现有路径。
func TestCheckGapsEmptyFrameRequiresHistory(t *testing.T) {
	factor := testPipelineFactor("f", domain.FactorTypeTimeSeries, 3, false)
	plan := livePlanAt(minute(10), []domain.FactorDef{factor}, "BTC")
	frames := map[string]*storageio.Frame{"BTC": {SubjectID: "BTC", Columns: []string{"data_time", "series_tag", "close"}}}
	had := &earliestStore{at: minute(0), found: true}
	require.True(t, gapRunner(had).checkGaps(context.Background(), plan, loadedOf(frames)).Invalid("f", "BTC", minute(10)))
	never := &earliestStore{found: false}
	require.Zero(t, gapRunner(never).checkGaps(context.Background(), plan, loadedOf(frames)).Len())
}

// 补算：一个空洞只影响它之后回看长度那么多个周期，其余周期不受影响。
func TestCheckGapsRecalcMarksOnlyPeriodsWhoseWindowCoversTheHole(t *testing.T) {
	factor := testPipelineFactor("f", domain.FactorTypeTimeSeries, 3, false)
	plan := computePlan(minute(11), []domain.FactorDef{factor}, []string{"BTC"})
	plan.Mode = ModeRecalc
	plan.TargetStart, plan.TargetEnd = minute(8), minute(12)
	frames := map[string]*storageio.Frame{"BTC": gapFrame("BTC", "", rangeMinutes(6, 11, 9)...)}
	gaps := gapRunner(&outputStore{}).checkGaps(context.Background(), plan, loadedOf(frames))
	require.False(t, gaps.Invalid("f", "BTC", minute(8)), "窗口 06-08 完整")
	for _, m := range []int{9, 10, 11} {
		require.True(t, gaps.Invalid("f", "BTC", minute(m)), "窗口覆盖 09：%d", m)
	}
}

// 组装：有洞的周期因子列为 NULL，其余周期照常写值；逐项结果区分 written 与 gap。
func TestAssembleNullsGapPeriodsOnlyAndReportsItems(t *testing.T) {
	factor := testPipelineFactor("f", domain.FactorTypeTimeSeries, 3, false)
	plan := computePlan(minute(11), []domain.FactorDef{factor}, []string{"BTC"})
	plan.Mode = ModeRecalc
	plan.TargetStart, plan.TargetEnd = minute(8), minute(12)
	frames := map[string]*storageio.Frame{"BTC": gapFrame("BTC", "", rangeMinutes(6, 11, 9)...)}
	runner := gapRunner(&outputStore{})
	loaded := loadedOf(frames)
	loaded.Gaps = runner.checkGaps(context.Background(), plan, loaded)
	computation := successfulComputation(factor, []string{"data_time", "series_tag", factor.Outputs[0]},
		[][]any{{minute(8), "", 1.0}, {minute(10), "", 3.0}, {minute(11), "", 4.0}})

	rows, report, err := runner.AssembleWithReport(plan, loaded, computation)
	require.NoError(t, err)

	byTime := map[time.Time]storageio.ResultRow{}
	for _, row := range rows {
		byTime[row.DataTime] = row
	}
	require.Equal(t, 1.0, byTime[minute(8)].Fields[factor.Outputs[0]])
	require.Nil(t, byTime[minute(10)].Fields[factor.Outputs[0]], "10 的窗口覆盖空洞：即使计算给了值也写 NULL")
	require.Nil(t, byTime[minute(11)].Fields[factor.Outputs[0]])
	require.Equal(t, ItemWritten, report.Items[ItemKey{FactorID: "f", Subject: "BTC", Period: minute(8)}])
	require.Equal(t, ItemGap, report.Items[ItemKey{FactorID: "f", Subject: "BTC", Period: minute(10)}])
}

// 目标源行不存在时没有行可以承载失效：补一行只含该因子列的 NULL 行，清掉旧值。
func TestAssembleAddsNullRowWhenTargetSourceRowIsMissing(t *testing.T) {
	factor := testPipelineFactor("f", domain.FactorTypeTimeSeries, 3, false)
	plan := livePlanAt(minute(10), []domain.FactorDef{factor}, "BTC")
	frames := map[string]*storageio.Frame{"BTC": gapFrame("BTC", "binance", 8, 9)} // 目标 10 缺失
	runner := gapRunner(&outputStore{})
	loaded := loadedOf(frames)
	loaded.Gaps = runner.checkGaps(context.Background(), plan, loaded)
	computation := successfulComputation(factor, []string{"data_time", "series_tag", factor.Outputs[0]}, nil)

	rows, _, err := runner.AssembleWithReport(plan, loaded, computation)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, minute(10), rows[0].DataTime)
	require.Equal(t, "binance", rows[0].SeriesTag)
	require.Equal(t, map[string]any{factor.Outputs[0]: nil}, rows[0].Fields)
}

// 只填充：有洞、无效值、失败都不写任何字段（尤其不写 NULL），不写 carry 列；有效值照常写入。合法 NULL 写入并确认。
func TestAssembleFillOnlyNeverWritesInvalidationNull(t *testing.T) {
	factor := testPipelineFactor("f", domain.FactorTypeTimeSeries, 3, false)
	plan := computePlan(minute(11), []domain.FactorDef{factor}, []string{"BTC"})
	plan.Mode = ModeRecalc
	plan.FillOnly, plan.WriteCarry = true, false
	plan.TargetStart, plan.TargetEnd = minute(8), minute(12)
	frames := map[string]*storageio.Frame{"BTC": gapFrame("BTC", "", rangeMinutes(6, 11, 9)...)}
	runner := gapRunner(&outputStore{})
	loaded := loadedOf(frames)
	loaded.Gaps = runner.checkGaps(context.Background(), plan, loaded)
	// 08 有效值、10 有洞（给了值也不写）、11 有洞；Python 对 08 之外没有行：另造一个合法 NULL。
	computation := successfulComputation(factor, []string{"data_time", "series_tag", factor.Outputs[0]},
		[][]any{{minute(8), "", 1.0}, {minute(10), "", 3.0}, {minute(11), "", nil}})

	rows, report, err := runner.AssembleWithReport(plan, loaded, computation)
	require.NoError(t, err)

	require.Len(t, rows, 1, "只有 08 有可写的内容")
	require.Equal(t, minute(8), rows[0].DataTime)
	require.Equal(t, map[string]any{factor.Outputs[0]: 1.0}, rows[0].Fields, "不写 carry 列，只写因子列")
	require.Equal(t, ItemGap, report.Items[ItemKey{FactorID: "f", Subject: "BTC", Period: minute(10)}])
	require.Equal(t, ItemGap, report.Items[ItemKey{FactorID: "f", Subject: "BTC", Period: minute(11)}])
}

// 允许部分全集的截面因子：标的的输入在某个截面时刻不完整时（窗口跨过空洞，严格窗口给 NaN），只在那个时刻不参与截面
// （设计 7.3）；严格截面因子实时有洞时整因子 skipped。
func TestComputeCrossSectionExcludesGapSubjects(t *testing.T) {
	target := minute(10)
	partial := testPipelineFactor("partial", domain.FactorTypeCrossSection, 3, true)
	strict := testPipelineFactor("strict", domain.FactorTypeCrossSection, 3, false)
	plan := livePlanAt(target, []domain.FactorDef{partial, strict}, "BTC", "ETH")
	runner := NewRunner(&outputStore{}, periodclock.Continuous{}, Config{GapCheck: true})
	frames := map[string]*storageio.Frame{
		"BTC": gapFrame("BTC", "", 8, 9, 10),
		"ETH": gapFrame("ETH", "", 8, 10), // 09 缺失
	}
	loaded := loadedOf(frames)
	loaded.Gaps = runner.checkGaps(context.Background(), plan, loaded)

	computation, err := runner.Compute(context.Background(), plan, loaded)
	require.NoError(t, err)

	require.Equal(t, "skipped", computation.FactorStates["strict"].Status, "严格截面因子实时有洞整因子 skipped")
	require.Equal(t, "degraded", computation.FactorStates["partial"].Status)
	require.Equal(t, []string{"ETH"}, computation.FactorStates["partial"].GapSubjects)
	require.Contains(t, computation.FactorStates["partial"].FailedSubjects, "ETH")
	require.Empty(t, computation.Results["strict"], "严格截面因子不计算")
	ranks := map[string]any{}
	for _, row := range computation.Results["partial"][""].Rows {
		ranks[row[2].(string)] = row[3]
	}
	require.Equal(t, map[string]any{"BTC": 1.0, "ETH": nil}, ranks, "ETH 的窗口跨过空洞：不参与排名，也没有值")
}

type recordingSink struct {
	mu      sync.Mutex
	entries []GapEntry
}

func (s *recordingSink) RecordGaps(_ context.Context, entries []GapEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entries...)
	return nil
}

// Run：有洞时先记账本再写；有洞的批次与补算的写入使用每次执行唯一的提交 ID，没有空洞的实时写入保持内容寻址。
func TestRunRecordsGapsAndSaltsCommitIDs(t *testing.T) {
	factor := testPipelineFactor("f", domain.FactorTypeTimeSeries, 3, false)
	store := &gapRunStore{frames: map[string]*storageio.Frame{"BTC": gapFrame("BTC", "", 8, 10)}}
	sink := &recordingSink{}
	runner := NewRunner(store, periodclock.Continuous{}, Config{GapCheck: true, GapSink: sink})
	set := domain.FactorSet{SetID: "fset", SpaceID: "crypto", SourceDatasetID: "dataset_bars", ResultDatasetID: "result", Freq: "1m", Status: domain.SetStatusEnabled, SubjectMode: domain.SubjectModeAll}
	plan, err := BuildLivePlan(periodclock.Continuous{}, LiveInput{Set: set, Factors: []domain.FactorDef{factor}, PeriodTime: minute(10), Universe: []string{"BTC"}, CarryColumns: []string{"close"}, TriggerEventID: "evt-1"})
	require.NoError(t, err)

	outcome, err := runner.Run(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, "degraded", outcome.Status)
	require.Equal(t, []string{"BTC"}, outcome.Factors[0].GapSubjects)
	require.Len(t, sink.entries, 1)
	require.Equal(t, "evt-1", sink.entries[0].TriggerEventID)
	require.Equal(t, minute(10), sink.entries[0].Period)
	require.Len(t, store.markers, 1)
	require.Equal(t, []string{"BTC"}, store.markers[0].Factors[0].GapSubjects)

	// 同一个有洞的周期重跑：提交 ID 不同（不会被旧回执吞掉）。
	_, err = runner.Run(context.Background(), plan)
	require.NoError(t, err)
	require.Len(t, store.writeCalls, 2)
	require.NotEqual(t, store.writeCalls[0].commitID, store.writeCalls[1].commitID)

	// 没有空洞的实时周期：提交 ID 是内容寻址（重跑相同）。
	store.frames = map[string]*storageio.Frame{"BTC": gapFrame("BTC", "", 8, 9, 10)}
	store.writeCalls = nil
	fresh := NewRunner(store, periodclock.Continuous{}, Config{GapCheck: true, GapSink: sink})
	_, err = fresh.Run(context.Background(), plan)
	require.NoError(t, err)
	_, err = fresh.Run(context.Background(), plan)
	require.NoError(t, err)
	require.Len(t, store.writeCalls, 2)
	require.Equal(t, store.writeCalls[0].commitID, store.writeCalls[1].commitID)
}

type gapRunStore struct {
	outputStore
	frames map[string]*storageio.Frame
}

func (s *gapRunStore) ReadWindow(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
	out := make(map[string]*storageio.Frame, len(req.Subjects))
	for _, subject := range req.Subjects {
		frame := &storageio.Frame{SubjectID: subject, Columns: []string{"data_time", "series_tag", "close"}}
		if source := s.frames[subject]; source != nil {
			for _, row := range source.Rows {
				if at := row[0].(time.Time); !at.Before(req.Start) && at.Before(req.End) {
					frame.Rows = append(frame.Rows, row)
				}
			}
		}
		out[subject] = frame
	}
	return out, nil
}

// 缓存里的窗口一定无洞：有洞的标的不写缓存并标记 dirty，下期整窗口读取（不用缓存前缀），读到完整窗口后恢复正常增量读取。
func TestLiveWindowCacheNeverKeepsHolesAndRereadsDirtySubjects(t *testing.T) {
	set := domain.FactorSet{SetID: "fset", SpaceID: "crypto", SourceDatasetID: "dataset_bars", Freq: "1m", Status: domain.SetStatusEnabled, SubjectMode: domain.SubjectModeAll}
	livePlan := func(target time.Time) Plan {
		plan, err := BuildLivePlan(periodclock.Continuous{}, LiveInput{
			Set: set, Factors: []domain.FactorDef{{FactorID: "f", LookbackPeriods: 3}},
			PeriodTime: target, Universe: []string{"BTC"}, CarryColumns: []string{"close"},
		})
		require.NoError(t, err)
		return plan
	}
	missing := map[int]bool{9: true}
	var mu sync.Mutex
	var starts []time.Time
	store := &fakeStore{read: func(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
		mu.Lock()
		defer mu.Unlock()
		starts = append(starts, req.Start)
		frames := emptyFrames(req)
		for at := req.Start; at.Before(req.End); at = at.Add(time.Minute) {
			if missing[int(at.Sub(gapBase)/time.Minute)] {
				continue
			}
			frames["BTC"].Rows = append(frames["BTC"].Rows, []any{at, "", 1.0})
		}
		return frames, nil
	}}
	runner := NewRunner(store, periodclock.Continuous{}, Config{GapCheck: true})

	// 10：窗口 08-10 缺 09，不进缓存，标记 dirty。
	first := livePlan(minute(10))
	loaded, err := runner.Load(context.Background(), first)
	require.NoError(t, err)
	require.Len(t, loaded.Frames["BTC"].Rows, 2)
	require.Equal(t, 1, runner.windows.dirtyCount(first))

	// 11：dirty 的标的整窗口读取（从 09 起），不是只读目标那一根；窗口仍有洞（09 缺），保持 dirty。
	second := livePlan(minute(11))
	_, err = runner.Load(context.Background(), second)
	require.NoError(t, err)
	require.Equal(t, minute(9), starts[len(starts)-1], "dirty 的标的整窗口读取")
	require.Equal(t, 1, runner.windows.dirtyCount(second))

	// 空洞补上后读到完整窗口：解除 dirty，窗口进缓存。
	delete(missing, 9)
	third := livePlan(minute(12))
	_, err = runner.Load(context.Background(), third)
	require.NoError(t, err)
	require.Equal(t, minute(10), starts[len(starts)-1])
	require.Zero(t, runner.windows.dirtyCount(third))

	// 之后恢复增量读取：只读目标那一根。
	fourth := livePlan(minute(13))
	_, err = runner.Load(context.Background(), fourth)
	require.NoError(t, err)
	require.Equal(t, minute(13), starts[len(starts)-1], "缓存里的窗口无洞，只读目标周期")
}

// 迟到的较旧窗口不能解除较新的 dirty 标记。
func TestOlderCleanWindowDoesNotClearNewerDirty(t *testing.T) {
	cache := newLiveWindowCache(false)
	plan := Plan{Set: domain.FactorSet{SetID: "fset", SpaceID: "crypto", SourceDatasetID: "d", Freq: "1m"}, TargetEnd: minute(11)}
	cache.markDirty(plan, map[string]struct{}{"BTC": {}}, nil)
	older := plan
	older.TargetEnd = minute(9)
	cache.markDirty(older, nil, []string{"BTC"})
	require.Equal(t, 1, cache.dirtyCount(plan), "较旧的完整窗口不解除较新的 dirty")
	cache.markDirty(plan, nil, []string{"BTC"})
	require.Zero(t, cache.dirtyCount(plan))
}

// 合法的 NULL（因子对某点输出空值）是有效结果：填充时写入并确认，不会永远重试。
func TestAssembleFillOnlyWritesLegitimateNull(t *testing.T) {
	factor := testPipelineFactor("f", domain.FactorTypeTimeSeries, 1, false)
	plan := computePlan(minute(10), []domain.FactorDef{factor}, []string{"BTC"})
	plan.Mode = ModeRecalc
	plan.FillOnly, plan.WriteCarry = true, false
	plan.TargetStart, plan.TargetEnd = minute(10), minute(11)
	loaded := loadedOf(map[string]*storageio.Frame{"BTC": gapFrame("BTC", "", 10)})
	computation := successfulComputation(factor, []string{"data_time", "series_tag", factor.Outputs[0]}, [][]any{{minute(10), "", nil}})
	rows, report, err := gapRunner(&outputStore{}).AssembleWithReport(plan, loaded, computation)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, map[string]any{factor.Outputs[0]: nil}, rows[0].Fields)
	require.Equal(t, ItemWritten, report.Items[ItemKey{FactorID: "f", Subject: "BTC", Period: minute(10)}])
}

// 存储不支持首根 K 线查询（或读取失败）时，空帧保守当作窗口整段缺失，不能把「查不到」当成「没有历史」。
func TestCheckGapsEmptyFrameWithUnknownHistoryIsConservative(t *testing.T) {
	factor := testPipelineFactor("f", domain.FactorTypeTimeSeries, 3, false)
	plan := livePlanAt(minute(10), []domain.FactorDef{factor}, "BTC")
	frames := map[string]*storageio.Frame{"BTC": {SubjectID: "BTC", Columns: []string{"data_time", "series_tag", "close"}}}
	require.True(t, gapRunner(&outputStore{}).checkGaps(context.Background(), plan, loadedOf(frames)).Invalid("f", "BTC", minute(10)))
	require.True(t, gapRunner(&earliestStore{failed: true}).checkGaps(context.Background(), plan, loadedOf(frames)).Invalid("f", "BTC", minute(10)))
}

// 同一因子、标的、周期有两个序列：一个成功一个失败时不能确认该项（否则恢复会把待办移除，失败的序列再也不重试）。
func TestAssembleReportDoesNotConfirmWhenOneSeriesFails(t *testing.T) {
	factor := testPipelineFactor("f", domain.FactorTypeTimeSeries, 1, false)
	plan := computePlan(minute(10), []domain.FactorDef{factor}, []string{"BTC"})
	plan.Mode = ModeRecalc
	plan.FillOnly, plan.WriteCarry = true, false
	plan.TargetStart, plan.TargetEnd = minute(10), minute(11)
	frame := gapFrame("BTC", "a", 10)
	frame.Rows = append(frame.Rows, gapFrame("BTC", "b", 10).Rows...)
	loaded := loadedOf(map[string]*storageio.Frame{"BTC": frame})
	computation := successfulComputation(factor, []string{"data_time", "series_tag", factor.Outputs[0]},
		[][]any{{minute(10), "a", 1.0}, {minute(10), "b", "不是数字"}})
	rows, report, err := gapRunner(&outputStore{}).AssembleWithReport(plan, loaded, computation)
	require.NoError(t, err)
	require.Len(t, rows, 1, "只写成功的序列")
	require.Equal(t, ItemFailed, report.Items[ItemKey{FactorID: "f", Subject: "BTC", Period: minute(10)}])
}
