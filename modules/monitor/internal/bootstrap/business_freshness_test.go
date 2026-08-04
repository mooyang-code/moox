package bootstrap

import (
	"fmt"
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

func TestBusinessFreshnessReporterKeepsCollectorExpectationBeforeStorage(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	labels := `{"dataset_id":"bars","freq":"1m","space_id":"crypto"}`
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		for _, metric := range []struct {
			id, name, labels string
			value            float64
		}{
			{"enabled", "moox_collector_dataset_enabled", labels, 1},
			{"interval", "moox_collector_dataset_expected_interval_seconds", labels, 60},
			{"inventory", "moox_collector_dataset_inventory_last_success_timestamp_seconds", `{}`, float64(now.Unix())},
		} {
			if err := db.Create(&monmetrics.MetricSeries{
				ServiceName: "moox_collector", InstanceID: "collector@node-a", SeriesID: metric.id,
				MetricName: metric.name, MetricType: "gauge", LabelsJSON: metric.labels, LastSeenAt: now,
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&monmetrics.MetricLatest{
				SeriesID: metric.id, ServiceName: "moox_collector", InstanceID: "collector@node-a",
				MetricName: metric.name, MetricType: "gauge", LabelsJSON: metric.labels,
				Value: metric.value, ObservedAt: now,
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
		Now: func() time.Time { return now },
	}, repositories, nil)
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	results, err := repositories.Results.Recent(t.Context(), "crypto", "dataset:collector:bars:1m", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Success || results[0].ErrorMessage != "尚未上报" {
		t.Fatalf("results = %+v", results)
	}
}

func TestBusinessFreshnessReporterStoresBalanceCheckInCryptoMarket(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		for _, metric := range []struct {
			id, name string
			value    float64
		}{
			{"balance-success", "moox_trade_balance_sync_last_success_timestamp_seconds", float64(now.Unix())},
			{"balance-run", "moox_trade_balance_sync_last_run_timestamp_seconds", float64(now.Unix())},
			{"balance-failures", "moox_trade_balance_sync_consecutive_failures", 0},
			{"balance-difference", "moox_trade_balance_sync_max_difference_ratio", 0},
		} {
			if err := db.Create(&monmetrics.MetricSeries{
				ServiceName: "moox_trade", InstanceID: "moox_trade@control", SeriesID: metric.id,
				MetricName: metric.name, MetricType: "gauge", LabelsJSON: "{}", LastSeenAt: now,
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&monmetrics.MetricLatest{
				SeriesID: metric.id, ServiceName: "moox_trade", InstanceID: "moox_trade@control",
				MetricName: metric.name, MetricType: "gauge", LabelsJSON: "{}", Value: metric.value, ObservedAt: now,
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
		Now: func() time.Time { return now },
	}, repositories, nil)
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	results, err := repositories.Results.Recent(t.Context(), "crypto_market", "balance:moox_trade", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Success {
		t.Fatalf("results = %+v", results)
	}
}

func TestBusinessFreshnessReporterResolvesReporterForDisabledDeployment(t *testing.T) {
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
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		if err := db.Create(&monmetrics.MetricService{
			ServiceName: "moox_factor", InstanceID: "moox_factor@control", BootID: "old-boot",
			NodeID: "control", LastSeenAt: now.Add(-time.Hour), IsStale: true,
		}).Error; err != nil {
			t.Fatal(err)
		}
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	repositories := manager.Repositories()
	for _, check := range []*domain.Check{
		{
			CheckID: "sysdeploy:control:moox_factor", Name: "moox_factor@control",
			Source: domain.CheckSourceSysDeploy, Enabled: false, IntervalSeconds: 30,
		},
		{
			SpaceID: monmetrics.InternalMetricSpaceID,
			CheckID: "reporter:control:moox_factor:moox_factor@control",
			Name:    "Reporter moox_factor control moox_factor@control",
			Source:  domain.CheckSourceObservability, Kind: domain.CheckKindExternal,
			Enabled: true, IntervalSeconds: 30,
		},
	} {
		if err := repositories.Checks.Create(t.Context(), check); err != nil {
			t.Fatal(err)
		}
	}
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{
		Metrics: query, Checks: repositories.Checks, Results: repositories.Results,
	}, repositories, nil)
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	results, err := repositories.Results.Recent(
		t.Context(),
		monmetrics.InternalMetricSpaceID,
		"reporter:control:moox_factor:moox_factor@control",
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Success || results[0].ErrorMessage != "no_longer_expected" {
		t.Fatalf("results = %+v", results)
	}
}

func TestBusinessFreshnessReporterResolvesDatasetForDisabledProducer(t *testing.T) {
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
	labels := `{"dataset_id":"factor_output","freq":"1m","space_id":"crypto"}`
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		if err := db.Create(&monmetrics.MetricSeries{
			ServiceName: "moox_factor", InstanceID: "moox_factor@control",
			SeriesID: "factor-enabled", MetricName: "moox_factor_dataset_enabled",
			MetricType: "gauge", LabelsJSON: labels, LastSeenAt: now.Add(-time.Hour), IsStale: true,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&monmetrics.MetricLatest{
			SeriesID: "factor-enabled", ServiceName: "moox_factor", InstanceID: "moox_factor@control",
			MetricName: "moox_factor_dataset_enabled", MetricType: "gauge",
			LabelsJSON: labels, Value: 1, ObservedAt: now.Add(-time.Hour),
		}).Error; err != nil {
			t.Fatal(err)
		}
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	repositories := manager.Repositories()
	for _, check := range []*domain.Check{
		{
			CheckID: "sysdeploy:control:moox_factor", Name: "moox_factor@control",
			Source: domain.CheckSourceSysDeploy, Enabled: false, IntervalSeconds: 30,
		},
		{
			SpaceID: "crypto", CheckID: "dataset:factor:factor_output:1m",
			Name: "Dataset factor factor_output 1m", Source: domain.CheckSourceObservability,
			Kind: domain.CheckKindExternal, Enabled: true, IntervalSeconds: 30,
		},
	} {
		if err := repositories.Checks.Create(t.Context(), check); err != nil {
			t.Fatal(err)
		}
	}
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{
		Metrics: query, Checks: repositories.Checks, Results: repositories.Results,
	}, repositories, nil)
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	results, err := repositories.Results.Recent(
		t.Context(), "crypto", "dataset:factor:factor_output:1m", 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Success || results[0].ErrorMessage != "no_longer_expected" {
		t.Fatalf("results = %+v", results)
	}
}

func TestServiceDeploymentExpectedAcceptsConfiguredLimit(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	rows := make([]domain.Check, 1500)
	for i := range rows {
		rows[i] = domain.Check{
			CheckID: "sysdeploy:node-" + fmt.Sprint(i) + ":service-" + fmt.Sprint(i),
			Source:  domain.CheckSourceSysDeploy, Enabled: false, IntervalSeconds: 30,
		}
	}
	if _, err := store.WithDatabase(manager, func(db *gorm.DB) struct{} {
		if err := db.CreateInBatches(rows, 100).Error; err != nil {
			t.Fatal(err)
		}
		return struct{}{}
	}); err != nil {
		t.Fatal(err)
	}
	expected, err := serviceDeploymentExpected(t.Context(), manager.Repositories().Checks, "moox_factor")
	if err != nil || !expected {
		t.Fatalf("expected = %v, err = %v", expected, err)
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
