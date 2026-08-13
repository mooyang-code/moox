package bootstrap

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	collectordns "github.com/mooyang-code/moox/modules/collector/internal/dnsresolver"
	"github.com/mooyang-code/moox/modules/collector/internal/health"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
)

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
	if rsp.Details["storage_rpc_gateway_target"] != "ip://127.0.0.1:11003" {
		t.Fatalf("storage_rpc_gateway_target = %v", rsp.Details["storage_rpc_gateway_target"])
	}
	dnsDetails, ok := rsp.Details["dns_resolver"].(map[string]any)
	if !ok || dnsDetails["enabled"] != false || dnsDetails["source"] != "local" {
		t.Fatalf("dns details = %#v", rsp.Details["dns_resolver"])
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
