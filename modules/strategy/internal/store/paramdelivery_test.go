package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// withNextParams 给 ok 结果附上下一期参数：目标期取结果期次之后一小时。
func withNextParams(t *testing.T, result Result) Result {
	t.Helper()
	members := json.RawMessage(`{"ma__var":{"window":{"default":20}}}`)
	raw, err := EncodeNextParams(result.BarEndTime.Add(time.Hour), members)
	if err != nil {
		t.Fatal(err)
	}
	result.NextParamsJSON = raw
	return result
}

func TestEncodeNextParamsRequiresPeriodAndMembers(t *testing.T) {
	if _, err := EncodeNextParams(time.Time{}, json.RawMessage(`{"a":{}}`)); err == nil {
		t.Fatal("缺目标期应报错")
	}
	if _, err := EncodeNextParams(testNow, nil); err == nil {
		t.Fatal("缺成员快照应报错")
	}
}

// 参数随结果同事务落库；投递循环只取会话里最新带参数的一条，送达或被拒绝后不再出现。
func TestPendingParamDeliveriesOnlyLatestUndelivered(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	for bar := 1; bar <= 3; bar++ {
		mustCommit(t, repo, withNextParams(t, okResult(fmt.Sprintf("r%d", bar), "i1", "session-1", bar, PublishNone)), nil, testNow)
	}
	// 没有参数的结果不参与投递，也不遮住带参数的最新一条。
	mustCommit(t, repo, okResult("r4", "i1", "session-1", 4, PublishNone), nil, testNow)

	pending, err := repo.PendingParamDeliveries(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0].ResultID != "r3" || pending[0].InstanceID != "i1" || pending[0].SessionID != "session-1" {
		t.Fatalf("应只投最新带参数的一条：%+v err=%v", pending, err)
	}
	if want := testNow.Add(4 * time.Hour); !pending[0].Period.Equal(want) {
		t.Fatalf("目标期不符：%v want=%v", pending[0].Period, want)
	}
	if !strings.Contains(string(pending[0].Members), `"ma__var"`) {
		t.Fatalf("成员快照不符：%s", pending[0].Members)
	}

	if err := repo.MarkParamsDelivered(ctx, "r3", testNow); err != nil {
		t.Fatal(err)
	}
	if pending, err := repo.PendingParamDeliveries(ctx, 10); err != nil || len(pending) != 0 {
		t.Fatalf("送达后不应再投：%+v err=%v", pending, err)
	}
	got, err := repo.GetResult(ctx, "r3")
	if err != nil || got.ParamsDeliveredAt == nil || got.ParamsError != "" {
		t.Fatalf("送达标记不符：%+v err=%v", got, err)
	}

	// 新一期带参数的结果出现后，只投新的；被拒绝后记下原因且不再重试。
	mustCommit(t, repo, withNextParams(t, okResult("r5", "i1", "session-1", 5, PublishNone)), nil, testNow)
	if pending, err := repo.PendingParamDeliveries(ctx, 10); err != nil || len(pending) != 1 || pending[0].ResultID != "r5" {
		t.Fatalf("应投新的一条：%+v err=%v", pending, err)
	}
	if err := repo.MarkParamsRejected(ctx, "r5", "参数超出范围"); err != nil {
		t.Fatal(err)
	}
	if pending, err := repo.PendingParamDeliveries(ctx, 10); err != nil || len(pending) != 0 {
		t.Fatalf("被拒绝后不应再投：%+v err=%v", pending, err)
	}
	if got, err := repo.GetResult(ctx, "r5"); err != nil || got.ParamsError != "参数超出范围" || got.ParamsDeliveredAt != nil {
		t.Fatalf("拒绝原因不符：%+v err=%v", got, err)
	}
}

// 实例停用后（会话不再挂在启用的实例上）旧会话的记录不再投递。
func TestPendingParamDeliveriesSkipsDisabledInstance(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	mustCommit(t, repo, withNextParams(t, okResult("r1", "i1", "session-1", 1, PublishNone)), nil, testNow)
	if err := repo.DisableInstance(ctx, "i1", ptr("session-1"), nil, "", testNow); err != nil {
		t.Fatal(err)
	}
	if pending, err := repo.PendingParamDeliveries(ctx, 10); err != nil || len(pending) != 0 {
		t.Fatalf("停用后不应投递：%+v err=%v", pending, err)
	}
}

// 最近 n 条带参数的结果都没有送达才算"连续没有送达"；不足 n 条或中间有送达的都不算。
func TestParamDeliveryStale(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	stale := func(n int) bool {
		t.Helper()
		got, err := repo.ParamDeliveryStale(ctx, "i1", "session-1", n)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if stale(2) || stale(0) {
		t.Fatal("没有带参数的结果时不应报告滞后")
	}
	mustCommit(t, repo, withNextParams(t, okResult("r1", "i1", "session-1", 1, PublishNone)), nil, testNow)
	if stale(2) {
		t.Fatal("不足 n 条不应报告滞后")
	}
	mustCommit(t, repo, withNextParams(t, okResult("r2", "i1", "session-1", 2, PublishNone)), nil, testNow)
	if !stale(2) {
		t.Fatal("最近两条都没送达应报告滞后")
	}
	if err := repo.MarkParamsDelivered(ctx, "r2", testNow); err != nil {
		t.Fatal(err)
	}
	if stale(2) {
		t.Fatal("最近一条已送达不应报告滞后")
	}
	// 被拒绝的记录没有送达，同样计入滞后。
	mustCommit(t, repo, withNextParams(t, okResult("r3", "i1", "session-1", 3, PublishNone)), nil, testNow)
	mustCommit(t, repo, withNextParams(t, okResult("r4", "i1", "session-1", 4, PublishNone)), nil, testNow)
	if err := repo.MarkParamsRejected(ctx, "r4", ""); err != nil {
		t.Fatal(err)
	}
	if !stale(2) {
		t.Fatal("被拒绝的记录应计入滞后")
	}
}

// 事件按结果 View 路由到实例：view_factor_<后缀> 对应 ftask_<后缀> 关联的启用实例；非因子结果 View 没有实例。
func TestEnabledInstancesByViewMapsResultViewToTask(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	got, err := repo.EnabledInstancesByView(ctx, "space", "view_factor_a")
	if err != nil || len(got) != 1 || got[0].InstanceID != "i1" || got[0].FactorTaskID != "ftask_a" {
		t.Fatalf("路由不符：%+v err=%v", got, err)
	}
	for _, view := range []string{"view_factor_b", "view_a", "not_a_view", ""} {
		if got, err := repo.EnabledInstancesByView(ctx, "space", view); err != nil || len(got) != 0 {
			t.Fatalf("%q 不应有实例：%+v err=%v", view, got, err)
		}
	}
	if got, err := repo.EnabledInstancesByView(ctx, "other-space", "view_factor_a"); err != nil || len(got) != 0 {
		t.Fatalf("其他空间不应有实例：%+v err=%v", got, err)
	}
}
