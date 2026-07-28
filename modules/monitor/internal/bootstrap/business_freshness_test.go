package bootstrap

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/report"
	"gorm.io/gorm"
)

func TestBusinessFreshnessReporterResolvesDatasetNoLongerExpected(t *testing.T) {
	t.Setenv("MOOX_MSGBOX_WECOM_WEBHOOK", "")
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	repositories := manager.Repositories()
	check := &domain.Check{
		SpaceID: "crypto", CheckID: "dataset:collector:market_kline:1m",
		Name: "old dataset", GroupName: "business", Kind: domain.CheckKindExternal,
		Source: domain.CheckSourceObservability, Enabled: true, IntervalSeconds: 30,
	}
	if err := repositories.Checks.Create(t.Context(), check); err != nil {
		t.Fatal(err)
	}
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{
		Checks: repositories.Checks, Results: repositories.Results,
	}, repositories, nil)
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	results, err := repositories.Results.Recent(t.Context(), "crypto", check.CheckID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Success || results[0].ErrorMessage != "no_longer_expected" {
		t.Fatalf("results = %+v", results)
	}
}

func TestBusinessFreshnessReporterAlertsOncePerStaleReporterAndSuppressesDatasets(t *testing.T) {
	t.Setenv("MOOX_MSGBOX_WECOM_WEBHOOK", "")
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	staleAt := now.Add(-10 * time.Minute)
	labels := `{"dataset_id":"market_kline","freq":"1m","space_id":"crypto"}`
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		if err := db.Create(&monmetrics.MetricService{
			ServiceName: "moox_collector", InstanceID: "collector@node-a", BootID: "boot-a",
			NodeID: "node-a", LastSeenAt: staleAt,
		}).Error; err != nil {
			t.Fatal(err)
		}
		for _, metric := range []struct {
			id, name, labels string
			value            float64
		}{
			{"enabled", "moox_collector_dataset_enabled", labels, 1},
			{"interval", "moox_collector_dataset_expected_interval_seconds", labels, 60},
			{"inventory", "moox_collector_dataset_inventory_last_success_timestamp_seconds", `{}`, float64(now.Unix())},
			{"run", "moox_collector_dataset_last_run_timestamp_seconds", labels, float64(now.Unix())},
			{"success", "moox_collector_dataset_last_success_timestamp_seconds", labels, float64(now.Unix())},
			{"output", "moox_collector_dataset_output_watermark_timestamp_seconds", labels, float64(now.Unix())},
		} {
			if err := db.Create(&monmetrics.MetricSeries{
				ServiceName: "moox_collector", InstanceID: "collector@node-a", SeriesID: metric.id,
				MetricName: metric.name, MetricType: "gauge", LabelsJSON: metric.labels, LastSeenAt: staleAt,
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&monmetrics.MetricLatest{
				SeriesID: metric.id, ServiceName: "moox_collector", InstanceID: "collector@node-a",
				MetricName: metric.name, MetricType: "gauge", LabelsJSON: metric.labels,
				Value: metric.value, ObservedAt: staleAt,
			}).Error; err != nil {
				t.Fatal(err)
			}
		}
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	repositories := manager.Repositories()
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{
		Metrics: query, Checks: repositories.Checks, Results: repositories.Results,
		Policy: report.RealtimeTimeSeriesPolicy{Defaults: report.RealtimeTimeSeriesDefaults{
			RunMissedIntervals: 2, SuccessMissedIntervals: 3,
			WatermarkPeriods: 3, MinimumWatermarkLag: 10 * time.Minute,
		}},
	}, repositories, nil)
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	enabled := true
	checks, err := repositories.Checks.List(t.Context(), store.ListChecksOptions{
		Source: domain.CheckSourceObservability, Enabled: &enabled,
		Page: store.Page{Page: 1, PageSize: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	reporterCheckID := "reporter:node-a:moox_collector:collector@node-a"
	if len(checks) != 1 || checks[0].CheckID != reporterCheckID {
		t.Fatalf("checks = %+v", checks)
	}
	results, err := repositories.Results.Recent(t.Context(), monmetrics.InternalMetricSpaceID, reporterCheckID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Success || results[0].ErrorMessage != "producer stale" {
		t.Fatalf("results = %+v", results)
	}

	if _, err := store.WithDatabase(manager, func(db *gorm.DB) struct{} {
		if err := db.Model(&monmetrics.MetricService{}).
			Where("c_service_name = ? AND c_instance_id = ?", "moox_collector", "collector@node-a").
			Updates(map[string]any{"c_last_seen_at": now, "c_is_stale": false}).Error; err != nil {
			t.Fatal(err)
		}
		return struct{}{}
	}); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	results, err = repositories.Results.Recent(t.Context(), monmetrics.InternalMetricSpaceID, reporterCheckID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Success || results[0].ErrorMessage != "reporter fresh" {
		t.Fatalf("recovery results = %+v", results)
	}
}
