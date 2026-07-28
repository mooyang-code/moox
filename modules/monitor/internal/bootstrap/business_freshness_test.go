package bootstrap

import (
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
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
