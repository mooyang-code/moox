package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/stretchr/testify/require"
)

type prewarmCatalogFake struct {
	order []string
	sets  map[string]domain.EngineSet
}

func (f *prewarmCatalogFake) SetIDs() []string { return f.order }

func (f *prewarmCatalogFake) SetByID(id string) (domain.EngineSet, SetLookup) {
	set, ok := f.sets[id]
	if !ok {
		return domain.EngineSet{}, SetGone
	}
	if !set.ResultReady {
		return domain.EngineSet{}, SetNotReady
	}
	return set, SetFound
}

type prewarmSourceFake struct {
	subjects map[string][]string
	err      map[string]error
}

func (f *prewarmSourceFake) DatasetColumns(context.Context, string, string) ([]string, error) {
	return []string{"close"}, nil
}

func (f *prewarmSourceFake) ListDatasetSubjects(_ context.Context, _, datasetID string) ([]string, error) {
	if err := f.err[datasetID]; err != nil {
		return nil, err
	}
	return f.subjects[datasetID], nil
}

type prewarmWindowsFake struct {
	known []string
	plans []pipeline.Plan
}

func (f *prewarmWindowsFake) WarmupStatuses() []pipeline.WarmupStatus {
	out := make([]pipeline.WarmupStatus, 0, len(f.known))
	for _, id := range f.known {
		out = append(out, pipeline.WarmupStatus{SetID: id})
	}
	return out
}

func (f *prewarmWindowsFake) Prewarm(plan pipeline.Plan) int {
	f.plans = append(f.plans, plan)
	return len(plan.Available)
}

func prewarmSet(id, dataset, freq, mode string, ready bool, subjects ...string) domain.EngineSet {
	return domain.EngineSet{
		Set: domain.FactorSet{
			SetID: id, SpaceID: "crypto", SourceDatasetID: dataset, ResultDatasetID: id + "_result", Freq: freq,
			Status: domain.SetStatusEnabled, SubjectMode: mode, Subjects: subjects,
		},
		Factors:     []domain.FactorDef{gapFactor("ma", domain.FactorTypeTimeSeries, false)},
		ResultReady: ready,
	}
}

// 活动引擎为实时缓存里还没有的集合排队预热：窗口与实时周期一致，目标是最近一根已收盘 K 线的前一根；
// 已有记录、结果表未就绪的集合跳过；一个集合失败不影响其他集合；不是活动引擎时什么都不做。
func TestPrewarmerQueuesUnknownSetsOnlyWhenActive(t *testing.T) {
	catalog := &prewarmCatalogFake{
		order: []string{"fset_1h", "fset_known", "fset_include", "fset_pending", "fset_broken"},
		sets: map[string]domain.EngineSet{
			"fset_1h":      prewarmSet("fset_1h", "bars_1h", "1h", domain.SubjectModeAll, true),
			"fset_known":   prewarmSet("fset_known", "bars_1m", "1m", domain.SubjectModeAll, true),
			"fset_include": prewarmSet("fset_include", "bars_1m", "1m", domain.SubjectModeInclude, true, "BTC"),
			"fset_pending": prewarmSet("fset_pending", "bars_1m", "1m", domain.SubjectModeAll, false),
			"fset_broken":  prewarmSet("fset_broken", "bars_broken", "1h", domain.SubjectModeAll, true),
		},
	}
	source := &prewarmSourceFake{
		subjects: map[string][]string{"bars_1h": {"ETH", "BTC"}, "bars_1m": {"BTC", "ETH", "SOL"}},
		err:      map[string]error{"bars_broken": errors.New("storage unavailable")},
	}
	windows := &prewarmWindowsFake{known: []string{"fset_known"}}
	active := false
	p := newPrewarmer(catalog, source, windows, func() bool { return active })
	p.now = func() time.Time { return time.Date(2026, 10, 16, 8, 10, 30, 0, time.UTC) }

	p.once(context.Background())
	require.Empty(t, windows.plans, "租约在别的引擎手里：不预热")

	active = true
	p.once(context.Background())
	require.Len(t, windows.plans, 2)
	hourly := windows.plans[0]
	require.Equal(t, "fset_1h", hourly.Set.SetID)
	require.Equal(t, pipeline.ModeLive, hourly.Mode)
	require.Equal(t, time.Date(2026, 10, 16, 6, 0, 0, 0, time.UTC), hourly.PeriodTime, "最近一根已收盘的是 07:00Z，预热到它的前一根")
	require.Equal(t, time.Date(2026, 10, 16, 7, 0, 0, 0, time.UTC), hourly.TargetEnd)
	require.Equal(t, time.Date(2026, 10, 16, 4, 0, 0, 0, time.UTC), hourly.TargetStart, "回看 3 根")
	require.Equal(t, []string{"BTC", "ETH"}, hourly.Available)
	require.Equal(t, []string{"close"}, hourly.CarryColumns)
	include := windows.plans[1]
	require.Equal(t, "fset_include", include.Set.SetID)
	require.Equal(t, []string{"BTC"}, include.Available, "指定标的的集合用自己的标的")
	require.Equal(t, time.Date(2026, 10, 16, 8, 8, 0, 0, time.UTC), include.PeriodTime)
}

// 预热计划的目标周期与实时周期对齐，任何支持的频率都能通过时钟校验。
func TestPrewarmPeriodAlignsToClock(t *testing.T) {
	now := time.Date(2026, 10, 16, 8, 10, 30, 0, time.UTC)
	for freq, want := range map[string]time.Time{
		"1m": time.Date(2026, 10, 16, 8, 8, 0, 0, time.UTC),
		"1h": time.Date(2026, 10, 16, 6, 0, 0, 0, time.UTC),
		"4h": time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC),
		"1d": time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC),
	} {
		got, err := prewarmPeriod(periodclock.Continuous{}, freq, now)
		require.NoError(t, err, freq)
		require.Equal(t, want, got, freq)
	}
}
