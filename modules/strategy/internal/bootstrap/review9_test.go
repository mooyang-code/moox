package bootstrap

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/prometheus/client_golang/prometheus"
)

// 重启后第一批积压事件先于第一次刷新到达：ObservePeriod 先写入的基准不能让 loadBases 认为已载入，成功口径的基准仍要从库里补，
// 否则周五 ok、周一重启后第一条跳过会让期望间隔退回 24 小时，周一 15:10 就报 success stale。
func TestStockBasesLoadedAfterEventsArrivedFirst(t *testing.T) {
	repo := openStore(t)
	stockJSON := `{"completion_kind":"collector.period.completed","view_id":"view_a","dataset_id":"ds","bar":"1d","calendar":"cn_stock","spot":true,"columns":{},"view_columns":["close"]}`
	seedEnabled(t, repo, "i1", nil, stockJSON)
	shanghai := time.FixedZone("CST", 8*3600)
	friday := time.Date(2026, 10, 9, 15, 0, 0, 0, shanghai)
	monday := time.Date(2026, 10, 12, 15, 0, 0, 0, shanghai)
	input, _ := json.Marshal(store.InputRecord{ViewID: "view_a", Bar: "1d", Calendar: "cn_stock", BarStart: friday.Add(-15 * time.Hour)})
	result := store.Result{ResultID: "r1", InstanceID: "i1", SessionID: "i1-session", BarEndTime: friday, ValidUntil: friday.Add(48 * time.Hour), Status: store.StatusOK, DSLHash: "sha256:demo",
		InputJSON: input, TargetsJSON: json.RawMessage(`[]`), RuleStatesJSON: json.RawMessage(`{}`), SummaryJSON: json.RawMessage(`{}`), PublishStatus: store.PublishNone, CreatedAt: friday.Add(time.Minute)}
	if _, _, err := repo.CommitResult(context.Background(), store.CommitRequest{Result: result, Now: friday.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	observer, err := newInstanceObserver(repo, prometheus.NewRegistry(), t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := repo.GetInstance(context.Background(), "i1")
	if err != nil {
		t.Fatal(err)
	}
	// 积压事件先到：周一跳过（只记运行，不是 ok）。
	observer.ObservePeriod(instance, "1d", monday, store.StatusSkipped, "previous_version_unknown")
	if err := observer.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := observer.expected["i1"].Interval; got != 40*time.Hour {
		t.Fatalf("事件先于刷新到达时，成功口径的基准也要从库里补，期望间隔应是 40 小时：%s", got)
	}
	// 之后的刷新不再读库：基准已载入。
	observer.lastOkBarEnd["i1"] = time.Time{}
	if err := observer.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !observer.lastOkBarEnd["i1"].IsZero() {
		t.Fatalf("已载入过基准的实例不应再读库：%s", observer.lastOkBarEnd["i1"])
	}
	if !observer.basesLoaded["i1"] {
		t.Fatal("成功读库后应记为已载入")
	}
	// 实例停用、移出启用清单后清除标记，重新启用时再载入。
	session := "i1-session"
	if err := repo.DisableInstance(context.Background(), "i1", &session, nil, store.HealthOK, monday); err != nil {
		t.Fatal(err)
	}
	if err := observer.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if observer.basesLoaded["i1"] {
		t.Fatal("停用后应清除已载入的标记")
	}
}

// 实例 ID 区分大小写，只差大小写的两个实例不能映射到同一个数据集标识：否则登记整批失败，所有实例都没有期望数据集，
// 停用的实例也撤销不了期望。含大写的 ID 用哈希，已是合法小写形式的 ID 直接使用。
func TestDatasetKeysAreDistinctForCaseOnlyDifferences(t *testing.T) {
	repo := openStore(t)
	for _, id := range []string{"Abc", "abc", "other"} {
		seedEnabled(t, repo, id, nil, resolvedJSON)
	}
	observer, err := newInstanceObserver(repo, prometheus.NewRegistry(), t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.refresh(context.Background()); err != nil {
		t.Fatalf("只差大小写的实例不应让登记失败：%v", err)
	}
	if len(observer.expected) != 3 {
		t.Fatalf("三个启用实例都应登记：%v", observer.expected)
	}
	if got := observer.expected["abc"].Key.DatasetID; got != "strategy_abc" {
		t.Fatalf("合法小写形式的 ID 直接使用：%q", got)
	}
	if upper, lower := observer.expected["Abc"].Key.DatasetID, observer.expected["abc"].Key.DatasetID; upper == lower || len(upper) != len("strategy_")+16 {
		t.Fatalf("含大写的 ID 应用哈希且与小写的不同：%q 与 %q", upper, lower)
	}
}
