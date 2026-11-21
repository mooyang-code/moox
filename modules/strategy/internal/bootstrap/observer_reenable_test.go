package bootstrap

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/prometheus/client_golang/prometheus"
)

// 停用后在休市时重新启用：Monitor 里的输出水位停在停用前最后处理的那根，停用期间的周期不会补跑。重新登记时把输出水位推到
// 启用时刻已收线的最近一根，Monitor 只等启用之后收线的根；进程重启后再次登记结果不变；本次会话处理过周期后不再推。
func TestReenabledStockInstanceAdvancesWatermarkToSessionStart(t *testing.T) {
	repo := openStore(t)
	ctx := context.Background()
	hkJSON := `{"market":{"select":"spot","prefer":"spot"},"tasks":{"spot":{"view_id":"view_factor_a","factor_task_id":"ftask_a","dataset_id":"ds"}},"bar":"5m","calendar":"hk_stock","columns":{},"view_columns":["close"]}`
	seedEnabled(t, repo, "i1", nil, hkJSON)
	hongKong := time.FixedZone("HKT", 8*3600)
	commit := func(id, session string, barEnd time.Time) {
		t.Helper()
		record, _ := json.Marshal(store.InputRecord{Views: map[string]store.ViewInput{"view_a": {}}, Bar: "5m", Calendar: "hk_stock", BarStart: barEnd.Add(-5 * time.Minute)})
		result := store.Result{ResultID: id, InstanceID: "i1", SessionID: session, BarEndTime: barEnd, ValidUntil: barEnd.Add(time.Hour), Status: store.StatusOK, DSLHash: "sha256:demo",
			InputJSON: record, TargetsJSON: json.RawMessage(`[]`), SummaryJSON: json.RawMessage(`{}`), PublishStatus: store.PublishNone, CreatedAt: barEnd.Add(5 * time.Second)}
		if _, _, err := repo.CommitResult(ctx, store.CommitRequest{Result: result, Now: barEnd.Add(5 * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	// 周三 10:00 那根处理完后停用，周六 09:27 重新启用。
	wednesday := time.Date(2026, 11, 18, 10, 0, 0, 0, hongKong)
	commit("r1", "i1-session", wednesday)
	if err := repo.DisableInstance(ctx, "i1", "i1-session", wednesday.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	instance, err := repo.GetInstance(ctx, "i1")
	if err != nil {
		t.Fatal(err)
	}
	saturday := time.Date(2026, 11, 21, 9, 27, 0, 0, hongKong)
	if err := repo.EnableInstance(ctx, store.Session{SessionID: "i1-session-2", InstanceID: "i1", DSLHash: "sha256:demo", ResolvedJSON: string(instance.ResolvedJSON), CreatedAt: saturday}, "name: demo"); err != nil {
		t.Fatal(err)
	}
	register := func(now time.Time) func(string) (float64, bool) {
		registry := prometheus.NewRegistry()
		observer, err := newInstanceObserver(repo, registry, t.Logf)
		if err != nil {
			t.Fatal(err)
		}
		observer.now = func() time.Time { return now }
		if err := observer.refresh(ctx); err != nil {
			t.Fatal(err)
		}
		return func(name string) (float64, bool) {
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
	}
	fridayLast := time.Date(2026, 11, 20, 15, 55, 0, 0, hongKong).Unix()
	for _, now := range []time.Time{saturday.Add(3 * time.Minute), saturday.Add(30 * time.Hour)} {
		value := register(now)
		if got, ok := value("moox_strategy_dataset_output_watermark_timestamp_seconds"); !ok || int64(got) != fridayLast {
			t.Fatalf("%s 登记后输出水位应推到启用时刻已收线的周五 15:55 那根：%v ok=%v", now, got, ok)
		}
		if got, ok := value("moox_strategy_dataset_last_run_timestamp_seconds"); !ok || int64(got) != saturday.Unix() {
			t.Fatalf("%s 登记后最近运行应是启用时刻：%v ok=%v", now, got, ok)
		}
	}
	// 周一处理完第一根后进程重启：本次会话已越过启用时刻的那根，不再记宽限。
	monday := time.Date(2026, 11, 23, 9, 35, 0, 0, hongKong)
	commit("r2", "i1-session-2", monday)
	value := register(monday.Add(time.Minute))
	if got, ok := value("moox_strategy_dataset_output_watermark_timestamp_seconds"); ok && got != 0 {
		t.Fatalf("本次会话已处理过周期，不应再推输出水位：%v", got)
	}
	if got, ok := value("moox_strategy_dataset_last_run_timestamp_seconds"); ok && got != 0 {
		t.Fatalf("本次会话已处理过周期，不应再记宽限：%v", got)
	}
}
