package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 确认列（批 4 T4）：没挂账户的实例启用即确认；挂了账户的启用后为空、确认实盘后写入；停用、修改都清空。
// 读实例的每一条路径（按 ID、四种列表、按 View 路由）都要带上它：漏读的路径会让已确认的实例一直发影子目标，或页面一直显示试运行。
func TestConfirmedAtFollowsEnableConfirmDisableAndEveryReadPath(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	if err := repo.CreateDefinition(ctx, Definition{StrategyID: "s1", Name: "demo", DSLYaml: "name: demo", DSLHash: testHash, CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	account := "acct-1"
	for _, instance := range []Instance{
		{InstanceID: "trader", StrategyID: "s1", SpaceID: "space", FactorTasks: map[string]string{"spot": "ftask_a"}, LogicalAccountID: &account, CreatedAt: testNow},
		{InstanceID: "observer", StrategyID: "s1", SpaceID: "space", FactorTasks: map[string]string{"spot": "ftask_b"}, CreatedAt: testNow},
	} {
		if err := repo.CreateInstance(ctx, instance); err != nil {
			t.Fatal(err)
		}
	}
	enable := func(instanceID, session, taskID, datasetID string) {
		t.Helper()
		resolved := `{"market":{"select":"spot","prefer":"spot"},"tasks":{"spot":{"dataset_id":"` + datasetID + `","factor_task_id":"` + taskID + `"}}}`
		if err := repo.EnableInstance(ctx, Session{SessionID: session, InstanceID: instanceID, DSLHash: testHash, ResolvedJSON: resolved, CreatedAt: testNow}, "name: demo"); err != nil {
			t.Fatal(err)
		}
	}
	enable("trader", "session-1", "ftask_a", "dataset_factor_a")
	enable("observer", "session-2", "ftask_b", "dataset_factor_b")

	if observer, err := repo.GetInstance(ctx, "observer"); err != nil || observer.ConfirmedAt == nil || observer.Trial() {
		t.Fatalf("没挂账户的实例启用即确认：%+v err=%v", observer, err)
	}
	if trader, err := repo.GetInstance(ctx, "trader"); err != nil || trader.ConfirmedAt != nil || !trader.Trial() {
		t.Fatalf("挂了账户的实例启用后在试运行：%+v err=%v", trader, err)
	}

	// 停用的实例、换过的会话、没挂账户的实例都不能确认。
	if err := repo.ConfirmInstance(ctx, "trader", "session-x", testNow); err == nil || !strings.Contains(err.Error(), "会话已变化") {
		t.Fatalf("会话不符时不应确认：%v", err)
	}
	if err := repo.ConfirmInstance(ctx, "observer", "session-2", testNow); err == nil || !strings.Contains(err.Error(), "没有挂组合账户") {
		t.Fatalf("没挂账户的实例不需要确认：%v", err)
	}
	confirmedAt := testNow.Add(time.Minute)
	if err := repo.ConfirmInstance(ctx, "trader", "session-1", confirmedAt); err != nil {
		t.Fatal(err)
	}
	// 再确认一次是成功，不改确认时间。
	if err := repo.ConfirmInstance(ctx, "trader", "session-1", confirmedAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	confirmed := func(where string, list []Instance) {
		t.Helper()
		for _, item := range list {
			if item.InstanceID == "trader" {
				if item.ConfirmedAt == nil || !item.ConfirmedAt.Equal(confirmedAt) || item.Trial() {
					t.Fatalf("%s 读到的确认时间不符：%+v", where, item.ConfirmedAt)
				}
				return
			}
		}
		t.Fatalf("%s 没有读到实例", where)
	}
	got, err := repo.GetInstance(ctx, "trader")
	if err != nil {
		t.Fatal(err)
	}
	confirmed("GetInstance", []Instance{got})
	enabledOnly := true
	for name, args := range map[string]struct {
		space   string
		enabled *bool
	}{"全部": {}, "按空间": {space: "space"}, "按启用状态": {enabled: &enabledOnly}, "按空间与启用状态": {space: "space", enabled: &enabledOnly}} {
		list, err := repo.ListInstances(ctx, args.space, args.enabled)
		if err != nil {
			t.Fatal(err)
		}
		confirmed("ListInstances "+name, list)
	}
	byView, err := repo.EnabledInstancesByDataset(ctx, "space", "dataset_factor_a")
	if err != nil {
		t.Fatal(err)
	}
	confirmed("EnabledInstancesByDataset", byView)

	// 停用清空；重新启用从试运行开始。
	if err := repo.DisableInstance(ctx, "trader", "session-1", testNow); err != nil {
		t.Fatal(err)
	}
	if disabled, err := repo.GetInstance(ctx, "trader"); err != nil || disabled.ConfirmedAt != nil || disabled.Trial() {
		t.Fatalf("停用应清空确认时间：%+v err=%v", disabled, err)
	}
	enable("trader", "session-3", "ftask_a", "dataset_factor_a")
	if reenabled, err := repo.GetInstance(ctx, "trader"); err != nil || reenabled.ConfirmedAt != nil || !reenabled.Trial() {
		t.Fatalf("重新启用应回到试运行：%+v err=%v", reenabled, err)
	}
	if err := repo.ConfirmInstance(ctx, "trader", "session-1", testNow); err == nil {
		t.Fatal("旧会话的确认不应落到新会话上")
	}

	// 修改清空：停用后改成观察实例，再启用就直接确认。
	if err := repo.ConfirmInstance(ctx, "trader", "session-3", testNow); err != nil {
		t.Fatal(err)
	}
	if err := repo.DisableInstance(ctx, "trader", "session-3", testNow); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateInstance(ctx, Instance{InstanceID: "trader", StrategyID: "s1", SpaceID: "space", FactorTasks: map[string]string{"spot": "ftask_a"}, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if updated, err := repo.GetInstance(ctx, "trader"); err != nil || updated.ConfirmedAt != nil {
		t.Fatalf("修改应清空确认时间：%+v err=%v", updated, err)
	}

	// 表约束兜底：停用的实例不能带确认时间。
	if err := repo.db.Exec(`UPDATE t_strategy_instances SET c_confirmed_at = CURRENT_TIMESTAMP WHERE c_instance_id = 'trader'`).Error; err == nil {
		t.Fatal("停用的实例不应带确认时间")
	}
}

// 写确认时间是按“还是这次启用、还没确认”的条件更新：条件已经不成立（读完之后会话变了）、一行都没改到时报“会话已变化”，
// 不能当成确认成功（页面会提示“已确认”，实际仍在试运行）。
func TestConfirmInstanceRowReportsAChangedSession(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	if err := repo.CreateDefinition(ctx, Definition{StrategyID: "s1", Name: "demo", DSLYaml: "name: demo", DSLHash: testHash, CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	account := "acct-1"
	if err := repo.CreateInstance(ctx, Instance{InstanceID: "trader", StrategyID: "s1", SpaceID: "space", FactorTasks: map[string]string{"spot": "ftask_a"}, LogicalAccountID: &account, CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	resolved := `{"market":{"select":"spot","prefer":"spot"},"tasks":{"spot":{"dataset_id":"dataset_factor_a","factor_task_id":"ftask_a"}}}`
	if err := repo.EnableInstance(ctx, Session{SessionID: "session-1", InstanceID: "trader", DSLHash: testHash, ResolvedJSON: resolved, CreatedAt: testNow}, "name: demo"); err != nil {
		t.Fatal(err)
	}
	if err := confirmInstanceRow(repo.db.WithContext(ctx), "trader", "session-old", testNow); err == nil || !strings.Contains(err.Error(), "会话已变化") {
		t.Fatalf("会话已经不是这一次时应报会话已变化：%v", err)
	}
	if trader, err := repo.GetInstance(ctx, "trader"); err != nil || trader.ConfirmedAt != nil {
		t.Fatalf("没有写入确认时间：%+v err=%v", trader, err)
	}
	if err := confirmInstanceRow(repo.db.WithContext(ctx), "trader", "session-1", testNow); err != nil {
		t.Fatal(err)
	}
	if err := confirmInstanceRow(repo.db.WithContext(ctx), "trader", "session-1", testNow); err == nil {
		t.Fatal("已经确认过，再按“还没确认”的条件更新改不到行")
	}
}
