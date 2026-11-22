package bootstrap

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/prometheus/client_golang/prometheus"
)

const hkStockJSON = `{"market":{"select":"spot","prefer":"spot"},"tasks":{"spot":{"view_id":"view_factor_a","factor_task_id":"ftask_a","dataset_id":"ds"}},"bar":"5m","calendar":"hk_stock","columns":{},"view_columns":["close"]}`

var restoreHK = time.FixedZone("HKT", 8*3600)

// commitHKResult 在实例 i1 的会话 i1-session 里写入一根港股 5m 的处理记录，写入时间是收线后 written。
func commitHKResult(t *testing.T, repo *store.Store, id string, barEnd time.Time, status, reason string, written time.Duration) {
	t.Helper()
	record, _ := json.Marshal(store.InputRecord{Views: map[string]store.ViewInput{"view_a": {}}, Bar: "5m", Calendar: "hk_stock", BarStart: barEnd.Add(-5 * time.Minute)})
	result := store.Result{ResultID: id, InstanceID: "i1", SessionID: "i1-session", BarEndTime: barEnd, ValidUntil: barEnd.Add(time.Hour), Status: status, SkipReason: reason, DSLHash: "sha256:demo",
		InputJSON: record, TargetsJSON: json.RawMessage(`[]`), SummaryJSON: json.RawMessage(`{}`), PublishStatus: store.PublishNone, CreatedAt: barEnd.Add(written)}
	if _, _, err := repo.CommitResult(context.Background(), store.CommitRequest{Result: result, Now: barEnd.Add(written)}); err != nil {
		t.Fatal(err)
	}
}

// datasetGauge 读取观察器注册表里某个数据集指标的值（每个测试只有一个实例）。
func datasetGauge(t *testing.T, registry *prometheus.Registry, name string) (float64, bool) {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() == name && len(family.GetMetric()) > 0 {
			return family.GetMetric()[0].GetGauge().GetValue(), true
		}
	}
	return 0, false
}

func expectGauge(t *testing.T, registry *prometheus.Registry, name string, want time.Time) {
	t.Helper()
	if got, ok := datasetGauge(t, registry, name); !ok || int64(got) != want.Unix() {
		t.Fatalf("%s 应是 %s：%v ok=%v", name, want.Format(time.RFC3339), got, ok)
	}
}

// 周五收市后、周末重启：新进程没有任何周期可跑，补报库里最近一个 ok（记成功，带输出水位）与其后最近处理的一根（记非成功运行），
// Monitor 重新拿到上次的运行、成功时间与水位，不会一直“尚未上报”。
func TestRestartRestoresLastObservations(t *testing.T) {
	repo := openStore(t)
	seedEnabled(t, repo, "i1", nil, hkStockJSON)
	okEnd := time.Date(2026, 11, 20, 15, 55, 0, 0, restoreHK)
	lastEnd := time.Date(2026, 11, 20, 16, 0, 0, 0, restoreHK)
	commitHKResult(t, repo, "r1", okEnd, store.StatusOK, "", 5*time.Second)
	commitHKResult(t, repo, "r2", lastEnd, store.StatusSkipped, store.SkipMarketMissing, 7*time.Second)

	registry := prometheus.NewRegistry()
	observer, err := newInstanceObserver(repo, registry, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	observer.now = func() time.Time { return time.Date(2026, 11, 22, 11, 57, 0, 0, restoreHK) }
	if err := observer.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	expectGauge(t, registry, "moox_strategy_dataset_last_success_timestamp_seconds", okEnd.Add(5*time.Second))
	expectGauge(t, registry, "moox_strategy_dataset_last_run_timestamp_seconds", lastEnd.Add(7*time.Second))
	expectGauge(t, registry, "moox_strategy_dataset_output_watermark_timestamp_seconds", okEnd.Add(-5*time.Minute))
	expectGauge(t, registry, "moox_strategy_dataset_input_watermark_timestamp_seconds", lastEnd.Add(-5*time.Minute))
	if _, ok := datasetGauge(t, registry, "moox_strategy_dataset_last_partial_timestamp_seconds"); ok {
		t.Fatal("market_missing 不是积累类原因，不应记 partial")
	}
}

// 重启后积压事件先于第一次刷新到达：本进程已经按当前时间上报过，补报库里的记录会把最近运行、最近成功往回拨，所以不补。
func TestRestoreDoesNotRollBackBacklogObservation(t *testing.T) {
	repo := openStore(t)
	seedEnabled(t, repo, "i1", nil, hkStockJSON)
	friday := time.Date(2026, 11, 20, 16, 0, 0, 0, restoreHK)
	monday := time.Date(2026, 11, 23, 9, 35, 0, 0, restoreHK)
	commitHKResult(t, repo, "r1", friday, store.StatusOK, "", 5*time.Second)
	commitHKResult(t, repo, "r2", monday, store.StatusOK, "", 5*time.Second)

	registry := prometheus.NewRegistry()
	observer, err := newInstanceObserver(repo, registry, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	observed := monday.Add(time.Minute)
	observer.now = func() time.Time { return observed }
	instance, err := repo.GetInstance(context.Background(), "i1")
	if err != nil {
		t.Fatal(err)
	}
	observer.ObservePeriod(context.Background(), instance, "5m", monday.Add(-5*time.Minute), monday, store.StatusOK, "")
	if err := observer.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	expectGauge(t, registry, "moox_strategy_dataset_last_run_timestamp_seconds", observed)
	expectGauge(t, registry, "moox_strategy_dataset_last_success_timestamp_seconds", observed)
	expectGauge(t, registry, "moox_strategy_dataset_output_watermark_timestamp_seconds", monday.Add(-5*time.Minute))
}

// 模块暂停中（Monitor 不收暂停中的运行）不补报，留到放行后的刷新。
func TestRestoreWaitsWhileModuleHalted(t *testing.T) {
	repo := openStore(t)
	seedEnabled(t, repo, "i1", nil, hkStockJSON)
	friday := time.Date(2026, 11, 20, 16, 0, 0, 0, restoreHK)
	commitHKResult(t, repo, "r1", friday, store.StatusOK, "", 5*time.Second)

	registry := prometheus.NewRegistry()
	observer, err := newInstanceObserver(repo, registry, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	observer.now = func() time.Time { return time.Date(2026, 11, 22, 11, 57, 0, 0, restoreHK) }
	halted := true
	observer.SetHalted(func() bool { return halted })
	if err := observer.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, ok := datasetGauge(t, registry, "moox_strategy_dataset_last_run_timestamp_seconds"); ok {
		t.Fatalf("暂停中不应补报：%v", got)
	}
	if _, ok := observer.restored["i1"]; ok {
		t.Fatal("暂停中不应记为已恢复")
	}
	halted = false
	if err := observer.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	expectGauge(t, registry, "moox_strategy_dataset_last_run_timestamp_seconds", friday.Add(5*time.Second))
	expectGauge(t, registry, "moox_strategy_dataset_last_success_timestamp_seconds", friday.Add(5*time.Second))
}
