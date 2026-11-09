package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// 附加来源与参考市场的任务按（来源, 市场）登记：读回的任务键不变；启用握手按会话快照里的主帧、参考市场与来源一起核对；
// 来源 View 的事件也能路由到实例。
func TestInstanceSourceTasksRoundTrip(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	if err := repo.CreateDefinition(ctx, Definition{StrategyID: "s1", Name: "demo", DSLYaml: "name: demo", DSLHash: testHash, CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	tasks := map[string]string{"swap": "ftask_main", "spot": "ftask_ref", "daily:swap": "ftask_daily"}
	if err := repo.CreateInstance(ctx, Instance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", FactorTasks: tasks, CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetInstance(ctx, "i1")
	if err != nil || !SameTasks(got.FactorTasks, tasks) {
		t.Fatalf("任务键应原样读回：%v err=%v", got.FactorTasks, err)
	}
	listed, err := repo.ListInstances(ctx, "space", nil)
	if err != nil || len(listed) != 1 || !SameTasks(listed[0].FactorTasks, tasks) {
		t.Fatalf("列表里的任务键：%+v err=%v", listed, err)
	}
	resolved := `{"market":{"select":"swap","prefer":"swap"},"tasks":{"swap":{"view_id":"view_factor_main","factor_task_id":"ftask_main"}},` +
		`"references":{"spot":{"view_id":"view_factor_ref","factor_task_id":"ftask_ref"}},` +
		`"sources":{"daily":{"bar":"1d","calendar":"crypto_24x7","tasks":{"swap":{"view_id":"view_factor_daily","factor_task_id":"ftask_daily"}}}}}`
	if got := SessionTasks([]byte(resolved)); !SameTasks(got, tasks) {
		t.Fatalf("会话快照里的全部任务：%v", got)
	}
	session := "session-1"
	if err := repo.OpenSession(ctx, Session{SessionID: session, InstanceID: "i1", DSLHash: testHash, ResolvedJSON: resolved, CreatedAt: testNow}, "name: demo"); err != nil {
		t.Fatal(err)
	}
	attachAndEnable(t, repo, "i1", session, json.RawMessage(resolved))
	byView, err := repo.EnabledInstancesByView(ctx, "space", "view_factor_daily")
	if err != nil || len(byView) != 1 || byView[0].InstanceID != "i1" {
		t.Fatalf("来源 View 的事件应路由到实例：%+v err=%v", byView, err)
	}
	// 同一个来源名的同一个市场只能登记一个任务（主键是 实例, 来源, 市场），不同来源可以各绑一个市场。
	if err := ValidateTaskKeys(map[string]string{"spot": "a", "x:spot": "b", "y:spot": "c"}); err != nil {
		t.Fatalf("两个来源各绑一个市场应通过：%v", err)
	}
	for name, tc := range map[string]struct {
		tasks map[string]string
		want  string
	}{
		"只有来源":   {map[string]string{"x:spot": "a"}, "主帧"},
		"三个主帧键":  {map[string]string{"spot": "a", "swap": "b", "equity": "c"}, "一个或两个"},
		"来源名非法":  {map[string]string{"spot": "a", "X:spot": "b"}, "来源名"},
		"市场非法":   {map[string]string{"spot": "a", "x:futures": "b"}, "不受支持"},
		"来源任务为空": {map[string]string{"spot": "a", "x:spot": " "}, "不能为空"},
	} {
		if err := ValidateTaskKeys(tc.tasks); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s：期望错误含 %q，实际 %v", name, tc.want, err)
		}
	}
}
