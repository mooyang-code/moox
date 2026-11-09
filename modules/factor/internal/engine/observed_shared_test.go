package engine

import (
	"context"
	"math"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/localdata"
	"github.com/mooyang-code/moox/modules/factor/internal/memadmit"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

// sharedStorage 是内存里的 Storage：源数据集 dataset_a 的 close/open（1m），同时接收结果行。
type sharedStorage struct {
	mu      sync.Mutex
	bars    map[string]map[int64]float64 // 标的 → 分钟时间戳 → close
	results map[string]map[string]any    // 结果数据集/标的/时间 → 字段
}

func newSharedStorage() *sharedStorage {
	return &sharedStorage{bars: map[string]map[int64]float64{}, results: map[string]map[string]any{}}
}

func (s *sharedStorage) put(subject string, base float64, minutes ...int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bars[subject] == nil {
		s.bars[subject] = map[int64]float64{}
	}
	for _, m := range minutes {
		s.bars[subject][rt0.Add(time.Duration(m)*time.Minute).Unix()] = base + float64(m)
	}
}

func (s *sharedStorage) ReadWindow(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]*storageio.Frame{}
	for _, subject := range req.Subjects {
		frame := &storageio.Frame{SubjectID: subject, Columns: append([]string{"data_time", "series_tag"}, req.Columns...)}
		var times []int64
		for at := range s.bars[subject] {
			times = append(times, at)
		}
		sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
		for _, at := range times {
			t := time.Unix(at, 0).UTC()
			if t.Before(req.Start) || !t.Before(req.End) {
				continue
			}
			row := []any{t, ""}
			for _, column := range req.Columns {
				value := s.bars[subject][at]
				if column == "open" {
					value -= 0.5
				}
				row = append(row, value)
			}
			frame.Rows = append(frame.Rows, row)
		}
		out[subject] = frame
	}
	return out, nil
}

func (s *sharedStorage) ReadDatasetChanges(_ context.Context, _, _ string, cursor *storageio.ChangeCursor, _ bool, _ int) (storageio.Changes, error) {
	head := storageio.ChangeCursor{NodeID: "n1", StoreID: "s1"}
	if cursor != nil {
		head = *cursor
	}
	return storageio.Changes{Next: head}, nil
}

func (s *sharedStorage) EarliestPeriod(_ context.Context, _, _, _ string, subjects, _ []string) (time.Time, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best int64
	for _, subject := range subjects {
		for at := range s.bars[subject] {
			if best == 0 || at < best {
				best = at
			}
		}
	}
	return time.Unix(best, 0).UTC(), best != 0, nil
}

func (*sharedStorage) ListColumns(context.Context, string, string) ([]storageio.ColumnInfo, error) {
	return []storageio.ColumnInfo{{ColumnName: "close", ValueType: storageio.ColumnTypeDouble}, {ColumnName: "open", ValueType: storageio.ColumnTypeDouble}}, nil
}

func (*sharedStorage) GetDataset(context.Context, string, string) (storageio.DatasetInfo, error) {
	return storageio.DatasetInfo{Retention: "forever"}, nil
}

func (*sharedStorage) DatasetColumns(context.Context, string, string) ([]string, error) {
	return []string{"close", "open"}, nil
}

func (s *sharedStorage) WriteRows(_ context.Context, _, datasetID, _ string, rows []storageio.ResultRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range rows {
		key := datasetID + "/" + row.SubjectID + "/" + row.DataTime.Format(time.RFC3339)
		if s.results[key] == nil {
			s.results[key] = map[string]any{}
		}
		for name, value := range row.Fields {
			s.results[key][name] = value
		}
	}
	return nil
}

func (*sharedStorage) ReportComputed(context.Context, storageio.PeriodMarker) error { return nil }
func (*sharedStorage) ComputedExists(context.Context, string, string, string, int64) (bool, error) {
	return false, nil
}

func (s *sharedStorage) value(t *testing.T, dataset, subject string, minute int, column string) float64 {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.results[dataset+"/"+subject+"/"+rt0.Add(time.Duration(minute)*time.Minute).Format(time.RFC3339)][column].(float64)
	require.True(t, ok, "%s %s 第 %d 分钟没有 %s", dataset, subject, minute, column)
	return value
}

// sharedTasks 是共用数据源 dataset_a 的两个任务：obs（observed，范围 A、B、C）与 wide（current，范围 A、B、C、X）。
func sharedTasks() (domain.EngineTask, domain.EngineTask) {
	observed := domain.MustDefinition("obs", "obs", "cross_section_history: observed\noutput: ts_mean(cs_rank(close), 3)\n")
	observed.AllowPartialUniverse = true
	plain := domain.MustDefinition("mean", "mean", "output: ts_mean(close, 3)\n")
	task := func(id string, def domain.FactorDef) domain.EngineTask {
		return domain.EngineTask{Set: domain.FactorTask{TaskID: id, SpaceID: "crypto", SourceDatasetID: "dataset_a", Freq: "1m",
			ResultDatasetID: "result_" + id, Status: domain.TaskStatusEnabled}, Members: domain.AsMembers(def), ResultReady: true}
	}
	return task("ftask_obs", observed), task("ftask_wide", plain)
}

// 共用数据源时 observed 的值与任务执行先后无关：另一个任务正在同步、而本任务范围不含的 X 不进历史截面；
// 已下架、没有任何任务在同步的 GONE 仍在它有数据的历史截面里。实时（两种先后）、补算、回放逐位相同。
func TestObservedSharedSourceIndependentOfOrder(t *testing.T) {
	values := map[string]float64{}
	var replayStore *seriesstore.Store
	var replayData *localdata.Data
	for _, order := range []string{"obs-first", "wide-first"} {
		storage := newSharedStorage()
		for subject, base := range map[string]float64{"A": 100, "B": 300, "C": 500, "X": 400} {
			storage.put(subject, base, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11)
		}
		storage.put("GONE", 200, 0, 1, 2, 3, 4, 5, 6, 7, 8)
		store, err := seriesstore.Open(seriesstore.Options{Dir: t.TempDir(), BlockCacheBytes: 8 << 20, DisableSync: true})
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		now := rt0.Add(9 * time.Minute)
		data := localdata.New(localdata.Config{Store: store, Storage: storage, Admit: memadmit.New(0), Workers: 1, BatchSubjects: 2,
			Now: func() time.Time { return now }})
		runner := pipeline.NewRunner(storage, data, periodclock.Continuous{}, pipeline.Config{ComputeWorkers: 1})
		obs, wide := sharedTasks()
		ctx := context.Background()
		// GONE 先随 obs 的旧全集同步进本地，之后移出全集（已下架）。
		require.NoError(t, data.Prepare(ctx, obs, []string{"A", "B", "C", "GONE"}))
		now = rt0.Add(10 * time.Minute)
		require.NoError(t, data.Prepare(ctx, obs, []string{"A", "B", "C"}))
		require.NoError(t, data.Prepare(ctx, wide, []string{"A", "B", "C", "X"}))
		live := func(set domain.EngineTask, universe []string) {
			period := now.Add(-time.Minute) // 本期是最新已收盘的一根
			plan, err := pipeline.BuildLivePlan(periodclock.Continuous{}, pipeline.LiveInput{Set: set.Set, Members: set.Members,
				PeriodTime: period, Universe: universe, CarryColumns: []string{"close"}})
			require.NoError(t, err)
			outcome, err := runner.Run(ctx, plan)
			require.NoError(t, err)
			require.Equal(t, "complete", outcome.Status)
			data.Drain()
		}
		now = rt0.Add(11 * time.Minute)
		if order == "obs-first" {
			live(obs, []string{"A", "B", "C"})
			live(wide, []string{"A", "B", "C", "X"})
		} else {
			live(wide, []string{"A", "B", "C", "X"})
			live(obs, []string{"A", "B", "C"})
		}
		values[order] = storage.value(t, "result_ftask_obs", "A", 10, "obs")
		// 再跑一期（第 11 分钟），本地有了回放终点之后的一根。
		now = rt0.Add(12 * time.Minute)
		live(wide, []string{"A", "B", "C", "X"})
		live(obs, []string{"A", "B", "C"})

		// 补算同一期。
		recalc := pipeline.Plan{Mode: pipeline.ModeRecalc, Set: obs.Set, Members: obs.Members, TargetStart: rt0.Add(10 * time.Minute),
			TargetEnd: rt0.Add(11 * time.Minute), Expected: []string{"A", "B", "C"}, Available: []string{"A", "B", "C"}, CarryColumns: []string{"close"}}
		_, err = runner.Run(ctx, recalc)
		require.NoError(t, err)
		values[order+"/recalc"] = storage.value(t, "result_ftask_obs", "A", 10, "obs")
		replayStore, replayData = store, data
	}
	// 第 8 分钟的截面有 GONE：A 的名次 1/4；第 9、10 分钟只有 A、B、C：1/3。X 始终不在。
	require.InDelta(t, (0.25+1.0/3+1.0/3)/3, values["obs-first"], 1e-12)
	for key, value := range values {
		require.Equal(t, math.Float64bits(values["obs-first"]), math.Float64bits(value), key)
	}

	// 回放走真实路径：本地存储与数据层的登记。
	obs, _ := sharedTasks()
	runner := pipeline.NewRunner(nil, nil, periodclock.Continuous{}, pipeline.Config{ComputeWorkers: 1})
	m := newReplayManager(ReplayDeps{Sets: replayTasks{set: obs}, Source: sharedReplaySource{}, Columns: &sharedStorage{}, Store: replayStore,
		Synced: replayData.SyncedSubjects, Runner: runner})
	ctx := context.Background()
	_, status, err := m.Open(ctx, ReplayOpenRequest{ReplayID: "shared", TaskID: obs.Set.TaskID,
		From: rt0.Add(10 * time.Minute).Format(time.RFC3339), To: rt0.Add(11 * time.Minute).Format(time.RFC3339)})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	step, _, err := m.Step(ctx, ReplayStepRequest{ReplayID: "shared", Period: rt0.Add(10 * time.Minute).Format(time.RFC3339)})
	require.NoError(t, err)
	require.Equal(t, math.Float64bits(values["obs-first"]), math.Float64bits(step.Subjects["A"].Factors["obs"]["obs"]), "回放")
}

// sharedReplaySource 是回放的源数据集：在册的标的是 obs 任务的范围。
type sharedReplaySource struct{}

func (sharedReplaySource) DatasetColumns(context.Context, string, string) ([]string, error) {
	return []string{"close", "open"}, nil
}

func (sharedReplaySource) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return []string{"A", "B", "C"}, nil
}
