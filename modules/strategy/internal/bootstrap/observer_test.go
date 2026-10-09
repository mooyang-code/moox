package bootstrap

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestDatasetKeyDerivation(t *testing.T) {
	expectation, ok := datasetKey(store.Instance{InstanceID: "3f2a6c1e-0b7d-4a51-9c2e-111111111111", SpaceID: "crypto"}, "1h")
	if !ok || expectation.Key.DatasetID != "strategy_3f2a6c1e-0b7d-4a51-9c2e-111111111111" || expectation.Key.Freq != "1h" || expectation.Interval != time.Hour {
		t.Fatalf("数据集标识不符：%+v ok=%v", expectation, ok)
	}
	hashed, ok := datasetKey(store.Instance{InstanceID: "My Instance/with spaces and a very long name that exceeds limits", SpaceID: "crypto"}, "4h")
	if !ok || !strings.HasPrefix(hashed.Key.DatasetID, "strategy_") || len(hashed.Key.DatasetID) != len("strategy_")+16 || hashed.Interval != 4*time.Hour {
		t.Fatalf("不合法的实例 ID 应改用哈希：%+v", hashed)
	}
	if _, ok := datasetKey(store.Instance{InstanceID: "i1", SpaceID: "Bad Space"}, "1h"); ok {
		t.Fatal("不合法的空间 ID 应跳过")
	}
	if _, ok := datasetKey(store.Instance{InstanceID: "i1", SpaceID: "crypto"}, "7x"); ok {
		t.Fatal("不合法的周期应跳过")
	}
}

func TestInstanceObserverRegistersEnabledInstancesAndCountsPeriods(t *testing.T) {
	repo := openStore(t)
	seedEnabled(t, repo, "i1", nil, resolvedJSON)
	registry := prometheus.NewRegistry()
	observer, err := newInstanceObserver(repo, registry, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(observer.expected) != 1 {
		t.Fatalf("应登记 1 个启用实例：%+v", observer.expected)
	}
	instance, err := repo.GetInstance(context.Background(), "i1")
	if err != nil {
		t.Fatal(err)
	}
	barEnd := time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC)
	observer.ObservePeriod(instance, "1h", barEnd, store.StatusOK, "")
	observer.ObservePeriod(instance, "1h", barEnd.Add(time.Hour), store.StatusSkipped, "factor_missing")
	if got := testutil.ToFloat64(observer.periods.WithLabelValues("i1", "ok", "")); got != 1 {
		t.Fatalf("ok 计数不符：%v", got)
	}
	if got := testutil.ToFloat64(observer.periods.WithLabelValues("i1", "skipped", "factor_missing")); got != 1 {
		t.Fatalf("skipped 计数不符：%v", got)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, family := range families {
		found[family.GetName()] = true
	}
	for _, name := range []string{"moox_strategy_dataset_enabled", "moox_strategy_dataset_last_success_timestamp_seconds", "moox_strategy_dataset_output_watermark_timestamp_seconds", "moox_strategy_period_total", "moox_strategy_runs_total"} {
		if !found[name] {
			t.Fatalf("缺少指标 %s", name)
		}
	}
	// 新启用、尚未刷新清单的实例在首次观测时自动登记。
	seedEnabled(t, repo, "i2", nil, resolvedJSON)
	second, err := repo.GetInstance(context.Background(), "i2")
	if err != nil {
		t.Fatal(err)
	}
	observer.ObservePeriod(second, "1h", barEnd, store.StatusOK, "")
	if len(observer.expected) != 2 {
		t.Fatalf("首次观测应登记新实例：%+v", observer.expected)
	}
	// 停用后刷新清单即移出。
	session := "i1-session"
	if err := repo.DisableInstance(context.Background(), "i1", &session, nil, "", seedTime); err != nil {
		t.Fatal(err)
	}
	if err := observer.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := observer.expected["i1"]; ok || len(observer.expected) != 1 {
		t.Fatalf("停用实例应移出清单：%+v", observer.expected)
	}
}
