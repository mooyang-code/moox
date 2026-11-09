package engine

import (
	"context"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/serieswindow"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

// localStoreData 按本地存储读数据，模拟实时计算（追赶读磁盘）的路径：HistorySubjects 给定，Load 读本期可用与历史标的。
type localStoreData struct {
	store   *seriesstore.Store
	src     seriesstore.Source
	history []string
}

func (d localStoreData) HistorySubjects(context.Context, pipeline.Plan) ([]string, error) {
	return d.history, nil
}

func (d localStoreData) Load(ctx context.Context, plan pipeline.Plan) (pipeline.DataResult, error) {
	from, err := pipeline.ReadStart(periodclock.Continuous{}, plan)
	if err != nil {
		return pipeline.DataResult{}, err
	}
	cols, err := d.store.ReadRange(ctx, d.src, append(append([]string(nil), plan.Available...), plan.HistorySubjects...), from, plan.TargetEnd)
	if err != nil {
		return pipeline.DataResult{}, err
	}
	view, err := serieswindow.FromColumns(d.src, 60, []string{"close", "open"}, cols)
	if err != nil {
		return pipeline.DataResult{}, err
	}
	return pipeline.DataResult{View: view, Frames: map[string]*storageio.Frame{}}, nil
}

func (localStoreData) WindowStatuses() []pipeline.WarmupStatus { return nil }

type discardStore struct{ storageio.Store }

func (discardStore) WriteRows(context.Context, string, string, string, []storageio.ResultRow) error { return nil }
func (discardStore) ReportComputed(context.Context, storageio.PeriodMarker) error               { return nil }

// 回放支持 observed：数据到不了回放终点（已下架）的 GONE 被排除在回放标的之外，但仍出现在它还有数据的历史截面里；
// 每期的值与按本地存储走实时计算路径的结果逐位相同。
func TestReplayObservedHistoryIncludesDelisted(t *testing.T) {
	def := domain.MustDefinition("obs", "obs", "cross_section_history: observed\noutput: ts_mean(cs_rank(close), 3)\n")
	def.AllowPartialUniverse = true
	set := uncompiled(domain.EngineTask{
		Set: domain.FactorTask{TaskID: "ftask_replay", SpaceID: "crypto", SourceDatasetID: "dataset_a", Freq: "1m",
			ResultDatasetID: "result_dataset_a", Status: domain.TaskStatusEnabled},
		Members: domain.AsMembers(def), ResultReady: true,
	})
	m, _ := newReplayManagerWithTask(t, set)
	store := m.deps.Store
	src := seriesstore.Source{SpaceID: "crypto", DatasetID: "dataset_a", Freq: "1m"}
	// 改写收盘价：A、GONE、B 的基数为 100、200、300（GONE 只有第 0～7 分钟）。
	fields := []seriesstore.Field{{Name: "close", Type: seriesstore.TypeFloat64}, {Name: "open", Type: seriesstore.TypeFloat64}}
	rewrite := func(subject string, base float64, minutes int) {
		cols := seriesstore.NewColumns(fields, minutes)
		for i := 0; i < minutes; i++ {
			cols.AppendRow(rt0.Add(time.Duration(i)*time.Minute), "", false, seriesstore.Float(base+float64(i)), seriesstore.Float(base+float64(i)-0.5))
		}
		_, err := store.WriteRange(context.Background(), src, seriesstore.SubjectWrite{
			Subject: subject, From: rt0, To: rt0.Add(time.Duration(minutes) * time.Minute), Rows: cols}, seriesstore.WriteOptions{FillGaps: true})
		require.NoError(t, err)
	}
	rewrite("A", 100, 20)
	rewrite("B", 300, 20)
	rewrite("GONE", 200, 8)

	ctx := context.Background()
	open, status, err := m.Open(ctx, ReplayOpenRequest{ReplayID: "obs", TaskID: "ftask_replay",
		From: rt0.Add(5 * time.Minute).Format(time.RFC3339), To: rt0.Add(10 * time.Minute).Format(time.RFC3339)})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, []string{"A", "B"}, open.Subjects)

	live := pipeline.NewRunner(discardStore{}, localStoreData{store: store, src: src, history: []string{"GONE"}}, periodclock.Continuous{},
		pipeline.Config{ComputeWorkers: 1})
	want := map[int]float64{7: 1.0 / 3, 9: (1.0/3 + 0.5 + 0.5) / 3} // A 的值：第 7 分钟的截面有 GONE，第 8、9 分钟没有
	for minute := 5; minute < 10; minute++ {
		period := rt0.Add(time.Duration(minute) * time.Minute)
		step, _, err := m.Step(ctx, ReplayStepRequest{ReplayID: "obs", Period: period.Format(time.RFC3339)})
		require.NoError(t, err)
		got := step.Subjects["A"].Factors["obs"]["obs"]
		if expected, ok := want[minute]; ok {
			require.InDelta(t, expected, got, 1e-12, "minute %d", minute)
		}
		plan, err := pipeline.BuildLivePlan(periodclock.Continuous{}, pipeline.LiveInput{Set: set.Set, Members: set.Members, PeriodTime: period,
			Universe: []string{"A", "B"}})
		require.NoError(t, err)
		plan.HistorySubjects, err = live.HistorySubjects(ctx, plan)
		require.NoError(t, err)
		loaded, err := live.Load(ctx, plan)
		require.NoError(t, err)
		computation, err := live.Compute(ctx, plan, loaded)
		require.NoError(t, err)
		values, err := pipeline.PeriodValues(plan, computation, period)
		require.NoError(t, err)
		liveValue := values["A"]["obs"]["obs"]
		require.Equal(t, math.Float64bits(liveValue), math.Float64bits(got), "minute %d: replay %v, live %v", minute, got, liveValue)
	}
}
