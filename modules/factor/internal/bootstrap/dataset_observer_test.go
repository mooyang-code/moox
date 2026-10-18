package bootstrap

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

type fakeEngineView struct {
	sets []domain.EngineSet
	runs []domain.SetRunSummary
}

func (f *fakeEngineView) Snapshot(context.Context, string) (string, bool, []domain.EngineSet, error) {
	return "hash", false, f.sets, nil
}

func (f *fakeEngineView) Engine(context.Context) (domain.EngineInfo, domain.EngineStatus, bool, error) {
	return domain.EngineInfo{}, domain.EngineStatus{RecentRuns: f.runs}, true, nil
}

func TestFactorDatasetObserverReportsEngineRuns(t *testing.T) {
	registry := prometheus.NewRegistry()
	datasets, err := report.NewDatasetMetrics(registry, "factor")
	require.NoError(t, err)
	set := func(id, dataset string, ready bool) domain.EngineSet {
		return domain.EngineSet{Set: domain.FactorSet{SetID: id, SpaceID: "crypto", ResultDatasetID: dataset, Freq: "1m"}, ResultReady: ready}
	}
	period := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	engine := &fakeEngineView{
		sets: []domain.EngineSet{set("spot", "dataset_factor_spot", true), set("pending", "dataset_factor_pending", false)},
		runs: []domain.SetRunSummary{{SetID: "spot", LastPeriodTime: period.Unix(), LastStatus: "complete"}},
	}
	observer := newFactorDatasetObserver(engine, datasets)
	observer.now = func() time.Time { return period.Add(90 * time.Second) }

	require.NoError(t, observer.observe(context.Background()))
	require.NoError(t, observer.observe(context.Background()), "a repeated heartbeat is not another run")
	requireDatasetValue(t, registry, "moox_factor_dataset_enabled", "dataset_factor_spot", 1)
	_, found := datasetValue(t, registry, "moox_factor_dataset_enabled", "dataset_factor_pending")
	require.False(t, found, "a set whose result is not ready is not expected yet")
	requireDatasetValue(t, registry, "moox_factor_dataset_output_watermark_timestamp_seconds", "dataset_factor_spot", float64(period.Unix()))
	require.Equal(t, 1, testutil.CollectAndCount(registry, "moox_factor_dataset_runs_total"))

	// A warming period is a run, but its results are not formal output.
	engine.runs = []domain.SetRunSummary{{SetID: "spot", LastPeriodTime: period.Add(time.Minute).Unix(), LastStatus: "degraded", WarmingSubjects: 3}}
	require.NoError(t, observer.observe(context.Background()))
	requireDatasetValue(t, registry, "moox_factor_dataset_output_watermark_timestamp_seconds", "dataset_factor_spot", float64(period.Unix()))
	require.Equal(t, 2, testutil.CollectAndCount(registry, "moox_factor_dataset_runs_total"))
}

// 生产现象（2026-10-16）：引擎重启后 1h 那一期全部标的预热中，缺口补算几分钟后补齐，但因子环节一直显示“数据积累中”到下一个整点：
// 同一期的摘要升级为完成后，管理端只认更新的期次、不再记录。现在同一期结果变好时照常记录，产出水位前进，到达时间是补齐的时刻。
func TestFactorDatasetObserverAcceptsSamePeriodUpgradedByRepair(t *testing.T) {
	registry := prometheus.NewRegistry()
	datasets, err := report.NewDatasetMetrics(registry, "factor")
	require.NoError(t, err)
	period := time.Date(2026, 10, 16, 15, 0, 0, 0, time.UTC)
	live := period.Add(time.Hour + 9*time.Second)
	repaired := period.Add(time.Hour + 4*time.Minute)
	engine := &fakeEngineView{
		sets: []domain.EngineSet{{Set: domain.FactorSet{SetID: "spot_1h", SpaceID: "crypto", ResultDatasetID: "dataset_factor_spot_1h", Freq: "1h"}, ResultReady: true}},
		runs: []domain.SetRunSummary{{SetID: "spot_1h", LastPeriodTime: period.Unix(), CompletedAt: live.Unix(), LastStatus: "degraded", WarmingSubjects: 511, FailedSubjects: []string{"A"}}},
	}
	observer := newFactorDatasetObserver(engine, datasets)
	observer.now = func() time.Time { return repaired.Add(time.Second) }
	runs := func() float64 {
		families, err := registry.Gather()
		require.NoError(t, err)
		total := 0.0
		for _, family := range families {
			if family.GetName() == "moox_factor_dataset_runs_total" {
				for _, metric := range family.GetMetric() {
					total += metric.GetCounter().GetValue()
				}
			}
		}
		return total
	}

	require.NoError(t, observer.observe(context.Background()))
	_, found := datasetValue(t, registry, "moox_factor_dataset_output_watermark_timestamp_seconds", "dataset_factor_spot_1h")
	require.False(t, found, "预热中的结果不推进产出水位")
	requireDatasetValue(t, registry, "moox_factor_dataset_last_partial_timestamp_seconds", "dataset_factor_spot_1h", float64(live.Unix()))

	// 补算补齐后同一期升级为完成：记一次成功，产出水位前进，到达时间是补齐的时刻。
	engine.runs = []domain.SetRunSummary{{SetID: "spot_1h", LastPeriodTime: period.Unix(), CompletedAt: repaired.Unix(), LastStatus: "complete"}}
	require.NoError(t, observer.observe(context.Background()))
	requireDatasetValue(t, registry, "moox_factor_dataset_output_watermark_timestamp_seconds", "dataset_factor_spot_1h", float64(period.Unix()))
	requireDatasetValue(t, registry, "moox_factor_dataset_output_arrival_timestamp_seconds", "dataset_factor_spot_1h", float64(repaired.Unix()))
	requireDatasetValue(t, registry, "moox_factor_dataset_last_success_timestamp_seconds", "dataset_factor_spot_1h", float64(repaired.Unix()))
	require.Equal(t, 2.0, runs())

	// 同一期重复的心跳、结果没有变好的不再记录。
	require.NoError(t, observer.observe(context.Background()))
	engine.runs[0].LastStatus, engine.runs[0].WarmingSubjects = "degraded", 3
	require.NoError(t, observer.observe(context.Background()))
	require.Equal(t, 2.0, runs())
}

// 空洞跳过的一期首次观测时已经是正式结果（推进了水位），补齐后升级为成功：水位不再前进，观察器主动补报一次。
func TestFactorDatasetObserverFlushesWhenFormalPeriodTurnsSuccessful(t *testing.T) {
	registry := prometheus.NewRegistry()
	datasets, err := report.NewDatasetMetrics(registry, "factor")
	require.NoError(t, err)
	period := time.Date(2026, 10, 16, 15, 0, 0, 0, time.UTC)
	partial := domain.SetRunSummary{
		SetID: "spot_1h", LastPeriodTime: period.Unix(), CompletedAt: period.Add(time.Hour).Unix(), LastStatus: "degraded", FailedSubjects: []string{"A"},
		Factors: []domain.FactorPeriodState{{FactorID: "f1", Status: "degraded", FailedSubjects: []string{"A"}, GapSubjects: []string{"A"}}},
	}
	engine := &fakeEngineView{
		sets: []domain.EngineSet{{Set: domain.FactorSet{SetID: "spot_1h", SpaceID: "crypto", ResultDatasetID: "dataset_factor_spot_1h", Freq: "1h"}, ResultReady: true}},
		runs: []domain.SetRunSummary{partial},
	}
	observer := newFactorDatasetObserver(engine, datasets)
	var flushed []string
	observer.notify = func(spaceID string) { flushed = append(flushed, spaceID) }

	require.NoError(t, observer.observe(context.Background()))
	requireDatasetValue(t, registry, "moox_factor_dataset_output_watermark_timestamp_seconds", "dataset_factor_spot_1h", float64(period.Unix()))
	require.Empty(t, flushed, "新的一期由水位前进触发补报，观察器不重复触发")

	repaired := period.Add(time.Hour + 20*time.Minute)
	engine.runs = []domain.SetRunSummary{{SetID: "spot_1h", LastPeriodTime: period.Unix(), CompletedAt: repaired.Unix(), LastStatus: "complete"}}
	require.NoError(t, observer.observe(context.Background()))
	require.Equal(t, []string{"crypto"}, flushed)
	requireDatasetValue(t, registry, "moox_factor_dataset_last_success_timestamp_seconds", "dataset_factor_spot_1h", float64(repaired.Unix()))
	requireDatasetValue(t, registry, "moox_factor_dataset_output_arrival_timestamp_seconds", "dataset_factor_spot_1h", float64(period.Add(time.Hour).Unix()))
	require.NoError(t, observer.observe(context.Background()))
	require.Len(t, flushed, 1)
}

// datasetValue returns one dataset's gauge value in a gathered family.
func datasetValue(t *testing.T, registry *prometheus.Registry, name, datasetID string) (float64, bool) {
	t.Helper()
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "dataset_id" && label.GetValue() == datasetID {
					return metric.GetGauge().GetValue(), true
				}
			}
		}
	}
	return 0, false
}

func requireDatasetValue(t *testing.T, registry *prometheus.Registry, name, datasetID string, want float64) {
	t.Helper()
	got, found := datasetValue(t, registry, name, datasetID)
	require.True(t, found, "%s for %s", name, datasetID)
	require.Equal(t, want, got, "%s for %s", name, datasetID)
}

// 失败只来自回看窗口空洞或预热中的运行记为 partial（数据积累中，Monitor 不告警）；
// 有空洞与预热之外的失败是意外的不完整，仍记 incomplete；失败与完整照旧。
func TestDatasetResultClassifiesRunsThatAreIncompleteOnlyBecauseDataIsAccumulating(t *testing.T) {
	gapOnly := domain.SetRunSummary{
		LastStatus: "degraded", FailedSubjects: []string{"A", "B"},
		Factors: []domain.FactorPeriodState{{FactorID: "f1", FailedSubjects: []string{"A", "B"}, GapSubjects: []string{"A", "B"}}},
	}
	require.Equal(t, "partial", datasetResult(gapOnly))

	warmingOnly := domain.SetRunSummary{LastStatus: "degraded", WarmingSubjects: 507, FailedSubjects: []string{"A", "B", "C"}}
	require.Equal(t, "partial", datasetResult(warmingOnly), "预热中的标的也列在失败标的里")

	mixed := domain.SetRunSummary{
		LastStatus: "degraded", WarmingSubjects: 1, FailedSubjects: []string{"A", "B"},
		Factors: []domain.FactorPeriodState{{FactorID: "f1", FailedSubjects: []string{"A"}, GapSubjects: []string{"A"}}},
	}
	require.Equal(t, "partial", datasetResult(mixed), "A 是空洞，B 是预热中的标的（预热标的也列在失败标的里）")
	mixed.FailedSubjects = []string{"A", "B", "C"}
	require.Equal(t, "incomplete", datasetResult(mixed), "C 既不是空洞也不是预热：意外的失败")

	realFailure := domain.SetRunSummary{LastStatus: "degraded", FailedSubjects: []string{"A"}, Factors: []domain.FactorPeriodState{{FactorID: "f1", FailedSubjects: []string{"A"}}}}
	require.Equal(t, "incomplete", datasetResult(realFailure), "没有空洞也没有预热的降级是意外的")

	require.Equal(t, "error", datasetResult(domain.SetRunSummary{LastStatus: "failed", WarmingSubjects: 3}))
	require.Equal(t, "success", datasetResult(domain.SetRunSummary{LastStatus: "complete"}))
}
