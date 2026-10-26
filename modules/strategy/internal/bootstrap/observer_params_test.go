package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

const tunedResolvedJSON = `{"completion_kind":"factor_period.computed","view_id":"view_factor_a","factor_task_id":"ftask_a","dataset_id":"ds","bar":"1h","calendar":"crypto_24x7","spot":true,"columns":{},"view_columns":["close"],"factor_params":{"f__var":[{"name":"window","type":"int","min":5,"max":60,"default":20}]}}`

// commitParams 给实例的当前会话提交第 bar 期的结果，附上下一期参数；delivered 为真时同时记为已送达。
func commitParams(t *testing.T, repo *store.Store, instanceID string, bar int, delivered bool) {
	t.Helper()
	barEnd := seedTime.Add(time.Duration(bar) * time.Hour)
	next, err := store.EncodeNextParams(barEnd.Add(time.Hour), json.RawMessage(`{"f__var":{"window":{"default":20}}}`))
	if err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("%s-r%d", instanceID, bar)
	result := store.Result{ResultID: id, InstanceID: instanceID, SessionID: instanceID + "-session", BarEndTime: barEnd, ValidUntil: barEnd.Add(2 * time.Hour), Status: store.StatusOK, DSLHash: "sha256:demo",
		InputJSON: json.RawMessage(`{}`), TargetsJSON: json.RawMessage(`[]`), RuleStatesJSON: json.RawMessage(`{}`), SummaryJSON: json.RawMessage(`{}`), PublishStatus: store.PublishNone,
		NextParamsJSON: next, CreatedAt: barEnd}
	if _, _, err := repo.CommitResult(context.Background(), store.CommitRequest{Result: result, Now: barEnd}); err != nil {
		t.Fatal(err)
	}
	if delivered {
		if err := repo.MarkParamsDelivered(context.Background(), id, barEnd); err != nil {
			t.Fatal(err)
		}
	}
}

func staleValue(t *testing.T, registry *prometheus.Registry, instanceID string) float64 {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != paramStaleMetric {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "instance_id" && label.GetValue() == instanceID {
					hasSpace := false
					for _, other := range metric.GetLabel() {
						hasSpace = hasSpace || (other.GetName() == "space_id" && other.GetValue() == "crypto")
					}
					if !hasSpace {
						t.Fatalf("参数投递滞后指标应带 space_id 标签：%v", metric.GetLabel())
					}
					return metric.GetGauge().GetValue()
				}
			}
		}
	}
	t.Fatalf("没有实例 %s 的 %s 序列", instanceID, paramStaleMetric)
	return 0
}

// 有可变成员的实例，最近 N 期带参数的结果都没有送达时导出 1；有一期送达就回到 0；停用后清零；没有可变成员的实例不导出。
func TestObserverExportsParamDeliveryStale(t *testing.T) {
	repo := openStore(t)
	seedEnabled(t, repo, "i1", nil, tunedResolvedJSON)
	seedEnabled(t, repo, "plain", nil, resolvedJSON)
	registry := prometheus.NewRegistry()
	observer, err := newInstanceObserver(repo, registry, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	observer.SetStaleAlertPeriods(2)
	ctx := context.Background()

	commitParams(t, repo, "i1", 1, false)
	if err := observer.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := staleValue(t, registry, "i1"); got != 0 {
		t.Fatalf("不足 N 期不应告警：%v", got)
	}
	commitParams(t, repo, "i1", 2, false)
	if err := observer.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := staleValue(t, registry, "i1"); got != 1 {
		t.Fatalf("连续 2 期没有送达应告警：%v", got)
	}
	if n := testutil.CollectAndCount(observer.paramStale); n != 1 {
		t.Fatalf("没有可变成员的实例不应导出序列：%d", n)
	}

	commitParams(t, repo, "i1", 3, true)
	if err := observer.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := staleValue(t, registry, "i1"); got != 0 {
		t.Fatalf("最近一期送达后应恢复：%v", got)
	}

	commitParams(t, repo, "i1", 4, false)
	commitParams(t, repo, "i1", 5, false)
	if err := observer.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := staleValue(t, registry, "i1"); got != 1 {
		t.Fatalf("再次连续未送达应告警：%v", got)
	}
	session := "i1-session"
	if err := repo.DisableInstance(ctx, "i1", &session, nil, store.HealthOK, seedTime); err != nil {
		t.Fatal(err)
	}
	if err := observer.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := staleValue(t, registry, "i1"); got != 0 {
		t.Fatalf("停用后应清零：%v", got)
	}

	// 阈值为 0 表示不告警。
	observer.SetStaleAlertPeriods(0)
	if err := observer.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	observer.ParamSubmitted("delivered")
	if got := testutil.ToFloat64(observer.paramSubmits.WithLabelValues("delivered")); got != 1 {
		t.Fatalf("投递终态计数不符：%v", got)
	}
}
