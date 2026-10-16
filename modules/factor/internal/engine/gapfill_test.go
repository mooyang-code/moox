package engine

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/gapledger"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/recalcexec"
	"github.com/stretchr/testify/require"
)

var gapT0 = time.Date(2026, 10, 14, 6, 0, 0, 0, time.UTC)

func gapFactor(id, factorType string, partial bool) domain.FactorDef {
	code := "def compute(df, params, context): return df"
	return domain.FactorDef{
		FactorID: id, Name: id, FactorType: factorType, SourceCode: code, SourceHash: domain.SourceHash(code),
		InputColumns: []string{"close"}, Outputs: []string{id + "_value"}, ParamsJSON: "{}", LookbackPeriods: 3, AllowPartialUniverse: partial,
	}
}

type fakeSets struct {
	sets    map[string]domain.EngineSet
	lookups map[string]SetLookup
}

func (f *fakeSets) SetByID(id string) (domain.EngineSet, SetLookup) {
	if lookup, ok := f.lookups[id]; ok {
		return f.sets[id], lookup
	}
	if set, ok := f.sets[id]; ok {
		return set, SetFound
	}
	return domain.EngineSet{}, SetGone
}

type fakeColumns struct{}

func (fakeColumns) DatasetColumns(context.Context, string, string) ([]string, error) {
	return []string{"close"}, nil
}
func (fakeColumns) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return nil, nil
}

type runCall struct {
	selection  recalcexec.Selection
	start, end time.Time
}

// fakeChunks 模拟补算执行器：RunChunkWith 取锁后调用 source（锁内重读），再按 items 给出逐项结果并调用 after。
type fakeChunks struct {
	calls  []runCall
	status func(recalcexec.Selection, time.Time) pipeline.ItemStatus
	err    error
}

func (f *fakeChunks) RunChunkWith(ctx context.Context, source recalcexec.SelectionSource, start, end time.Time, after func(recalcexec.Selection, pipeline.Outcome, error)) (pipeline.Outcome, error) {
	if _, err := source(ctx); err != nil { // 锁外探测
		return pipeline.Outcome{}, err
	}
	selection, err := source(ctx) // 锁内重读
	if err != nil || len(selection.Subjects) == 0 {
		return pipeline.Outcome{}, err
	}
	f.calls = append(f.calls, runCall{selection: selection, start: start, end: end})
	outcome := pipeline.Outcome{Items: map[pipeline.ItemKey]pipeline.ItemStatus{}}
	for _, factor := range selection.Factors {
		for _, subject := range selection.Subjects {
			for at := start; at.Before(end); at = at.Add(time.Minute) {
				status := pipeline.ItemWritten
				if f.status != nil {
					status = f.status(selection, at)
				}
				outcome.Items[pipeline.ItemKey{FactorID: factor.FactorID, Subject: subject, Period: at}] = status
			}
		}
	}
	if after != nil {
		after(selection, outcome, f.err)
	}
	return outcome, f.err
}

type fakeUniverse struct {
	subjects []string
	found    bool
	err      error
}

func (f fakeUniverse) ComputedUniverse(context.Context, string, string, string, int64) ([]string, bool, error) {
	return f.subjects, f.found, f.err
}

type gapFixture struct {
	ledger *gapledger.Ledger
	now    time.Time
	sets   *fakeSets
	chunks *fakeChunks
	filler *gapFiller
}

func newGapFixture(t *testing.T, universe fakeUniverse, factors ...domain.FactorDef) *gapFixture {
	t.Helper()
	f := &gapFixture{now: gapT0.Add(10 * time.Minute), chunks: &fakeChunks{}}
	ledger, _, err := gapledger.Open(t.TempDir(), func() time.Time { return f.now }, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ledger.Close() })
	f.ledger = ledger
	set := domain.EngineSet{Set: domain.FactorSet{SetID: "fset", SpaceID: "crypto", SourceDatasetID: "bars", ResultDatasetID: "result", Freq: "1m"}, Factors: factors, ResultReady: true}
	f.sets = &fakeSets{sets: map[string]domain.EngineSet{"fset": set}, lookups: map[string]SetLookup{}}
	f.filler = &gapFiller{
		ledger: ledger, sets: f.sets, source: fakeColumns{}, exec: f.chunks, universe: universe, clock: periodclock.Continuous{},
		cfg:    GapConfig{SweepInterval: time.Minute, Settle: 5 * time.Minute, GiveUpAfter: 6 * time.Hour, MaxGroupsPerSweep: 20, MaxSpan: 60},
		now:    func() time.Time { return f.now },
		active: func() bool { return true },
	}
	return f
}

func (f *gapFixture) record(t *testing.T, at time.Time, subject string, minute int, trigger string, factors ...domain.FactorDef) {
	t.Helper()
	saved := f.now
	f.now = at
	var entries []pipeline.GapEntry
	for _, factor := range factors {
		entries = append(entries, pipeline.GapEntry{SetID: "fset", Subject: subject, Period: gapT0.Add(time.Duration(minute) * time.Minute),
			FactorID: factor.FactorID, DefinitionHash: domain.DefinitionHash(factor), TriggerEventID: trigger})
	}
	require.NoError(t, f.ledger.RecordGaps(context.Background(), entries))
	f.now = saved
}

// 发现后过了 settle 才恢复；填充写入确认的项追加 repaired，仍有洞的保持待恢复。
func TestGapFillerRepairsWrittenItemsAndKeepsGaps(t *testing.T) {
	factor := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	f := newGapFixture(t, fakeUniverse{}, factor)
	f.record(t, gapT0, "BTC", 0, "", factor)
	f.record(t, gapT0, "BTC", 1, "", factor)
	f.chunks.status = func(_ recalcexec.Selection, at time.Time) pipeline.ItemStatus {
		if at.Equal(gapT0.Add(time.Minute)) {
			return pipeline.ItemGap // 01 仍有洞
		}
		return pipeline.ItemWritten
	}

	f.now = gapT0.Add(2 * time.Minute) // 还没过 settle
	f.filler.tick(context.Background())
	require.Empty(t, f.chunks.calls)

	f.now = gapT0.Add(6 * time.Minute)
	f.filler.tick(context.Background())
	require.Len(t, f.chunks.calls, 1)
	call := f.chunks.calls[0]
	require.True(t, call.selection.FillOnly, "恢复用只填充")
	require.Equal(t, []string{"BTC"}, call.selection.Subjects)
	require.Equal(t, gapT0, call.start)
	require.Equal(t, gapT0.Add(2*time.Minute), call.end, "终点是最后一个待补周期加一个周期")
	pending := f.ledger.Pending()
	require.Len(t, pending, 1)
	require.Equal(t, gapT0.Add(time.Minute), pending[0].Period, "00 已 repaired，01 仍有洞保持待恢复")
}

// 定义哈希变化：取消，不用新定义的结果去标旧定义的待办为已修复；因子已移除同样取消。
func TestGapFillerCancelsWhenDefinitionChangedOrFactorRemoved(t *testing.T) {
	old := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	removed := gapFactor("rsi", domain.FactorTypeTimeSeries, false)
	f := newGapFixture(t, fakeUniverse{}, old)
	changed := old
	changed.LookbackPeriods = 9 // 定义变化，哈希不同
	f.record(t, gapT0, "BTC", 0, "", changed, removed)
	f.now = gapT0.Add(6 * time.Minute)
	f.filler.tick(context.Background())
	require.Empty(t, f.chunks.calls, "没有可恢复的项就不运行")
	require.Zero(t, f.ledger.PendingCount())
}

// 集合已不存在：取消；目录未加载或结果表未就绪：等待，不能当作集合已经不存在。
func TestGapFillerSetGoneCancelsButNotReadyWaits(t *testing.T) {
	factor := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	f := newGapFixture(t, fakeUniverse{}, factor)
	f.record(t, gapT0, "BTC", 0, "", factor)
	f.now = gapT0.Add(6 * time.Minute)

	f.sets.lookups["fset"] = SetNotReady
	f.filler.tick(context.Background())
	require.Equal(t, 1, f.ledger.PendingCount(), "目录未就绪：等待")

	f.sets.lookups["fset"] = SetGone
	f.filler.tick(context.Background())
	require.Zero(t, f.ledger.PendingCount(), "集合已不再启用：取消")
}

// 超过 give_up_after 仍补不上：gave_up 并通知；执行出错保持待恢复。
func TestGapFillerGivesUpOverdueAndKeepsPendingOnError(t *testing.T) {
	factor := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	f := newGapFixture(t, fakeUniverse{}, factor)
	f.record(t, gapT0, "BTC", 0, "", factor)
	f.chunks.err = errors.New("storage unavailable")
	f.now = gapT0.Add(6 * time.Minute)
	f.filler.tick(context.Background())
	require.Equal(t, 1, f.ledger.PendingCount(), "执行出错保持待恢复")

	gaveUp := 0
	f.filler.onGaveUp = func(n int) { gaveUp += n }
	f.now = gapT0.Add(7 * time.Hour)
	f.filler.tick(context.Background())
	require.Zero(t, f.ledger.PendingCount())
	require.Equal(t, 1, gaveUp)
}

// 截面因子：用 marker 里冻结的全集重算，不用单个标的当全集；全集读不到记 gave_up。
func TestGapFillerCrossSectionUsesFrozenUniverse(t *testing.T) {
	factor := gapFactor("rank", domain.FactorTypeCrossSection, true)
	f := newGapFixture(t, fakeUniverse{subjects: []string{"BTC", "ETH", "SOL"}, found: true}, factor)
	f.record(t, gapT0, "ETH", 0, "evt-1", factor)
	f.now = gapT0.Add(6 * time.Minute)
	f.filler.tick(context.Background())
	require.Len(t, f.chunks.calls, 1)
	require.Equal(t, []string{"BTC", "ETH", "SOL"}, f.chunks.calls[0].selection.Subjects, "用冻结全集，不是只有 ETH")
	require.Zero(t, f.ledger.PendingCount())

	missing := newGapFixture(t, fakeUniverse{found: false}, factor)
	missing.record(t, gapT0, "ETH", 0, "evt-1", factor)
	missing.now = gapT0.Add(6 * time.Minute)
	missing.filler.tick(context.Background())
	require.Empty(t, missing.chunks.calls)
	require.Zero(t, missing.ledger.PendingCount(), "marker 读不到：gave_up 交给人工")

	failing := newGapFixture(t, fakeUniverse{err: errors.New("storage unavailable")}, factor)
	failing.record(t, gapT0, "ETH", 0, "evt-1", factor)
	failing.now = gapT0.Add(6 * time.Minute)
	failing.filler.tick(context.Background())
	require.Equal(t, 1, failing.ledger.PendingCount(), "读取失败只是暂时的：保持待恢复")
}

// 不是活动引擎（租约冲突）时不恢复。
func TestGapFillerSkipsWhenNotActive(t *testing.T) {
	factor := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	f := newGapFixture(t, fakeUniverse{}, factor)
	f.record(t, gapT0, "BTC", 0, "", factor)
	f.filler.active = func() bool { return false }
	f.now = gapT0.Add(6 * time.Minute)
	f.filler.tick(context.Background())
	require.Empty(t, f.chunks.calls)
	require.Equal(t, 1, f.ledger.PendingCount())
}

func (f *gapFixture) recordWarming(t *testing.T, subject string, minute int, trigger string, factors ...domain.FactorDef) {
	t.Helper()
	var entries []pipeline.GapEntry
	for _, factor := range factors {
		entries = append(entries, pipeline.GapEntry{SetID: "fset", Subject: subject, Period: gapT0.Add(time.Duration(minute) * time.Minute),
			FactorID: factor.FactorID, DefinitionHash: domain.DefinitionHash(factor), TriggerEventID: trigger, Reason: pipeline.SkipReasonWarming})
	}
	saved := f.now
	f.now = gapT0
	require.NoError(t, f.ledger.RecordGaps(context.Background(), entries))
	f.now = saved
}

// 重启后一期整体预热跳过：恢复区间相同的标的合并成一次只填充重算，不逐个标的各跑一遍；区间不同的标的单独一批。
func TestGapFillerBatchesSubjectsWithSameSpan(t *testing.T) {
	factor := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	f := newGapFixture(t, fakeUniverse{}, factor)
	for _, subject := range []string{"SOL", "BTC", "ETH"} {
		f.recordWarming(t, subject, 0, "evt-0", factor)
	}
	f.record(t, gapT0, "DOGE", 0, "", factor)
	f.record(t, gapT0, "DOGE", 1, "", factor)

	f.now = gapT0.Add(6 * time.Minute)
	f.filler.tick(context.Background())
	require.Len(t, f.chunks.calls, 2)
	byFirst := map[string]runCall{}
	for _, call := range f.chunks.calls {
		require.True(t, call.selection.FillOnly)
		byFirst[call.selection.Subjects[0]] = call
	}
	warming := byFirst["BTC"]
	require.Equal(t, []string{"BTC", "ETH", "SOL"}, warming.selection.Subjects, "同一期预热跳过的标的一次恢复")
	require.True(t, warming.selection.FillCarry, "预热跳过的那一期没有写任何行，carry 列一并补上")
	require.Equal(t, gapT0, warming.start)
	require.Equal(t, gapT0.Add(time.Minute), warming.end)
	doge := byFirst["DOGE"]
	require.Equal(t, []string{"DOGE"}, doge.selection.Subjects)
	require.False(t, doge.selection.FillCarry, "空洞那一期已写过 carry 列，只补因子列")
	require.Equal(t, gapT0.Add(2*time.Minute), doge.end)
	require.Zero(t, f.ledger.PendingCount(), "写入确认后全部 repaired")
}

// 一批最多 maxFillSubjects 个标的；每轮的上限按批计。
func TestGapFillerCapsBatchSizeAndCountsBatchesPerSweep(t *testing.T) {
	factor := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	f := newGapFixture(t, fakeUniverse{}, factor)
	for i := 0; i < maxFillSubjects+20; i++ {
		f.recordWarming(t, fmt.Sprintf("S%03d", i), 0, "evt-0", factor)
	}
	f.filler.cfg.MaxGroupsPerSweep = 1
	f.now = gapT0.Add(6 * time.Minute)
	f.filler.tick(context.Background())
	require.Len(t, f.chunks.calls, 1)
	require.Len(t, f.chunks.calls[0].selection.Subjects, maxFillSubjects)
	require.Equal(t, 20, f.ledger.PendingCount())

	f.filler.tick(context.Background())
	require.Len(t, f.chunks.calls, 2)
	require.Len(t, f.chunks.calls[1].selection.Subjects, 20, "剩下的标的下一轮恢复")
	require.Zero(t, f.ledger.PendingCount())
}

// 截面因子的预热跳过同样按周期用冻结全集恢复。
func TestGapFillerRepairsWarmingCrossSectionWithFrozenUniverse(t *testing.T) {
	factor := gapFactor("rank", domain.FactorTypeCrossSection, false)
	f := newGapFixture(t, fakeUniverse{subjects: []string{"BTC", "ETH"}, found: true}, factor)
	f.recordWarming(t, "BTC", 0, "evt-0", factor)
	f.recordWarming(t, "ETH", 0, "evt-0", factor)
	f.now = gapT0.Add(6 * time.Minute)
	f.filler.tick(context.Background())
	require.Len(t, f.chunks.calls, 1)
	require.Equal(t, []string{"BTC", "ETH"}, f.chunks.calls[0].selection.Subjects)
	require.True(t, f.chunks.calls[0].selection.FillCarry)
	require.Zero(t, f.ledger.PendingCount())
}

type repairedCall struct {
	period time.Time
	items  int
	done   bool
}

// 预热完成后立刻补算预热期间跳过的那一期：不等 Settle，也不等下一轮轮询；空洞跳过的项和预热完成之后才跳过的项仍等 Settle。
// 每批补齐后按周期回调，账本里这一期没有待办了才算补齐。
func TestGapFillerRepairsWarmingSkipsRightAfterWarmup(t *testing.T) {
	factor := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	f := newGapFixture(t, fakeUniverse{}, factor)
	f.filler.kick = make(chan struct{}, 1)
	var calls []repairedCall
	f.filler.onRepaired = func(setID string, period time.Time, items []pipeline.ItemKey, done bool) {
		require.Equal(t, "fset", setID)
		calls = append(calls, repairedCall{period: period, items: len(items), done: done})
	}
	for i := 0; i < maxFillSubjects+1; i++ {
		f.recordWarming(t, fmt.Sprintf("S%03d", i), 0, "evt-0", factor) // 发现于 gapT0
	}
	f.record(t, gapT0, "DOGE", 1, "", factor) // 空洞跳过

	f.now = gapT0.Add(2 * time.Minute)
	f.filler.tick(context.Background())
	require.Empty(t, f.chunks.calls, "还在 Settle 内，集合也没有预热完成")

	f.filler.Warmed("fset", gapT0.Add(90*time.Second))
	require.Len(t, f.filler.kick, 1, "预热完成唤醒恢复循环")
	<-f.filler.kick
	f.filler.tick(context.Background())
	require.Len(t, f.chunks.calls, 2, "预热跳过的 101 个标的分两批，空洞跳过的 DOGE 仍等 Settle")
	for _, call := range f.chunks.calls {
		require.NotContains(t, call.selection.Subjects, "DOGE")
	}
	require.Equal(t, 1, f.ledger.PendingCount())
	require.Len(t, calls, 2)
	require.False(t, calls[0].done, "第一批补完时这一期还有待办")
	require.Equal(t, repairedCall{period: gapT0, items: 1, done: true}, calls[1])
	require.Equal(t, maxFillSubjects, calls[0].items)

	// 预热完成之后才因预热中跳过的项（标的又落后了，要重新预热）不算已预热，仍等 Settle。
	f.now = gapT0.Add(3 * time.Minute)
	f.recordWarmingAt(t, gapT0.Add(2*time.Minute), "LATE", 2, factor)
	f.filler.tick(context.Background())
	require.Len(t, f.chunks.calls, 2)
	require.Equal(t, 2, f.ledger.PendingCount())
}

func (f *gapFixture) recordWarmingAt(t *testing.T, at time.Time, subject string, minute int, factor domain.FactorDef) {
	t.Helper()
	saved := f.now
	f.now = at
	require.NoError(t, f.ledger.RecordGaps(context.Background(), []pipeline.GapEntry{{
		SetID: "fset", Subject: subject, Period: gapT0.Add(time.Duration(minute) * time.Minute), FactorID: factor.FactorID,
		DefinitionHash: domain.DefinitionHash(factor), TriggerEventID: "evt", Reason: pipeline.SkipReasonWarming,
	}}))
	f.now = saved
}
