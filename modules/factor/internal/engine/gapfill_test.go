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
	expression := "ts_mean(close, 3)"
	if factorType == domain.FactorTypeCrossSection {
		expression = "cs_rank(ts_mean(close, 3))"
	}
	def := domain.MustDefinition(id, id, "output: "+expression+"\n")
	def.AllowPartialUniverse = partial
	return def
}

type fakeTasks struct {
	sets    map[string]domain.EngineTask
	lookups map[string]SetLookup
}

func (f *fakeTasks) SetByID(id string) (domain.EngineTask, SetLookup) {
	if lookup, ok := f.lookups[id]; ok {
		return f.sets[id], lookup
	}
	if set, ok := f.sets[id]; ok {
		return set, SetFound
	}
	return domain.EngineTask{}, SetGone
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
	// statusFor 按标的给出逐项结果，优先于 status。
	statusFor func(subject string) pipeline.ItemStatus
	err       error
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
	for _, member := range selection.Members {
		for _, subject := range selection.Subjects {
			for at := start; at.Before(end); at = at.Add(time.Minute) {
				status := pipeline.ItemWritten
				switch {
				case f.statusFor != nil:
					status = f.statusFor(subject)
				case f.status != nil:
					status = f.status(selection, at)
				}
				outcome.Items[pipeline.ItemKey{Member: member.Alias, Subject: subject, Period: at}] = status
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
	sets   *fakeTasks
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
	set := domain.EngineTask{Set: domain.FactorTask{TaskID: "fset", SpaceID: "crypto", SourceDatasetID: "bars", ResultDatasetID: "result", Freq: "1m"}, Members: domain.AsMembers(factors...), ResultReady: true}
	f.sets = &fakeTasks{sets: map[string]domain.EngineTask{"fset": set}, lookups: map[string]SetLookup{}}
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
		entries = append(entries, pipeline.GapEntry{TaskID: "fset", Subject: subject, Period: gapT0.Add(time.Duration(minute) * time.Minute),
			Member: factor.FactorID, DefinitionHash: domain.DefinitionHash(factor), TriggerEventID: trigger})
	}
	require.NoError(t, f.ledger.RecordGaps(context.Background(), entries))
	f.now = saved
}

// 发现后过了 settle 才恢复；填充写入确认的项追加 repaired，仍然失败的保持待恢复。
func TestGapFillerRepairsWrittenItemsAndKeepsGaps(t *testing.T) {
	factor := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	f := newGapFixture(t, fakeUniverse{}, factor)
	f.record(t, gapT0, "BTC", 0, "", factor)
	f.record(t, gapT0, "BTC", 1, "", factor)
	f.chunks.status = func(_ recalcexec.Selection, at time.Time) pipeline.ItemStatus {
		if at.Equal(gapT0.Add(time.Minute)) {
			return pipeline.ItemFailed // 01 仍然取不到数据
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
	changed := domain.MustDefinition("ma", "ma", "output: ts_mean(close, 9)\n") // 定义变化，哈希不同
	f.record(t, gapT0, "BTC", 0, "", changed, removed)
	f.now = gapT0.Add(6 * time.Minute)
	f.filler.tick(context.Background())
	require.Empty(t, f.chunks.calls, "没有可恢复的项就不运行")
	require.Zero(t, f.ledger.PendingCount())
}

// 任务已不存在：取消；目录未加载或结果表未就绪：等待，不能当作任务已经不存在。
func TestGapFillerTaskGoneCancelsButNotReadyWaits(t *testing.T) {
	factor := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	f := newGapFixture(t, fakeUniverse{}, factor)
	f.record(t, gapT0, "BTC", 0, "", factor)
	f.now = gapT0.Add(6 * time.Minute)

	f.sets.lookups["fset"] = SetNotReady
	f.filler.tick(context.Background())
	require.Equal(t, 1, f.ledger.PendingCount(), "目录未就绪：等待")

	f.sets.lookups["fset"] = SetGone
	f.filler.tick(context.Background())
	require.Zero(t, f.ledger.PendingCount(), "任务已不再启用：取消")
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
		entries = append(entries, pipeline.GapEntry{TaskID: "fset", Subject: subject, Period: gapT0.Add(time.Duration(minute) * time.Minute),
			Member: factor.FactorID, DefinitionHash: domain.DefinitionHash(factor), TriggerEventID: trigger, Reason: pipeline.SkipReasonWarming})
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
	period              time.Time
	repaired, unwritten int
	done                bool
}

// 预热就绪后立刻补算预热期间跳过的那一期：不等 Settle；空洞跳过的项仍等 Settle，任务还在预热时也等。
// 每批补齐后按周期回调，账本里这一期没有待办了才算补齐。
func TestGapFillerRepairsWarmingSkipsOnceTaskIsWarm(t *testing.T) {
	factor := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	f := newGapFixture(t, fakeUniverse{}, factor)
	state := pipeline.WarmupWarming
	f.filler.warmup = func() []pipeline.WarmupStatus {
		return []pipeline.WarmupStatus{{TaskID: "fset", State: state}, {TaskID: "other", State: pipeline.WarmupReady}}
	}
	var calls []repairedCall
	f.filler.onRepaired = func(taskID string, period RepairedPeriod) {
		require.Equal(t, "fset", taskID)
		calls = append(calls, repairedCall{period: period.Period, repaired: len(period.Repaired), unwritten: len(period.Unwritten), done: period.Done})
	}
	for i := 0; i < maxFillSubjects+1; i++ {
		f.recordWarming(t, fmt.Sprintf("S%03d", i), 0, "evt-0", factor) // 发现于 gapT0
	}
	f.record(t, gapT0, "DOGE", 1, "", factor) // 空洞跳过

	f.now = gapT0.Add(2 * time.Minute)
	f.filler.tick(context.Background())
	require.Empty(t, f.chunks.calls, "还在 Settle 内，任务也还在预热")

	state = pipeline.WarmupReady
	f.filler.tick(context.Background())
	require.Len(t, f.chunks.calls, 2, "预热跳过的 101 个标的分两批，空洞跳过的 DOGE 仍等 Settle")
	for _, call := range f.chunks.calls {
		require.NotContains(t, call.selection.Subjects, "DOGE")
	}
	require.Equal(t, 1, f.ledger.PendingCount())
	require.Equal(t, []repairedCall{{period: gapT0, repaired: maxFillSubjects}, {period: gapT0, repaired: 1, done: true}}, calls,
		"第一批补完时这一期还有待办")
}

// 截面恢复重算整个冻结全集：账本里只有当初有洞的标的，别的标的这次没写成要随回调带回，摘要据此保留失败。
func TestGapFillerReportsUnwrittenCrossSectionItems(t *testing.T) {
	factor := gapFactor("rank", domain.FactorTypeCrossSection, false)
	f := newGapFixture(t, fakeUniverse{subjects: []string{"A", "B"}, found: true}, factor)
	f.record(t, gapT0, "A", 0, "evt-0", factor)
	var got RepairedPeriod
	f.filler.onRepaired = func(_ string, period RepairedPeriod) { got = period }
	f.chunks.statusFor = func(subject string) pipeline.ItemStatus {
		if subject == "B" {
			return pipeline.ItemFailed
		}
		return pipeline.ItemWritten
	}
	f.now = gapT0.Add(6 * time.Minute)
	f.filler.tick(context.Background())
	require.True(t, got.Done)
	require.Equal(t, []pipeline.ItemKey{{Member: "rank", Subject: "A", Period: gapT0}}, got.Repaired)
	require.Equal(t, []pipeline.ItemKey{{Member: "rank", Subject: "B", Period: gapT0}}, got.Unwritten)
}

// 记账时有预热中跳过的项就唤醒跳过恢复；只有空洞跳过的不唤醒。
func TestWarmingKickSinkKicksOnWarmingEntries(t *testing.T) {
	ledger, _, err := gapledger.Open(t.TempDir(), time.Now, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ledger.Close() })
	kicks := 0
	sink := warmingKickSink{GapSink: ledger, kick: func() { kicks++ }}
	entry := pipeline.GapEntry{TaskID: "fset", Subject: "BTC", Period: gapT0, Member: "ma", DefinitionHash: "h"}
	require.NoError(t, sink.RecordGaps(context.Background(), []pipeline.GapEntry{entry}))
	require.Zero(t, kicks)
	entry.Subject, entry.Reason = "ETH", pipeline.SkipReasonWarming
	require.NoError(t, sink.RecordGaps(context.Background(), []pipeline.GapEntry{entry, entry}))
	require.Equal(t, 1, kicks)
	require.Equal(t, 2, ledger.PendingCount())
}
