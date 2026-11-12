package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	collectordns "github.com/mooyang-code/moox/modules/collector/internal/dnsresolver"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/health"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/marketwiring"
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
	m := marketfetch.NewMetrics(reg, marketwiring.FeedLabels())
	now := time.Now().UTC()
	require.NoError(t, observeCollectorMaintenanceSpace(context.Background(), db, m, "crypto", now, map[string]int64{"backfill_notices": 2}))
	families, err := reg.Gather()
	require.NoError(t, err)
	values := map[string]float64{}
	for _, family := range families {
		for _, metric := range family.Metric {
			if family.GetName() == "moox_collector_maintenance_deleted_rows" {
				for _, label := range metric.Label {
					if label.GetName() == "table" && label.GetValue() == "backfill_notices" {
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
	require.Error(t, observeCollectorMaintenanceSpace(ctx, db, m, "crypto", now, nil))
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
	if rsp.Details["gateway_caller"] != "collector" {
		t.Fatalf("gateway_caller = %v", rsp.Details["gateway_caller"])
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
	cfg.EgressProxy.DNS.Domains = []string{"api.binance.com"}
	dbm, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = dbm.Close() })
	state := health.New("collector", "collector", "", "")
	when := time.Date(2026, 8, 11, 1, 2, 3, 4e6, time.UTC)
	rsp := collectorHealthSnapshot(cfg, dbm, state, dnsStatusStub{status: collectordns.Status{
		Source: "egress", Hash: "abc", RouteCount: 3, RouteAgeSeconds: 4.5,
		LastRefreshAt: when, LastSuccessAt: when, LastErrorCategory: "egress_rpc",
	}})(context.Background())
	details, ok := rsp.Details["dns_resolver"].(map[string]any)
	if !ok {
		t.Fatalf("dns details missing: %#v", rsp.Details)
	}
	if details["source"] != "egress" || details["hash"] != "abc" || details["route_count"] != 3 {
		t.Fatalf("dns details = %#v", details)
	}
	if details["last_error_category"] != "egress_rpc" || details["last_refresh_at"] != when.Format(time.RFC3339Nano) {
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

// 任务表非空时不读内置任务文件：文件格式比程序新（回滚到旧版二进制）也不挡住启动。
func TestSeedBuiltinTasksSkipsTheSeedFileWhenTasksExist(t *testing.T) {
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	broken := filepath.Join(t.TempDir(), "collection-tasks.yaml")
	require.NoError(t, os.WriteFile(broken, []byte("tasks:\n  - task_id: r1\n    mystery: true\n"), 0o644))

	// 任务表为空：照常解析，格式不对就报错。
	require.Error(t, seedBuiltinTasks(context.Background(), db.Tasks(), broken))

	require.NoError(t, db.Tasks().Create(context.Background(), domain.CollectionTask{
		SpaceID: "crypto", TaskID: "t1", TaskName: "任务", DataType: "kline", TagIDs: []string{"binance_spot"}, CollectParams: `{"frequency":"1m"}`,
	}))
	require.NoError(t, seedBuiltinTasks(context.Background(), db.Tasks(), broken))
}
