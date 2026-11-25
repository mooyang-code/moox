package bootstrap

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/prometheus/client_golang/prometheus"
)

const hkStockJSON = `{"market":{"select":"spot","prefer":"spot"},"tasks":{"spot":{"dataset_id":"dataset_factor_a","factor_task_id":"ftask_a","dataset_id":"ds"}},"bar":"5m","calendar":"hk_stock","columns":{},"view_columns":["close"]}`

var restoreHK = time.FixedZone("HKT", 8*3600)

// commitHKResult 在实例 i1 的会话 i1-session 里写入一根港股 5m 的处理记录，写入时间是收线后 written。
func commitHKResult(t *testing.T, repo *store.Store, id string, barEnd time.Time, status, reason string, written time.Duration) {
	t.Helper()
	commitSessionResult(t, repo, id, "i1-session", barEnd, status, reason, written)
}

func commitSessionResult(t *testing.T, repo *store.Store, id, session string, barEnd time.Time, status, reason string, written time.Duration) {
	t.Helper()
	record, _ := json.Marshal(store.InputRecord{Views: map[string]store.ViewInput{"view_a": {}}, Bar: "5m", Calendar: "hk_stock", BarStart: barEnd.Add(-5 * time.Minute)})
	result := store.Result{ResultID: id, InstanceID: "i1", SessionID: session, BarEndTime: barEnd, ValidUntil: barEnd.Add(time.Hour), Status: status, SkipReason: reason, DSLHash: "sha256:demo",
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

// 重启后积压事件先于第一次刷新到达：ObservePeriod 在结果写入之后调用，库里最近一条就是它，补报的时间是它的写入时间，
// 与进程内上报的时间只差处理耗时（这里两者相同），不会退回周五。
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
	observed := monday.Add(5 * time.Second)
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

// 交易时段重启，第一条积压事件是跳过、先于第一次刷新到达：仍补报库里最近一个 ok，最近成功与输出水位不缺。
func TestRestoreAfterBacklogSkipArrivedFirst(t *testing.T) {
	repo := openStore(t)
	seedEnabled(t, repo, "i1", nil, hkStockJSON)
	friday := time.Date(2026, 11, 20, 16, 0, 0, 0, restoreHK)
	monday := time.Date(2026, 11, 23, 9, 35, 0, 0, restoreHK)
	commitHKResult(t, repo, "r1", friday, store.StatusOK, "", 5*time.Second)
	commitHKResult(t, repo, "r2", monday, store.StatusSkipped, store.SkipMarketMissing, 5*time.Second)

	registry := prometheus.NewRegistry()
	observer, err := newInstanceObserver(repo, registry, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	observer.now = func() time.Time { return monday.Add(5 * time.Second) }
	instance, err := repo.GetInstance(context.Background(), "i1")
	if err != nil {
		t.Fatal(err)
	}
	observer.ObservePeriod(context.Background(), instance, "5m", monday.Add(-5*time.Minute), monday, store.StatusSkipped, store.SkipMarketMissing)
	if err := observer.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	expectGauge(t, registry, "moox_strategy_dataset_last_success_timestamp_seconds", friday.Add(5*time.Second))
	expectGauge(t, registry, "moox_strategy_dataset_output_watermark_timestamp_seconds", friday.Add(-5*time.Minute))
	expectGauge(t, registry, "moox_strategy_dataset_last_run_timestamp_seconds", monday.Add(5*time.Second))
}

// 重启时模块在暂停，恢复追赶期间处理了周五最后一根（Monitor 不收暂停中的运行）：放行后的刷新照样补报。
func TestRestoreAfterCatchupRelease(t *testing.T) {
	repo := openStore(t)
	seedEnabled(t, repo, "i1", nil, hkStockJSON)
	thursday := time.Date(2026, 11, 19, 16, 0, 0, 0, restoreHK)
	friday := time.Date(2026, 11, 20, 16, 0, 0, 0, restoreHK)
	commitHKResult(t, repo, "r1", thursday, store.StatusOK, "", 5*time.Second)

	registry := prometheus.NewRegistry()
	observer, err := newInstanceObserver(repo, registry, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	observer.now = func() time.Time { return time.Date(2026, 11, 21, 10, 0, 0, 0, restoreHK) }
	halted := true
	observer.SetHalted(func() bool { return halted })
	if err := observer.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	commitHKResult(t, repo, "r2", friday, store.StatusSkipped, store.SkipCatchup, 7*time.Second)
	instance, err := repo.GetInstance(context.Background(), "i1")
	if err != nil {
		t.Fatal(err)
	}
	observer.ObservePeriod(context.Background(), instance, "5m", friday.Add(-5*time.Minute), friday, store.StatusSkipped, store.SkipCatchup)
	halted = false
	if err := observer.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	expectGauge(t, registry, "moox_strategy_dataset_last_success_timestamp_seconds", thursday.Add(5*time.Second))
	expectGauge(t, registry, "moox_strategy_dataset_output_watermark_timestamp_seconds", thursday.Add(-5*time.Minute))
	expectGauge(t, registry, "moox_strategy_dataset_last_run_timestamp_seconds", friday.Add(7*time.Second))
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

// restoreGauges 是重启前后要一致的四项：最近运行、最近成功、最近部分成功、输出水位（没有的项记为 0）。
var restoreGauges = []string{
	"moox_strategy_dataset_last_run_timestamp_seconds",
	"moox_strategy_dataset_last_success_timestamp_seconds",
	"moox_strategy_dataset_last_partial_timestamp_seconds",
	"moox_strategy_dataset_output_watermark_timestamp_seconds",
}

func snapshotGauges(t *testing.T, registry *prometheus.Registry) map[string]float64 {
	t.Helper()
	out := make(map[string]float64, len(restoreGauges))
	for _, name := range restoreGauges {
		out[name], _ = datasetGauge(t, registry, name)
	}
	return out
}

// runWarmingSession 模拟周五 15:02 以会话 session 启用后的进程：登记时记首期宽限，之后每根都是预热期的 too_many_missing；
// 再模拟周日重启，返回重启前后的四项指标。
func runWarmingSession(t *testing.T, repo *store.Store, session string) (before, after map[string]float64) {
	t.Helper()
	ctx := context.Background()
	enabledAt := time.Date(2026, 11, 20, 15, 2, 0, 0, restoreHK)
	pre := prometheus.NewRegistry()
	observer, err := newInstanceObserver(repo, pre, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	observer.now = func() time.Time { return enabledAt.Add(time.Minute) }
	if err := observer.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	instance, err := repo.GetInstance(ctx, "i1")
	if err != nil {
		t.Fatal(err)
	}
	for bar := time.Date(2026, 11, 20, 15, 5, 0, 0, restoreHK); !bar.After(time.Date(2026, 11, 20, 16, 0, 0, 0, restoreHK)); bar = bar.Add(5 * time.Minute) {
		commitSessionResult(t, repo, "r"+bar.Format("1504"), session, bar, store.StatusSkipped, "too_many_missing", 5*time.Second)
		at := bar.Add(5 * time.Second)
		observer.now = func() time.Time { return at }
		observer.ObservePeriod(ctx, instance, "5m", bar.Add(-5*time.Minute), bar, store.StatusSkipped, "too_many_missing")
	}
	before = snapshotGauges(t, pre)
	post := prometheus.NewRegistry()
	restarted, err := newInstanceObserver(repo, post, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	restarted.now = func() time.Time { return time.Date(2026, 11, 22, 12, 0, 0, 0, restoreHK) }
	if err := restarted.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	return before, snapshotGauges(t, post)
}

func expectSameGauges(t *testing.T, before, after map[string]float64) {
	t.Helper()
	for _, name := range restoreGauges {
		if before[name] != after[name] {
			t.Errorf("%s 重启前 %v，重启后 %v", name, before[name], after[name])
		}
	}
}

// 周五下午新启用的实例，本会话只有预热期的 too_many_missing、还没有 ok：重启后补报的四项与重启前一致（最近成功是启用时刻的空运行，
// 最近部分成功是最后一根），不会变成“尚无成功运行”。
func TestRestoreNewInstanceStillWarming(t *testing.T) {
	repo := openStore(t)
	ctx := context.Background()
	if err := repo.CreateDefinition(ctx, store.Definition{StrategyID: "s1", Name: "demo", DSLYaml: "name: demo", DSLHash: "sha256:demo", CreatedAt: seedTime}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateInstance(ctx, store.Instance{InstanceID: "i1", StrategyID: "s1", SpaceID: "crypto", FactorTasks: map[string]string{"spot": "ftask_a"}, CreatedAt: seedTime}); err != nil {
		t.Fatal(err)
	}
	enabledAt := time.Date(2026, 11, 20, 15, 2, 0, 0, restoreHK)
	if err := repo.EnableInstance(ctx, store.Session{SessionID: "i1-session", InstanceID: "i1", DSLHash: "sha256:demo", ResolvedJSON: hkStockJSON, CreatedAt: enabledAt}, "name: demo"); err != nil {
		t.Fatal(err)
	}
	before, after := runWarmingSession(t, repo, "i1-session")
	if before["moox_strategy_dataset_last_success_timestamp_seconds"] != float64(enabledAt.Unix()) || before["moox_strategy_dataset_last_partial_timestamp_seconds"] == 0 {
		t.Fatalf("重启前应是启用时刻的空运行加预热 partial：%v", before)
	}
	expectSameGauges(t, before, after)
}

// 周三 ok 后停用、周五 15:02 重新启用，新会话只有 too_many_missing：重启后最近成功与输出水位不退回旧会话，四项与重启前一致。
func TestRestoreReenabledInstanceWithoutSessionOk(t *testing.T) {
	repo := openStore(t)
	ctx := context.Background()
	seedEnabled(t, repo, "i1", nil, hkStockJSON)
	wednesday := time.Date(2026, 11, 18, 10, 0, 0, 0, restoreHK)
	commitHKResult(t, repo, "r0", wednesday, store.StatusOK, "", 5*time.Second)
	if err := repo.DisableInstance(ctx, "i1", "i1-session", wednesday.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	instance, err := repo.GetInstance(ctx, "i1")
	if err != nil {
		t.Fatal(err)
	}
	enabledAt := time.Date(2026, 11, 20, 15, 2, 0, 0, restoreHK)
	if err := repo.EnableInstance(ctx, store.Session{SessionID: "i1-session-2", InstanceID: "i1", DSLHash: "sha256:demo", ResolvedJSON: string(instance.ResolvedJSON), CreatedAt: enabledAt}, "name: demo"); err != nil {
		t.Fatal(err)
	}
	before, after := runWarmingSession(t, repo, "i1-session-2")
	if got, want := int64(before["moox_strategy_dataset_output_watermark_timestamp_seconds"]), time.Date(2026, 11, 20, 14, 55, 0, 0, restoreHK).Unix(); got != want {
		t.Fatalf("重启前输出水位应是启用时刻已收线的 14:55 那根：%d，期望 %d", got, want)
	}
	expectSameGauges(t, before, after)
}
