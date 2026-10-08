package bootstrap

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	collectordns "github.com/mooyang-code/moox/modules/collector/internal/dnsresolver"
	"github.com/mooyang-code/moox/modules/collector/internal/health"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestObserveCollectorMaintenanceSpaceRefreshesMetricsAndReportsFailure(t *testing.T) {
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	reg := prometheus.NewRegistry()
	m := marketfetch.NewMetrics(reg)
	now := time.Now().UTC()
	require.NoError(t, observeCollectorMaintenanceSpace(context.Background(), db, m, "crypto", now.Add(-24*time.Hour), now, map[string]int64{"runs": 2}))
	families, err := reg.Gather()
	require.NoError(t, err)
	values := map[string]float64{}
	for _, family := range families {
		for _, metric := range family.Metric {
			if family.GetName() == "moox_collector_maintenance_deleted_rows" {
				for _, label := range metric.Label {
					if label.GetName() == "table" && label.GetValue() == "runs" {
						values["deleted"] = metric.GetGauge().GetValue()
					}
				}
			}
			if family.GetName() == "moox_collector_store_stats_healthy" {
				values["healthy"] = metric.GetGauge().GetValue()
			}
		}
	}
	require.Equal(t, map[string]float64{"deleted": 2, "healthy": 1}, values)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, observeCollectorMaintenanceSpace(ctx, db, m, "crypto", now, now, nil))
}

type inventoryReconcilerStub struct {
	due       bool
	refreshes int
	err       error
}

func (s *inventoryReconcilerStub) Due(time.Time) bool { return s.due }
func (s *inventoryReconcilerStub) Refresh(context.Context) error {
	s.refreshes++
	return s.err
}

type metricsReporterStub struct{ calls int }

func (s *metricsReporterStub) Handle(context.Context) error {
	s.calls++
	return nil
}

func TestCollectorHealthSnapshot(t *testing.T) {
	cfg := Default()
	dbm, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = dbm.Close() })
	state := health.New("collector", "collector", "", "")
	rsp := collectorHealthSnapshot(cfg, dbm, state)(context.Background())

	if rsp.Module != "collector" || !rsp.Ready || rsp.Status != "ok" {
		t.Fatalf("health response = %+v", rsp)
	}
	if _, ok := rsp.Details["storage_rpc_gateway_target"]; ok {
		t.Fatal("Collector 经 gatewayclient 访问 Storage，健康详情不再暴露网关地址")
	}
	dnsDetails, ok := rsp.Details["dns_resolver"].(map[string]any)
	if !ok || dnsDetails["enabled"] != false || dnsDetails["source"] != "local" {
		t.Fatalf("dns details = %#v", rsp.Details["dns_resolver"])
	}
}

func TestMarketFetchInvokeConcurrencyIsStockCNTimerSpecific(t *testing.T) {
	if got := marketFetchInvokeConcurrency("stockcn"); got != 256 {
		t.Fatalf("StockCN timer invoke concurrency = %d, want 256", got)
	}
	for _, spaceID := range []string{"crypto", "other"} {
		if got := marketFetchInvokeConcurrency(spaceID); got != 20 {
			t.Fatalf("%s invoke concurrency = %d, want existing default 20", spaceID, got)
		}
	}
}

func TestMarketFetchMaintenanceLimitsBudgetRetryGenerations(t *testing.T) {
	dispatch, recovery := marketFetchMaintenanceLimits("stockcn", marketFetchInvokeConcurrency("stockcn"), 170)
	if dispatch != 638 || recovery != 850 {
		t.Fatalf("StockCN dispatch/recovery limits = %d/%d, want 638/850 for one full pass per minute", dispatch, recovery)
	}
	dispatch, recovery = marketFetchMaintenanceLimits("crypto", marketFetchInvokeConcurrency("crypto"), 170)
	if dispatch != 20 || recovery != 20 {
		t.Fatalf("crypto dispatch/recovery limits = %d/%d, want invoke concurrency 20", dispatch, recovery)
	}
	dispatch, recovery = marketFetchMaintenanceLimits("stockcn", 96, 0)
	if dispatch != 96 || recovery != 96 {
		t.Fatalf("StockCN fallback limits = %d/%d, want invoke concurrency 96", dispatch, recovery)
	}
	dispatch, recovery = marketFetchMaintenanceLimits("stockcn", 0, 0)
	if dispatch != 1 || recovery != 1 {
		t.Fatalf("StockCN zero-concurrency fallback limits = %d/%d, want minimum one per lane", dispatch, recovery)
	}
}

type dnsStatusStub struct{ status collectordns.Status }

func (s dnsStatusStub) Status() collectordns.Status { return s.status }

func TestCollectorHealthSnapshotIncludesDNSDiagnostics(t *testing.T) {
	cfg := Default()
	cfg.DNSResolver.Enabled = true
	dbm, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = dbm.Close() })
	state := health.New("collector", "collector", "", "")
	when := time.Date(2026, 8, 11, 1, 2, 3, 4e6, time.UTC)
	rsp := collectorHealthSnapshot(cfg, dbm, state, dnsStatusStub{status: collectordns.Status{
		Source: "trade", Hash: "abc", RouteCount: 3, RouteAgeSeconds: 4.5,
		LastRefreshAt: when, LastSuccessAt: when, LastErrorCategory: "trade_rpc",
	}})(context.Background())
	details, ok := rsp.Details["dns_resolver"].(map[string]any)
	if !ok {
		t.Fatalf("dns details missing: %#v", rsp.Details)
	}
	if details["source"] != "trade" || details["hash"] != "abc" || details["route_count"] != 3 {
		t.Fatalf("dns details = %#v", details)
	}
	if details["last_error_category"] != "trade_rpc" || details["last_refresh_at"] != when.Format(time.RFC3339Nano) {
		t.Fatalf("dns timing/error details = %#v", details)
	}
}

func TestMetricsTimerRefreshFailureDoesNotBlockReporter(t *testing.T) {
	inventory := &inventoryReconcilerStub{due: true, err: errors.New("refresh failed")}
	reporter := &metricsReporterStub{}
	handler := metricsTimerHandler(inventory, reporter, time.Now)

	if err := handler(context.Background()); err != nil {
		t.Fatalf("metrics handler: %v", err)
	}
	if inventory.refreshes != 1 || reporter.calls != 1 {
		t.Fatalf("refreshes=%d reporter calls=%d", inventory.refreshes, reporter.calls)
	}
}
