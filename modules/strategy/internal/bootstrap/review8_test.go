package bootstrap

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
)

// 重启（或重新启用）后观察器的内存基准是空的：刷新时从库里取最近处理的一根与最近一个 ok 的一根，期望间隔与重启前
// 一致（周五 ok、周一跳过仍是 40 小时，而不是退回 24 小时、周一就报 success stale）。
func TestStockBasesAreLoadedFromStore(t *testing.T) {
	repo := openStore(t)
	stockJSON := `{"completion_kind":"collector.period.completed","view_id":"view_a","dataset_id":"ds","bar":"1d","calendar":"cn_stock","spot":true,"columns":{},"view_columns":["close"]}`
	seedEnabled(t, repo, "i1", nil, stockJSON)
	shanghai := time.FixedZone("CST", 8*3600)
	friday := time.Date(2026, 10, 9, 15, 0, 0, 0, shanghai)
	monday := time.Date(2026, 10, 12, 15, 0, 0, 0, shanghai)
	for _, item := range []struct {
		id     string
		barEnd time.Time
		status string
		reason string
	}{{"r1", friday, store.StatusOK, ""}, {"r2", monday, store.StatusSkipped, "factor_missing"}} {
		input, _ := json.Marshal(store.InputRecord{ViewID: "view_a", Bar: "1d", Calendar: "cn_stock", BarStart: item.barEnd.Add(-15 * time.Hour)})
		result := store.Result{ResultID: item.id, InstanceID: "i1", SessionID: "i1-session", BarEndTime: item.barEnd, ValidUntil: item.barEnd.Add(48 * time.Hour), Status: item.status, SkipReason: item.reason, DSLHash: "sha256:demo",
			InputJSON: input, TargetsJSON: json.RawMessage(`[]`), RuleStatesJSON: json.RawMessage(`{}`), SummaryJSON: json.RawMessage(`{}`), PublishStatus: store.PublishNone, CreatedAt: item.barEnd.Add(time.Minute)}
		if _, _, err := repo.CommitResult(context.Background(), store.CommitRequest{Result: result, Now: item.barEnd.Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	observer, err := newInstanceObserver(repo, prometheus.NewRegistry(), t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := observer.expected["i1"].Interval; got != 40*time.Hour {
		t.Fatalf("重启后的期望间隔应按库里的基准取 40 小时：%s", got)
	}
}

// 期望间隔的两个系数与 Monitor 的数据集健康策略一致。
func TestMissedIntervalsMatchHealthPolicy(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	policy, err := report.LoadDatasetHealthPolicy(filepath.Join(filepath.Dir(file), "../../../../config/setup/dataset-health-policy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	defaults := policy.RealtimeTimeSeries.Defaults
	if defaults.RunMissedIntervals != runMissedIntervals || defaults.SuccessMissedIntervals != successMissedIntervals {
		t.Fatalf("系数应与健康策略一致：run=%d/%d success=%d/%d", defaults.RunMissedIntervals, runMissedIntervals, defaults.SuccessMissedIntervals, successMissedIntervals)
	}
}
