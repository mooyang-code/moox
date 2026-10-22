package pipeline

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/serieswindow"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

func TestLoadRequiresDataLayer(t *testing.T) {
	_, err := NewRunner(&fakeStore{}, nil, periodclock.Continuous{}, Config{}).Load(context.Background(), loadPlan([]string{"A"}))
	require.Error(t, err)
}

func TestLoadMergesDataFailuresAndWarming(t *testing.T) {
	data := &frameData{
		read: func(_ context.Context, plan Plan, _ time.Time) (map[string]*storageio.Frame, error) {
			frames := map[string]*storageio.Frame{}
			for _, subject := range plan.Available {
				frames[subject] = &storageio.Frame{SubjectID: subject, Columns: []string{"data_time", "series_tag", "close"}, Rows: [][]any{{plan.TargetEnd.Add(-time.Minute), "", 1.0}}}
			}
			return frames, nil
		},
		failed: []string{"B"}, warming: []string{"C"},
	}
	plan := loadPlan([]string{"A", "B", "C", "D"})
	plan.UpstreamFailed = []string{"E"}
	loaded, err := NewRunner(&fakeStore{}, data, periodclock.Continuous{}, Config{}).Load(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, []string{"A", "D"}, loaded.Available)
	require.Equal(t, []string{"B", "C", "E"}, loaded.FailedSubjects)
	require.Equal(t, []string{"C"}, loaded.WarmingSubjects, "预热中的标的也列在失败名单里")
	require.NotNil(t, loaded.View)
	require.Len(t, loaded.Frames, 4)
}

func TestLoadSkipsDataLayerWithoutAvailableSubjects(t *testing.T) {
	data := &frameData{read: func(context.Context, Plan, time.Time) (map[string]*storageio.Frame, error) {
		t.Fatal("没有可用标的时不应读取")
		return nil, nil
	}}
	plan := loadPlan(nil)
	plan.UpstreamFailed = []string{"X"}
	loaded, err := NewRunner(&fakeStore{}, data, periodclock.Continuous{}, Config{}).Load(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, []string{"X"}, loaded.FailedSubjects)
}

func TestLoadRecalcReadsFromLookbackStart(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	plan := Plan{Mode: ModeRecalc, Set: domain.FactorSet{SpaceID: "crypto", SourceDatasetID: "dataset_bars", Freq: "1m"},
		Factors:     []domain.FactorDef{{FactorID: "short", LookbackPeriods: 5}, {FactorID: "long", LookbackPeriods: 20}},
		TargetStart: start, TargetEnd: start.Add(3 * time.Minute), Expected: []string{"BTC"}, Available: []string{"BTC"}, CarryColumns: []string{"close"}}
	got, err := ReadStart(periodclock.Continuous{}, plan)
	require.NoError(t, err)
	require.Equal(t, start.Add(-19*time.Minute), got)

	live, err := BuildLivePlan(periodclock.Continuous{}, LiveInput{
		Set:     domain.FactorSet{SpaceID: "crypto", SourceDatasetID: "dataset_bars", Freq: "1m", Status: domain.SetStatusEnabled, SubjectMode: domain.SubjectModeAll},
		Factors: []domain.FactorDef{{FactorID: "short", LookbackPeriods: 5}, {FactorID: "long", LookbackPeriods: 20}}, PeriodTime: start, Universe: []string{"BTC"}, CarryColumns: []string{"close"},
	})
	require.NoError(t, err)
	got, err = ReadStart(periodclock.Continuous{}, live)
	require.NoError(t, err)
	require.Equal(t, start.Add(-19*time.Minute), got, "实时计划的 TargetStart 已经是回看起点")
}

func loadPlan(subjects []string) Plan {
	return Plan{
		Mode: ModeLive, Set: domain.FactorSet{SpaceID: "crypto", SourceDatasetID: "dataset_bars", Freq: "1m"},
		TargetStart: time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC), TargetEnd: time.Date(2026, 10, 4, 0, 1, 0, 0, time.UTC),
		Expected: append([]string(nil), subjects...), Available: append([]string(nil), subjects...), CarryColumns: []string{"close"},
	}
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

// 本期同步之后仍没有目标周期行的标的、以及窗口还没载入完成的标的，各自记入账本，原因不同；上游失败的只记一次。
func TestRunRecordsUnavailableAndWarmingSubjects(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	subjects := []string{"BTC", "ETH", "SOL", "XRP"}
	clock := periodclock.Continuous{}
	store := newMemoryStorage(subjects, target, 10)
	data := store.data().(*frameData)
	data.failed, data.warming = []string{"ETH"}, []string{"SOL"}
	sink := &recordingSink{}
	runner := NewRunner(store, data, clock, Config{ComputeWorkers: 1, GapSink: sink})
	input := e2eLiveInput(target, subjects, []domain.FactorDef{e2eMean()})
	input.UpstreamFailed = []string{"XRP"}
	plan, err := BuildLivePlan(clock, input)
	require.NoError(t, err)

	outcome, err := runner.Run(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, "degraded", outcome.Status)
	require.Equal(t, []string{"ETH", "SOL", "XRP"}, outcome.FailedSubjects)
	require.Equal(t, 1, outcome.Warming)
	reasons := map[string]string{}
	for _, entry := range sink.entries {
		require.Equal(t, "evt-1", entry.TriggerEventID)
		require.Equal(t, target, entry.Period)
		reasons[entry.Subject] = entry.Reason
	}
	require.Equal(t, map[string]string{"ETH": SkipReasonUpstream, "SOL": SkipReasonWarming, "XRP": SkipReasonUpstream}, reasons)
}

// 面板直接按段复制窗口：回看起点早于窗口的部分是缺期（NaN 且没有源行），is_filled 虚拟列来自填充标记。
func TestBuildPanelCopiesWindowRunsAndFilledColumn(t *testing.T) {
	step := int64(60)
	t0 := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	fields := []seriesstore.Field{{Name: "close", Type: seriesstore.TypeFloat64}}
	cols := seriesstore.NewColumns(fields, 4)
	for i := 0; i < 4; i++ {
		cols.AppendRow(t0.Add(time.Duration(10+i)*time.Minute), "", i == 2, seriesstore.Float(float64(100+i)))
	}
	view, err := serieswindow.FromColumns(step, []string{"close"}, map[string]*seriesstore.Columns{"BTC": cols})
	require.NoError(t, err)

	// 网格从 t0+8m 开始、长度 8：窗口只覆盖网格的后 4 格。
	grid := computeGrid{start: t0.Add(8 * time.Minute), duration: time.Minute, first: 7, last: 7}
	panel, err := buildPanel(grid, []string{"close", serieswindow.FilledColumn}, []string{"BTC"}, view)
	require.NoError(t, err)
	index, _ := panel.SeriesIndex(execKey("BTC", ""))
	for tt := 0; tt < 2; tt++ {
		require.False(t, panel.Present(index, tt), "窗口之前是缺期")
	}
	for tt := 2; tt < 6; tt++ {
		require.True(t, panel.Present(index, tt))
		require.Equal(t, float64(100+tt-2), panel.Value(0, index, tt))
	}
	require.Equal(t, []float64{0, 0, 1, 0}, []float64{panel.Value(1, index, 2), panel.Value(1, index, 3), panel.Value(1, index, 4), panel.Value(1, index, 5)})
	require.False(t, panel.Present(index, 6))

	_, err = buildPanel(grid, []string{"volume"}, []string{"BTC"}, view)
	require.ErrorContains(t, err, "窗口缺少输入列")
}

type countingAdmission struct {
	mu       sync.Mutex
	reserved []int64
	released int
	err      error
}

func (a *countingAdmission) Reserve(_ context.Context, bytes int64) (func(), error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return nil, a.err
	}
	a.reserved = append(a.reserved, bytes)
	return func() { a.mu.Lock(); a.released++; a.mu.Unlock() }, nil
}

// 计算前向准入器预留估算的内存、计算后释放；预留失败时本期全部因子按计算失败处理。
func TestComputeReservesMemoryAdmission(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	factor := testPipelineFactor("mean", domain.FactorTypeTimeSeries, 2, false)
	plan := computePlan(target, []domain.FactorDef{factor}, []string{"A", "B"})

	admission := &countingAdmission{}
	runner := NewRunner(&fakeStore{}, nil, periodclock.Continuous{}, Config{MemoryAdmission: admission})
	computation, err := runner.Compute(context.Background(), plan, framesForBars(plan.Available, target, 2))
	require.NoError(t, err)
	require.NoError(t, computation.Results[factor.FactorID]["A"].Err)
	require.Len(t, admission.reserved, 1)
	require.Positive(t, admission.reserved[0])
	require.Equal(t, 1, admission.released)

	denied := &countingAdmission{err: errors.New("预算已满")}
	runner = NewRunner(&fakeStore{}, nil, periodclock.Continuous{}, Config{MemoryAdmission: denied})
	computation, err = runner.Compute(context.Background(), plan, framesForBars(plan.Available, target, 2))
	require.NoError(t, err)
	require.ErrorIs(t, computation.Results[factor.FactorID]["A"].Err, ErrMemoryBudget)
	require.Equal(t, "degraded", computation.FactorStates[factor.FactorID].Status)
}
