package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

const tunedResolvedJSON = `{"market":{"select":"spot","prefer":"spot"},"tasks":{"spot":{"dataset_id":"dataset_factor_a","factor_task_id":"ftask_a","dataset_id":"ds"}},"bar":"1h","calendar":"crypto_24x7","columns":{},"view_columns":["close"],"factor_params":{"f__var":[{"name":"window","type":"int","min":5,"max":60,"default":20}]}}`

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
		InputJSON: json.RawMessage(`{}`), TargetsJSON: json.RawMessage(`[]`), SummaryJSON: json.RawMessage(`{}`), PublishStatus: store.PublishNone,
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

// staleValue 是实例在关注项 param_delivery_stale 里时为 1，否则为 0（detail 写“空间/实例”）。
func staleValue(t *testing.T, observer *instanceObserver, instanceID string) float64 {
	t.Helper()
	if strings.Contains(observer.attention.Active()[attentionParamDeliveryStale], "crypto/"+instanceID) {
		return 1
	}
	return 0
}

// 有可变成员的实例，最近 N 期带参数的结果都没有送达时列进关注项；有一期送达就消失；停用后消失；没有可变成员的实例不列。
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
	if got := staleValue(t, observer, "i1"); got != 0 {
		t.Fatalf("不足 N 期不应告警：%v", got)
	}
	commitParams(t, repo, "i1", 2, false)
	if err := observer.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := staleValue(t, observer, "i1"); got != 1 {
		t.Fatalf("连续 2 期没有送达应告警：%v", got)
	}
	if got := staleValue(t, observer, "plain"); got != 0 {
		t.Fatalf("没有可变成员的实例不应列出：%v", got)
	}

	commitParams(t, repo, "i1", 3, true)
	if err := observer.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := staleValue(t, observer, "i1"); got != 0 {
		t.Fatalf("最近一期送达后应恢复：%v", got)
	}

	commitParams(t, repo, "i1", 4, false)
	commitParams(t, repo, "i1", 5, false)
	if err := observer.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := staleValue(t, observer, "i1"); got != 1 {
		t.Fatalf("再次连续未送达应告警：%v", got)
	}
	session := "i1-session"
	if err := repo.DisableInstance(ctx, "i1", session, seedTime); err != nil {
		t.Fatal(err)
	}
	if err := observer.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := staleValue(t, observer, "i1"); got != 0 {
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
