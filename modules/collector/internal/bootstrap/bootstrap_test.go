package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/fetchplan"
	"github.com/mooyang-code/moox/modules/collector/internal/health"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestObserveCollectorMaintenanceSpaceRefreshesMetricsAndReportsFailure(t *testing.T) {
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db"), DefaultIngest: markets.DefaultIngest})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	reg := prometheus.NewRegistry()
	m := fetchplan.NewControlMetrics(reg)
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
	dbm, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db"), DefaultIngest: markets.DefaultIngest})
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
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db"), DefaultIngest: markets.DefaultIngest})
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

// 全新库启动：内置任务种子不写采集方式，由任务仓库按市场默认值填上——crypto 是 stream，A 股与港美股是 scf，
// 合成任务为空。任务表的约束（K 线任务必须有采集方式）不会把种子拦下。
func TestFreshDatabaseSeedsBuiltinTasksWithTheMarketDefaultIngest(t *testing.T) {
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db"), DefaultIngest: markets.DefaultIngest})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	ctx := context.Background()
	seed := filepath.Join("..", "..", "..", "..", "config", "setup", "collection-tasks.yaml")
	require.NoError(t, seedBuiltinTasks(ctx, db.Tasks(), seed))

	tasks, total, err := db.Tasks().List(ctx, store.TaskFilter{Page: 1, PageSize: 1000})
	require.NoError(t, err)
	require.EqualValues(t, len(tasks), total)
	seen := map[string]int{}
	for _, task := range tasks {
		want := ""
		if task.DataType == domain.DataTypeKline {
			want = domain.IngestSCF
			if task.SpaceID == "crypto" {
				want = domain.IngestStreaming
			}
			seen[task.SpaceID]++
		}
		require.Equal(t, want, task.Ingest, "%s/%s", task.SpaceID, task.TaskID)
	}
	for _, space := range []string{"crypto", "stockcn", "stockhk", "stockus"} {
		require.Positive(t, seen[space], "种子里应有 %s 的 K 线任务", space)
	}

	// 合成任务没有采集方式，同样写得进去。
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "resample", TaskName: "合成", DataType: "kline_resample", CollectParams: "{}"}))
	resample, err := db.Tasks().GetByTaskID(ctx, "crypto", "resample")
	require.NoError(t, err)
	require.Empty(t, resample.Ingest)
}
