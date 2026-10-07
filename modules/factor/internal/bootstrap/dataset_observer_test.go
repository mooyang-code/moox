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
