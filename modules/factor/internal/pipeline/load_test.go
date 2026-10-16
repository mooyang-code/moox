package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

func TestLoadUsesMaxLookbackRange(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	plan, err := BuildLivePlan(periodclock.Continuous{}, LiveInput{
		Set: domain.FactorSet{SpaceID: "crypto", SourceDatasetID: "dataset_bars", Freq: "1m", Status: domain.SetStatusEnabled, SubjectMode: domain.SubjectModeAll},
		Factors: []domain.FactorDef{
			{FactorID: "short", LookbackPeriods: 5},
			{FactorID: "long", LookbackPeriods: 20},
		},
		PeriodTime: target, Universe: []string{"BTC"}, CarryColumns: []string{"close", "volume"},
	})
	require.NoError(t, err)
	store := &fakeStore{read: func(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
		require.Equal(t, target.Add(-19*time.Minute), req.Start)
		require.Equal(t, target.Add(time.Minute), req.End)
		require.Equal(t, []string{"close", "volume"}, req.Columns)
		return emptyFrames(req), nil
	}}
	loaded, err := NewRunner(store, nil, periodclock.Continuous{}, Config{}).Load(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, []string{"BTC"}, loaded.Available)
}

func TestLoadRecalcExtendsReadRangeByLookback(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	factors := []domain.FactorDef{
		{FactorID: "short", LookbackPeriods: 5},
		{FactorID: "long", LookbackPeriods: 20},
	}
	plan := Plan{Mode: ModeRecalc, Set: domain.FactorSet{SpaceID: "crypto", SourceDatasetID: "dataset_bars", Freq: "1m"}, Factors: factors,
		TargetStart: start, TargetEnd: start.Add(3 * time.Minute), Expected: []string{"BTC"}, Available: []string{"BTC"}, CarryColumns: []string{"close"}}
	store := &fakeStore{read: func(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
		require.Equal(t, start.Add(-19*time.Minute), req.Start)
		require.Equal(t, start.Add(3*time.Minute), req.End)
		return emptyFrames(req), nil
	}}
	_, err := NewRunner(store, nil, periodclock.Continuous{}, Config{}).Load(context.Background(), plan)
	require.NoError(t, err)
}

func TestLoadBatchesSubjectsAndLimitsConcurrency(t *testing.T) {
	subjects := make([]string, 250)
	for i := range subjects {
		subjects[i] = fmt.Sprintf("S%03d", i)
	}
	var active, maxActive, calls atomic.Int32
	store := &fakeStore{read: func(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
		calls.Add(1)
		current := active.Add(1)
		for previous := maxActive.Load(); current > previous && !maxActive.CompareAndSwap(previous, current); previous = maxActive.Load() {
		}
		time.Sleep(15 * time.Millisecond)
		active.Add(-1)
		return emptyFrames(req), nil
	}}
	plan := loadPlan(subjects)
	loaded, err := NewRunner(store, nil, periodclock.Continuous{}, Config{ReadBatchSubjects: 100, ReadWorkers: 2}).Load(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, int32(3), calls.Load())
	require.LessOrEqual(t, maxActive.Load(), int32(2))
	require.Len(t, loaded.Frames, 250)
}

func TestLoadBatchFailureMarksSubjectsFailed(t *testing.T) {
	var mu sync.Mutex
	attempts := map[string]int{}
	store := &fakeStore{read: func(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
		mu.Lock()
		defer mu.Unlock()
		first := req.Subjects[0]
		attempts[first]++
		if first == "A" {
			return nil, fmt.Errorf("%w: unavailable", storageio.ErrInfra)
		}
		return emptyFrames(req), nil
	}}
	loaded, err := NewRunner(store, nil, periodclock.Continuous{}, Config{ReadBatchSubjects: 2, ReadWorkers: 1, ReadRetries: 2}).Load(context.Background(), loadPlan([]string{"A", "B", "C", "D"}))
	require.NoError(t, err)
	require.Equal(t, []string{"A", "B"}, loaded.FailedSubjects)
	require.Equal(t, []string{"C", "D"}, loaded.Available)
	require.Equal(t, 3, attempts["A"])
	require.Len(t, loaded.Frames, 2)
}

func TestLoadAllBatchesFailReturnsErrInfra(t *testing.T) {
	store := &fakeStore{read: func(context.Context, storageio.ReadRequest) (map[string]*storageio.Frame, error) {
		return nil, errors.New("temporary network failure")
	}}
	_, err := NewRunner(store, nil, periodclock.Continuous{}, Config{ReadBatchSubjects: 1, ReadWorkers: 2, ReadRetries: 1}).Load(context.Background(), loadPlan([]string{"A", "B"}))
	require.ErrorIs(t, err, storageio.ErrInfra)
}

func loadPlan(subjects []string) Plan {
	return Plan{
		Mode: ModeLive, Set: domain.FactorSet{SpaceID: "crypto", SourceDatasetID: "dataset_bars", Freq: "1m"},
		TargetStart: time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC), TargetEnd: time.Date(2026, 10, 4, 0, 1, 0, 0, time.UTC),
		Expected: append([]string(nil), subjects...), Available: append([]string(nil), subjects...), CarryColumns: []string{"close"},
	}
}

func emptyFrames(req storageio.ReadRequest) map[string]*storageio.Frame {
	frames := make(map[string]*storageio.Frame, len(req.Subjects))
	for _, subject := range req.Subjects {
		frames[subject] = &storageio.Frame{SubjectID: subject, Columns: append([]string{"data_time", "series_tag"}, req.Columns...), Rows: [][]any{}}
	}
	return frames
}

type fakeStore struct {
	read         func(context.Context, storageio.ReadRequest) (map[string]*storageio.Frame, error)
	mu           sync.Mutex
	readRequests []storageio.ReadRequest
}

func (f *fakeStore) DatasetColumns(context.Context, string, string) ([]string, error) {
	return nil, nil
}

func (f *fakeStore) ReadWindow(ctx context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
	f.mu.Lock()
	f.readRequests = append(f.readRequests, req)
	f.mu.Unlock()
	return f.read(ctx, req)
}

func (*fakeStore) WriteRows(context.Context, string, string, string, []storageio.ResultRow) error {
	return nil
}
func (*fakeStore) ReportComputed(context.Context, storageio.PeriodMarker) error { return nil }
func (*fakeStore) ComputedExists(context.Context, string, string, string, int64) (bool, error) {
	return false, nil
}

func TestReadDeadlinesDifferForLiveAndRecalc(t *testing.T) {
	type observed struct {
		hasDeadline bool
		deadline    time.Duration
		pageTimeout time.Duration
	}
	got := map[Mode]observed{}
	for _, mode := range []Mode{ModeLive, ModeRecalc} {
		var seen observed
		store := &fakeStore{read: func(ctx context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
			deadline, ok := ctx.Deadline()
			seen = observed{hasDeadline: ok, deadline: time.Until(deadline), pageTimeout: req.PageTimeout}
			return emptyFrames(req), nil
		}}
		runner := NewRunner(store, nil, periodclock.Continuous{}, Config{ReadTimeout: 10 * time.Second})
		target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
		plan := computePlan(target, []domain.FactorDef{testPipelineFactor("momentum", domain.FactorTypeTimeSeries, 2, false)}, []string{"BTC"})
		plan.Mode = mode
		_, err := runner.Load(context.Background(), plan)
		require.NoError(t, err)
		got[mode] = seen
	}
	// A live period fails fast as a whole batch.
	require.True(t, got[ModeLive].hasDeadline)
	require.LessOrEqual(t, got[ModeLive].deadline, 10*time.Second)
	require.Zero(t, got[ModeLive].pageTimeout)
	// A backfill batch is bounded per page, never as a whole.
	require.False(t, got[ModeRecalc].hasDeadline)
	require.Equal(t, 10*time.Second, got[ModeRecalc].pageTimeout)
}

func TestLoadLiveReusesPreviousWindow(t *testing.T) {
	set := domain.FactorSet{SetID: "fset", SpaceID: "crypto", SourceDatasetID: "dataset_bars", Freq: "1m", Status: domain.SetStatusEnabled, SubjectMode: domain.SubjectModeAll}
	livePlan := func(target time.Time, universe ...string) Plan {
		plan, err := BuildLivePlan(periodclock.Continuous{}, LiveInput{
			Set: set, Factors: []domain.FactorDef{{FactorID: "f", LookbackPeriods: 3}},
			PeriodTime: target, Universe: universe, CarryColumns: []string{"close"},
		})
		require.NoError(t, err)
		return plan
	}
	var mu sync.Mutex
	starts := map[string]time.Time{}
	store := &fakeStore{read: func(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
		frames := emptyFrames(req)
		mu.Lock()
		defer mu.Unlock()
		for _, subject := range req.Subjects {
			starts[subject] = req.Start
			for at := req.Start; at.Before(req.End); at = at.Add(time.Minute) {
				frames[subject].Rows = append(frames[subject].Rows, []any{at, "", float64(at.Minute())})
			}
		}
		return frames, nil
	}}
	runner := NewRunner(store, nil, periodclock.Continuous{}, Config{})
	t0 := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)

	_, err := runner.Load(context.Background(), livePlan(t0, "BTC"))
	require.NoError(t, err)
	require.Equal(t, t0.Add(-2*time.Minute), starts["BTC"])

	t1 := t0.Add(time.Minute)
	loaded, err := runner.Load(context.Background(), livePlan(t1, "BTC", "ETH"))
	require.NoError(t, err)
	require.Equal(t, t1, starts["BTC"], "a warm subject reads only the target bar")
	require.Equal(t, t1.Add(-2*time.Minute), starts["ETH"], "a cold subject reads the whole window")
	for _, subject := range []string{"BTC", "ETH"} {
		rows := loaded.Frames[subject].Rows
		require.Len(t, rows, 3, subject)
		require.Equal(t, t1.Add(-2*time.Minute), rows[0][0])
		require.Equal(t, t1, rows[2][0])
	}

	t3 := t1.Add(2 * time.Minute)
	loaded, err = runner.Load(context.Background(), livePlan(t3, "BTC"))
	require.NoError(t, err)
	require.Equal(t, t1.Add(time.Minute), starts["BTC"], "a skipped period reads from the end of the cached window")
	require.Len(t, loaded.Frames["BTC"].Rows, 3)

	t9 := t3.Add(6 * time.Minute)
	_, err = runner.Load(context.Background(), livePlan(t9, "BTC"))
	require.NoError(t, err)
	require.Equal(t, t9.Add(-2*time.Minute), starts["BTC"], "a window past the cache is read in full")
}

func TestLoadLiveOlderPeriodKeepsNewerWindow(t *testing.T) {
	set := domain.FactorSet{SetID: "fset", SpaceID: "crypto", SourceDatasetID: "dataset_bars", Freq: "1m", Status: domain.SetStatusEnabled, SubjectMode: domain.SubjectModeAll}
	livePlan := func(target time.Time) Plan {
		plan, err := BuildLivePlan(periodclock.Continuous{}, LiveInput{
			Set: set, Factors: []domain.FactorDef{{FactorID: "f", LookbackPeriods: 3}},
			PeriodTime: target, Universe: []string{"BTC"}, CarryColumns: []string{"close"},
		})
		require.NoError(t, err)
		return plan
	}
	var start time.Time
	store := &fakeStore{read: func(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
		start = req.Start
		return emptyFrames(req), nil
	}}
	runner := NewRunner(store, nil, periodclock.Continuous{}, Config{})
	t0 := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	for _, target := range []time.Time{t0, t0.Add(-time.Minute)} {
		_, err := runner.Load(context.Background(), livePlan(target))
		require.NoError(t, err)
	}
	_, err := runner.Load(context.Background(), livePlan(t0.Add(time.Minute)))
	require.NoError(t, err)
	require.Equal(t, t0.Add(time.Minute), start)
}

func TestLoadLiveWarmsUpInBackground(t *testing.T) {
	set := domain.FactorSet{SetID: "fset", SpaceID: "crypto", SourceDatasetID: "dataset_bars", Freq: "1m", Status: domain.SetStatusEnabled, SubjectMode: domain.SubjectModeAll}
	livePlan := func(target time.Time) Plan {
		plan, err := BuildLivePlan(periodclock.Continuous{}, LiveInput{
			Set: set, Factors: []domain.FactorDef{{FactorID: "f", LookbackPeriods: 3}},
			PeriodTime: target, Universe: []string{"BTC", "ETH"}, CarryColumns: []string{"close"},
		})
		require.NoError(t, err)
		return plan
	}
	var mu sync.Mutex
	var reads []storageio.ReadRequest
	store := &fakeStore{read: func(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
		frames := emptyFrames(req)
		for _, subject := range req.Subjects {
			for at := req.Start; at.Before(req.End); at = at.Add(time.Minute) {
				frames[subject].Rows = append(frames[subject].Rows, []any{at, "", 1.0})
			}
		}
		mu.Lock()
		reads = append(reads, req)
		mu.Unlock()
		return frames, nil
	}}
	runner := NewRunner(store, nil, periodclock.Continuous{}, Config{BackgroundWarmup: true})
	t0 := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)

	loaded, err := runner.Load(context.Background(), livePlan(t0))
	require.NoError(t, err)
	require.Empty(t, loaded.Available)
	require.Equal(t, []string{"BTC", "ETH"}, loaded.WarmingSubjects)
	require.Equal(t, []string{"BTC", "ETH"}, loaded.FailedSubjects)
	require.Empty(t, reads, "a warming subject is not read inside the period")
	require.Equal(t, []WarmupStatus{{SetID: "fset", State: WarmupWarming, WarmSubjects: 0, ExpectedSubjects: 2}}, runner.WarmupStatuses())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runner.RunWarmup(ctx)
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(reads) == 1
	}, time.Second, 5*time.Millisecond)
	require.Equal(t, t0.Add(-2*time.Minute), reads[0].Start)
	require.Equal(t, []string{"BTC", "ETH"}, reads[0].Subjects)

	t1 := t0.Add(time.Minute)
	require.Eventually(t, func() bool {
		loaded, err = runner.Load(context.Background(), livePlan(t1))
		return err == nil && len(loaded.WarmingSubjects) == 0
	}, time.Second, 5*time.Millisecond)
	require.Equal(t, []string{"BTC", "ETH"}, loaded.Available)
	mu.Lock()
	last := reads[len(reads)-1]
	mu.Unlock()
	require.Equal(t, t1, last.Start, "a warm period reads only its own bar")
	require.Len(t, loaded.Frames["BTC"].Rows, 3)
	require.Equal(t, WarmupReady, runner.WarmupStatuses()[0].State)

	far := t1.Add(time.Duration(liveCatchUpBars+2) * time.Minute) // 缓存终点是 t1 之后一分钟：落后 liveCatchUpBars+1 根
	loaded, err = runner.Load(context.Background(), livePlan(far))
	require.NoError(t, err)
	require.Equal(t, []string{"BTC", "ETH"}, loaded.WarmingSubjects, "a subject too far behind warms up again")
}

func recalcLoadPlan(subjects ...string) Plan {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	plan := computePlan(target, []domain.FactorDef{testPipelineFactor("momentum", domain.FactorTypeTimeSeries, 2, false)}, subjects)
	plan.Mode = ModeRecalc
	return plan
}

func manySubjects(n int) []string {
	subjects := make([]string, n)
	for i := range subjects {
		subjects[i] = fmt.Sprintf("S%02d", i)
	}
	return subjects
}

// 补算整窗口读取时，并发读取按 BulkReadWorkers 收敛：ReadWorkers 随引擎机器的核数增长，DataNode 却没有那么多核。
func TestBulkReadsAreCappedByBulkReadWorkers(t *testing.T) {
	var mu sync.Mutex
	inFlight, peak := 0, 0
	store := &fakeStore{read: func(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
		mu.Lock()
		inFlight++
		peak = max(peak, inFlight)
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()
		return emptyFrames(req), nil
	}}
	runner := NewRunner(store, nil, periodclock.Continuous{}, Config{ReadBatchSubjects: 1, ReadWorkers: 16, BulkReadWorkers: 3})
	_, err := runner.Load(context.Background(), recalcLoadPlan(manySubjects(24)...))
	require.NoError(t, err)
	require.Equal(t, 3, peak, "补算读取的并发不超过 BulkReadWorkers")

	// 实时读取不受这个上限影响。
	peak = 0
	live := loadPlan(manySubjects(24))
	_, err = runner.Load(context.Background(), live)
	require.NoError(t, err)
	require.Greater(t, peak, 3)
}

// 连续失败的批次达到上限后，其余批次不再发请求：每个批次各自耗尽超时和重试，只会把已经读不动的 Storage 压得更久。
func TestBulkReadCircuitOpensAfterConsecutiveFailedBatches(t *testing.T) {
	var calls atomic.Int32
	store := &fakeStore{read: func(context.Context, storageio.ReadRequest) (map[string]*storageio.Frame, error) {
		calls.Add(1)
		return nil, errors.New("i/o timeout")
	}}
	config := Config{ReadBatchSubjects: 1, ReadWorkers: 1, BulkReadWorkers: 1, ReadRetries: 1, ReadRetryBackoff: time.Millisecond, ReadBreakerFailures: 2}
	_, err := NewRunner(store, nil, periodclock.Continuous{}, config).Load(context.Background(), recalcLoadPlan(manySubjects(10)...))
	require.ErrorIs(t, err, storageio.ErrInfra)
	require.Equal(t, int32(4), calls.Load(), "2 个批次各读 2 次（1 次重试）后断路，其余 8 个批次不再请求")

	// 实时读取没有断路：每个批次都要读，失败也不影响下一期。
	calls.Store(0)
	live := config
	live.ReadWorkers = 1
	_, err = NewRunner(store, nil, periodclock.Continuous{}, live).Load(context.Background(), loadPlan(manySubjects(10)))
	require.ErrorIs(t, err, storageio.ErrInfra)
	require.Equal(t, int32(20), calls.Load())

	// 成功的批次会清零计数：失败、成功交替时不会断路。
	calls.Store(0)
	flaky := &fakeStore{read: func(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
		if calls.Add(1)%3 != 0 {
			return nil, errors.New("i/o timeout")
		}
		return emptyFrames(req), nil
	}}
	flakyConfig := config
	flakyConfig.ReadRetries = 2
	_, err = NewRunner(flaky, nil, periodclock.Continuous{}, flakyConfig).Load(context.Background(), recalcLoadPlan(manySubjects(6)...))
	require.NoError(t, err)
	require.Equal(t, int32(18), calls.Load(), "每个批次读 3 次，第 3 次成功，断路计数始终归零")
}

func TestBulkReadRetriesBackOffButLiveReadsDoNot(t *testing.T) {
	gaps := func(plan Plan, backoff time.Duration) time.Duration {
		var first, second time.Time
		var calls atomic.Int32
		store := &fakeStore{read: func(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
			switch calls.Add(1) {
			case 1:
				first = time.Now()
				return nil, errors.New("i/o timeout")
			default:
				second = time.Now()
				return emptyFrames(req), nil
			}
		}}
		runner := NewRunner(store, nil, periodclock.Continuous{}, Config{ReadBatchSubjects: 10, ReadWorkers: 1, ReadRetries: 1, ReadRetryBackoff: backoff})
		_, err := runner.Load(context.Background(), plan)
		require.NoError(t, err)
		return second.Sub(first)
	}
	require.GreaterOrEqual(t, gaps(recalcLoadPlan("A"), 100*time.Millisecond), 80*time.Millisecond, "补算读取失败后先退避再重试（含 20% 抖动）")
	require.Less(t, gaps(loadPlan([]string{"A"}), 100*time.Millisecond), 50*time.Millisecond, "实时读取立即重试")
}

func TestRetryDelayDoublesWithJitterAndIsCapped(t *testing.T) {
	within := func(got, want time.Duration) bool { return got >= want*8/10 && got <= want*12/10 }
	for attempt, want := range []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second} {
		for i := 0; i < 50; i++ {
			require.True(t, within(retryDelay(2*time.Second, attempt), want), "attempt %d", attempt)
		}
	}
	for i := 0; i < 50; i++ {
		require.LessOrEqual(t, retryDelay(2*time.Second, 20), maxReadRetryDelay*12/10)
	}
}
