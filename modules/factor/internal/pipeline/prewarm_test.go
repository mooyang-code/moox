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

// hourlyStore 每次读取都返回 [Start, End) 内每小时一根的 K 线，并记录读取请求。
type hourlyStore struct {
	outputStore
	mu    sync.Mutex
	reads []storageio.ReadRequest
}

func (s *hourlyStore) ReadWindow(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
	frames := emptyFrames(req)
	for _, subject := range req.Subjects {
		for at := req.Start; at.Before(req.End); at = at.Add(time.Hour) {
			frames[subject].Rows = append(frames[subject].Rows, []any{at, "", 1.0})
		}
	}
	s.mu.Lock()
	s.reads = append(s.reads, req)
	s.mu.Unlock()
	return frames, nil
}

func (s *hourlyStore) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reads)
}

func (s *hourlyStore) lastRead() storageio.ReadRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads[len(s.reads)-1]
}

var hourlySet = domain.FactorSet{
	SetID: "fset_1h", SpaceID: "crypto", SourceDatasetID: "dataset_bars", ResultDatasetID: "result", Freq: "1h",
	Status: domain.SetStatusEnabled, SubjectMode: domain.SubjectModeAll,
}

func hourlyPlan(t *testing.T, period time.Time, factors ...domain.FactorDef) Plan {
	t.Helper()
	if len(factors) == 0 {
		factors = []domain.FactorDef{{FactorID: "f", FactorType: domain.FactorTypeTimeSeries, LookbackPeriods: 3}}
	}
	plan, err := BuildLivePlan(periodclock.Continuous{}, LiveInput{
		Set: hourlySet, Factors: factors, PeriodTime: period, Universe: []string{"BTC", "ETH"},
		CarryColumns: []string{"close"}, TriggerEventID: "evt-" + period.Format("1504"),
	})
	require.NoError(t, err)
	return plan
}

// 生产现象（2026-10-16）：引擎 16:10（08:10Z）重启，1h 集合要到 17:00 那一期到达才开始预热，08:00Z 这一期全部标的预热中，
// 结果整根缺失。主动预热之后，第一期到达时窗口已经载入，这一期照常完整计算，只读窗口之后新增的 K 线。
func TestPrewarmLoadsWindowsBeforeFirstLivePeriod(t *testing.T) {
	store := &hourlyStore{}
	runner := NewRunner(store, nil, periodclock.Continuous{}, Config{BackgroundWarmup: true})
	restart := time.Date(2026, 10, 16, 8, 10, 0, 0, time.UTC)

	// 预热到最近一根已收盘 K 线（07:00Z）的前一根：06:00Z。
	require.Equal(t, 2, runner.Prewarm(hourlyPlan(t, restart.Truncate(time.Hour).Add(-2*time.Hour))))
	require.Equal(t, []WarmupStatus{{SetID: "fset_1h", State: WarmupWarming, WarmSubjects: 0, ExpectedSubjects: 2}}, runner.WarmupStatuses())
	require.Zero(t, runner.Prewarm(hourlyPlan(t, restart.Truncate(time.Hour).Add(-time.Hour))), "已经有记录的集合不重复预热")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runner.RunWarmup(ctx)
	require.Eventually(t, func() bool {
		statuses := runner.WarmupStatuses()
		return len(statuses) == 1 && statuses[0].State == WarmupReady
	}, time.Second, 5*time.Millisecond, "预热完成后状态立刻变成 ready，不等下一个实时周期")
	require.Equal(t, WarmupStatus{SetID: "fset_1h", State: WarmupReady, WarmSubjects: 2, ExpectedSubjects: 2}, runner.WarmupStatuses()[0])
	warmed := store.lastRead()
	require.Equal(t, time.Date(2026, 10, 16, 4, 0, 0, 0, time.UTC), warmed.Start)
	require.Equal(t, time.Date(2026, 10, 16, 7, 0, 0, 0, time.UTC), warmed.End, "不读还没采到的最新一根")

	period := time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)
	loaded, err := runner.Load(context.Background(), hourlyPlan(t, period))
	require.NoError(t, err)
	require.Empty(t, loaded.WarmingSubjects, "第一期到达时已经预热完成")
	require.Equal(t, []string{"BTC", "ETH"}, loaded.Available)
	require.Equal(t, period.Add(-time.Hour), store.lastRead().Start, "只读预热窗口之后的 K 线")
	require.Len(t, loaded.Frames["BTC"].Rows, 3)
}

// 一批后台预热载入窗口后回调（引擎据此立刻补算预热期间跳过的那几期），回调时窗口已经在缓存里。
func TestWarmupDoneHookRunsAfterWindowsAreLoaded(t *testing.T) {
	store := &hourlyStore{}
	runner := NewRunner(store, nil, periodclock.Continuous{}, Config{BackgroundWarmup: true})
	done := make(chan string, 1)
	runner.OnWarmupDone(func(setID string) {
		require.Equal(t, WarmupReady, runner.WarmupStatuses()[0].State, "回调时预热状态已经就绪")
		done <- setID
	})
	require.Equal(t, 2, runner.Prewarm(hourlyPlan(t, time.Date(2026, 10, 16, 6, 0, 0, 0, time.UTC))))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runner.RunWarmup(ctx)
	select {
	case setID := <-done:
		require.Equal(t, "fset_1h", setID)
	case <-time.After(time.Second):
		t.Fatal("预热完成后没有回调")
	}
	loaded, err := runner.Load(context.Background(), hourlyPlan(t, time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)))
	require.NoError(t, err)
	require.Empty(t, loaded.WarmingSubjects)
}

// 只回看一根的集合：预热窗口终点比第一期的窗口起点早一根，落后不多，本期直接读整窗口，不再当作预热中。
func TestPrewarmConnectsToFirstPeriodWithSingleBarLookback(t *testing.T) {
	store := &hourlyStore{}
	runner := NewRunner(store, nil, periodclock.Continuous{}, Config{BackgroundWarmup: true})
	single := domain.FactorDef{FactorID: "rank", FactorType: domain.FactorTypeCrossSection, LookbackPeriods: 1}
	require.Equal(t, 2, runner.Prewarm(hourlyPlan(t, time.Date(2026, 10, 16, 6, 0, 0, 0, time.UTC), single)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runner.RunWarmup(ctx)
	require.Eventually(t, func() bool { return store.readCount() == 1 && runner.WarmupStatuses()[0].State == WarmupReady }, time.Second, 5*time.Millisecond)

	period := time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)
	loaded, err := runner.Load(context.Background(), hourlyPlan(t, period, single))
	require.NoError(t, err)
	require.Empty(t, loaded.WarmingSubjects)
	require.Equal(t, period, store.lastRead().Start)
	require.Len(t, loaded.Frames["BTC"].Rows, 1)

	loaded, err = runner.Load(context.Background(), hourlyPlan(t, period.Add(time.Hour), single))
	require.NoError(t, err)
	require.Empty(t, loaded.WarmingSubjects, "读到的整窗口写回缓存，下一期照常衔接")
	require.Equal(t, period.Add(time.Hour), store.lastRead().Start)
}

func TestPrewarmNeedsBackgroundWarmup(t *testing.T) {
	runner := NewRunner(&hourlyStore{}, nil, periodclock.Continuous{}, Config{})
	require.Zero(t, runner.Prewarm(hourlyPlan(t, time.Date(2026, 10, 16, 6, 0, 0, 0, time.UTC))))
	require.Empty(t, runner.WarmupStatuses())
}

// 预热中被跳过的 (因子, 标的, 周期) 记入账本，原因是 warming，带触发事件（截面因子恢复时据此读取冻结全集）。
func TestRunRecordsWarmingSkipsForRecovery(t *testing.T) {
	sink := &recordingSink{}
	store := &hourlyStore{}
	runner := NewRunner(store, &fakeExecutor{}, periodclock.Continuous{}, Config{BackgroundWarmup: true, GapSink: sink})
	factors := []domain.FactorDef{
		testPipelineFactor("mom", domain.FactorTypeTimeSeries, 3, false),
		testPipelineFactor("rank", domain.FactorTypeCrossSection, 3, false),
	}
	period := time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)
	plan := hourlyPlan(t, period, factors...)

	outcome, err := runner.Run(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, "degraded", outcome.Status)
	require.Equal(t, 2, outcome.Warming)
	require.Len(t, store.markers, 1, "marker 照常上报，预热中的标的列为失败")
	require.Equal(t, []string{"BTC", "ETH"}, store.markers[0].FailedSubjects)

	require.Len(t, sink.entries, 4)
	for _, entry := range sink.entries {
		require.Equal(t, SkipReasonWarming, entry.Reason)
		require.Equal(t, period, entry.Period)
		require.Equal(t, "evt-0800", entry.TriggerEventID)
		require.Empty(t, entry.Missing)
	}
	require.ElementsMatch(t, []string{"BTC|mom", "BTC|rank", "ETH|mom", "ETH|rank"}, func() []string {
		var keys []string
		for _, entry := range sink.entries {
			keys = append(keys, entry.Subject+"|"+entry.FactorID)
		}
		return keys
	}())
}
