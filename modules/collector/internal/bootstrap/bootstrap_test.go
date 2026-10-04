package bootstrap

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	collectordns "github.com/mooyang-code/moox/modules/collector/internal/dnsresolver"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/health"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/taskresult"
	"github.com/mooyang-code/moox/modules/collector/internal/ruleseed"
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

func TestEnsureTaskResultMetadataPreservesAllowlistedBinanceSharedTarget(t *testing.T) {
	dbm, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = dbm.Close() })
	require.NoError(t, dbm.ApplySchema(schema.AllSQL()))

	tasks, err := ruleseed.LoadFile(filepath.Join("..", "..", "..", "..", "config", "setup", "collection-tasks.yaml"))
	require.NoError(t, err)
	sharedTasks := make([]domain.CollectionTask, 0, 2)
	for _, task := range tasks {
		if task.TaskID == taskresult.BinanceSpotKline1mTaskID || task.TaskID == taskresult.BinanceSwapKline1mTaskID {
			sharedTasks = append(sharedTasks, task)
		}
	}
	require.Len(t, sharedTasks, 2)
	_, err = ruleseed.SeedMissing(context.Background(), dbm.Tasks(), sharedTasks)
	require.NoError(t, err)

	// Shared metadata belongs to setup's catalog, so startup must not call the
	// task-result manager to create or mutate it.
	require.NoError(t, ensureTaskResultMetadata(context.Background(), dbm.Tasks(), &taskresult.Manager{}, "storage-node-0"))
	for _, seeded := range sharedTasks {
		got, err := dbm.Tasks().GetByTaskID(context.Background(), seeded.SpaceID, seeded.TaskID)
		require.NoError(t, err)
		require.Equal(t, taskresult.BinanceKline1mDatasetID, got.ResultDatasetID)
		require.Equal(t, taskresult.BinanceKline1mViewID, got.ResultViewID)
		params, err := domain.ParseCollectParams(got.CollectParams, "", "", got.DataType)
		require.NoError(t, err)
		require.Equal(t, taskresult.BinanceKline1mDatasetID, params.TargetDatasetID)
		require.Equal(t, "binance", params.Provider)
		if got.TaskID == taskresult.BinanceSpotKline1mTaskID {
			require.Equal(t, "spot", params.MarketType)
		} else {
			require.Equal(t, "swap", params.MarketType)
		}
	}

}

func TestBootstrapSharedTaskTargetRequiresReservedProviderAndMarketType(t *testing.T) {
	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: taskresult.BinanceSpotKline1mTaskID, DataType: "kline", TagIDs: []string{"binance_spot"},
		ResultDatasetID: taskresult.BinanceKline1mDatasetID, ResultViewID: taskresult.BinanceKline1mViewID,
	}
	params, err := domain.ParseCollectParams(`{"provider":"binance","market_type":"spot","target_dataset_id":"dataset_binance_kline_1m","frequency":"1m"}`, "", "", "kline")
	require.NoError(t, err)
	ids, shared, err := builtinSharedTaskResultTarget(task, params)
	require.NoError(t, err)
	require.True(t, shared)
	require.Equal(t, taskresult.IDs{DatasetID: taskresult.BinanceKline1mDatasetID, ViewID: taskresult.BinanceKline1mViewID}, ids)

	params.MarketType = "swap"
	_, _, err = builtinSharedTaskResultTarget(task, params)
	require.ErrorContains(t, err, "provider, market_type")
	params.MarketType = "spot"
	params.Provider = "kraken"
	_, _, err = builtinSharedTaskResultTarget(task, params)
	require.ErrorContains(t, err, "provider, market_type")
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
	if rsp.Details["storage_rpc_gateway_target"] != "ip://127.0.0.1:11003" {
		t.Fatalf("storage_rpc_gateway_target = %v", rsp.Details["storage_rpc_gateway_target"])
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
