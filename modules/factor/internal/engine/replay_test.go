package engine

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

type replaySets struct{ set domain.EngineSet }

func (r replaySets) SetByID(id string) (domain.EngineSet, SetLookup) {
	if id == r.set.Set.SetID {
		return r.set, SetFound
	}
	return domain.EngineSet{}, SetGone
}

type replaySource struct{}

func (replaySource) DatasetColumns(context.Context, string, string) ([]string, error) {
	return []string{"close", "open"}, nil
}

func (replaySource) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return []string{"A", "B", "GONE"}, nil
}

type replayColumns struct{}

func (replayColumns) ListColumns(context.Context, string, string) ([]storageio.ColumnInfo, error) {
	return nil, nil
}

var rt0 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func newReplayManagerForTest(t *testing.T) (*replayManager, domain.EngineSet) {
	t.Helper()
	store, err := seriesstore.Open(seriesstore.Options{Dir: t.TempDir(), BlockCacheBytes: 8 << 20, DisableSync: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	fields := []seriesstore.Field{{Name: "close", Type: seriesstore.TypeFloat64}, {Name: "open", Type: seriesstore.TypeFloat64}}
	src := seriesstore.Source{SpaceID: "crypto", DatasetID: "dataset_a", Freq: "1m"}
	write := func(subject string, minutes int) {
		cols := seriesstore.NewColumns(fields, minutes)
		for i := 0; i < minutes; i++ {
			price := 100 + float64(i)
			cols.AppendRow(rt0.Add(time.Duration(i)*time.Minute), "", false, seriesstore.Float(price), seriesstore.Float(price-0.5))
		}
		_, err := store.WriteRange(context.Background(), src, seriesstore.SubjectWrite{
			Subject: subject, From: rt0, To: rt0.Add(time.Duration(minutes) * time.Minute), Rows: cols,
		}, seriesstore.WriteOptions{FillGaps: true})
		require.NoError(t, err)
	}
	write("A", 20)
	write("B", 20)
	write("GONE", 8) // 数据到不了回放终点：被排除
	set := engineSet("fset_replay", "dataset_a", true, "bias")
	set = uncompiled(set)
	runner := pipeline.NewRunner(nil, nil, periodclock.Continuous{}, pipeline.Config{ComputeWorkers: 1})
	return newReplayManager(ReplayDeps{
		Sets: replaySets{set: set}, Source: replaySource{}, Columns: replayColumns{}, Store: store, Runner: runner,
	}), set
}

// 回放逐期取因子帧：预热区间用于回看，终点之后的一根提供下一根开盘价，数据到不了终点的标的被排除。
func TestReplayOpenAndStep(t *testing.T) {
	m, _ := newReplayManagerForTest(t)
	ctx := context.Background()
	open, status, err := m.Open(ctx, ReplayOpenRequest{
		ReplayID: "r1", SetID: "fset_replay", From: rt0.Add(5 * time.Minute).Format(time.RFC3339), To: rt0.Add(10 * time.Minute).Format(time.RFC3339), ExtraPeriods: 1,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, []string{"A", "B"}, open.Subjects)
	require.Len(t, open.Excluded, 1)
	require.Equal(t, "GONE", open.Excluded[0].Subject)
	require.Equal(t, 5, open.Periods)
	require.Len(t, open.Factors, 1)

	step, status, err := m.Step(ctx, ReplayStepRequest{ReplayID: "r1", Period: rt0.Add(5 * time.Minute).Format(time.RFC3339)})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, rt0.Add(6*time.Minute).Format(time.RFC3339), step.DecisionAt)
	a := step.Subjects["A"]
	require.InDelta(t, 105, a.Close, 1e-9)
	require.InDelta(t, 106-0.5, a.Exec, 1e-9, "成交价默认取下一根开盘价")
	require.InDelta(t, 104, a.SourcePrev["close"], 1e-9)
	require.Equal(t, 6, a.AgeBars)
	require.NotEmpty(t, a.Factors["bias"], "本期因子值")
	require.NotEmpty(t, a.FactorsPrev["bias"], "上一期的因子值也要带回")
	require.Equal(t, "complete", step.States["bias"].Status)

	// 同一期重复请求得到同样的值；区间外的周期被拒绝。
	again, _, err := m.Step(ctx, ReplayStepRequest{ReplayID: "r1", Period: rt0.Add(5 * time.Minute).Format(time.RFC3339)})
	require.NoError(t, err)
	require.Equal(t, a.Factors, again.Subjects["A"].Factors)
	_, status, err = m.Step(ctx, ReplayStepRequest{ReplayID: "r1", Period: rt0.Add(10 * time.Minute).Format(time.RFC3339)})
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, status)

	require.True(t, m.closeSession("r1"))
	_, status, err = m.Step(ctx, ReplayStepRequest{ReplayID: "r1", Period: rt0.Add(5 * time.Minute).Format(time.RFC3339)})
	require.Error(t, err)
	require.Equal(t, http.StatusNotFound, status)
}

func TestReplayQuoteCloseAndValidation(t *testing.T) {
	m, _ := newReplayManagerForTest(t)
	ctx := context.Background()
	_, status, err := m.Open(ctx, ReplayOpenRequest{ReplayID: "r2", SetID: "fset_replay", From: rt0.Add(5 * time.Minute).Add(time.Second).Format(time.RFC3339), To: rt0.Add(10 * time.Minute).Format(time.RFC3339)})
	require.Error(t, err, "没有对齐的区间被拒绝")
	require.Equal(t, http.StatusBadRequest, status)
	_, status, err = m.Open(ctx, ReplayOpenRequest{ReplayID: "r2", SetID: "fset_replay", From: rt0.Add(5 * time.Minute).Format(time.RFC3339), To: rt0.Add(30 * time.Minute).Format(time.RFC3339)})
	require.Error(t, err, "本地数据到不了终点")
	require.Equal(t, http.StatusConflict, status)
	_, status, err = m.Open(ctx, ReplayOpenRequest{ReplayID: "r2", SetID: "missing", From: rt0.Format(time.RFC3339), To: rt0.Add(time.Minute).Format(time.RFC3339)})
	require.Error(t, err)
	require.Equal(t, http.StatusNotFound, status)

	_, _, err = m.Open(ctx, ReplayOpenRequest{ReplayID: "r3", SetID: "fset_replay", From: rt0.Add(5 * time.Minute).Format(time.RFC3339), To: rt0.Add(10 * time.Minute).Format(time.RFC3339), Quote: "close"})
	require.NoError(t, err)
	step, _, err := m.Step(ctx, ReplayStepRequest{ReplayID: "r3", Period: rt0.Add(5 * time.Minute).Format(time.RFC3339)})
	require.NoError(t, err)
	require.InDelta(t, 105, step.Subjects["A"].Exec, 1e-9, "close 模式用本期收盘价成交")
}

// 未来才上市的标的不能改变早期周期的截面：本期的全集只含已经上市的标的。
func TestReplayUniverseExcludesNotYetListedSubjects(t *testing.T) {
	m, set := newReplayManagerForTest(t)
	_ = set
	ctx := context.Background()
	// 用一个在第 8 分钟才上市的标的 LATE：数据覆盖到终点。
	store := m.deps.Store
	src := seriesstore.Source{SpaceID: "crypto", DatasetID: "dataset_a", Freq: "1m"}
	fields := []seriesstore.Field{{Name: "close", Type: seriesstore.TypeFloat64}, {Name: "open", Type: seriesstore.TypeFloat64}}
	cols := seriesstore.NewColumns(fields, 12)
	for i := 8; i < 20; i++ {
		cols.AppendRow(rt0.Add(time.Duration(i)*time.Minute), "", false, seriesstore.Float(50), seriesstore.Float(50))
	}
	_, err := store.WriteRange(ctx, src, seriesstore.SubjectWrite{Subject: "LATE", From: rt0.Add(8 * time.Minute), To: rt0.Add(20 * time.Minute), Rows: cols}, seriesstore.WriteOptions{})
	require.NoError(t, err)
	m.deps.Source = lateSource{}
	_, _, err = m.Open(ctx, ReplayOpenRequest{ReplayID: "r9", SetID: "fset_replay", From: rt0.Add(5 * time.Minute).Format(time.RFC3339), To: rt0.Add(10 * time.Minute).Format(time.RFC3339), ExtraPeriods: 1})
	require.NoError(t, err)

	early, _, err := m.Step(ctx, ReplayStepRequest{ReplayID: "r9", Period: rt0.Add(6 * time.Minute).Format(time.RFC3339)})
	require.NoError(t, err)
	require.NotContains(t, early.Subjects, "LATE", "第 6 分钟 LATE 还没上市")
	require.Contains(t, early.Subjects, "A")
	later, _, err := m.Step(ctx, ReplayStepRequest{ReplayID: "r9", Period: rt0.Add(8 * time.Minute).Format(time.RFC3339)})
	require.NoError(t, err)
	require.Contains(t, later.Subjects, "LATE")
}

type lateSource struct{ replaySource }

func (lateSource) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return []string{"A", "B", "LATE"}, nil
}
