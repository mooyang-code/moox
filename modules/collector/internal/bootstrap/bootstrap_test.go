package bootstrap

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

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
