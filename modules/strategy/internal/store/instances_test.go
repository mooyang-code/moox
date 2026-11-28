package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

const spotResolved = `{"market":{"select":"spot","prefer":"spot"},"tasks":{"spot":{"dataset_id":"dataset_factor_a","factor_task_id":"ftask_a"}}}`

func TestInstanceRoundTripAndEnabledAccountUniqueness(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	if err := repo.CreateDefinition(ctx, Definition{StrategyID: "s1", Name: "demo", DSLYaml: "name: demo", DSLHash: testHash, CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	account := "acct-1"
	first := Instance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", FactorTasks: map[string]string{"spot": "ftask_a"}, LogicalAccountID: &account, CreatedAt: testNow}
	second := first
	second.InstanceID, second.FactorTasks = "i2", map[string]string{"spot": "ftask_b"}
	for _, instance := range []Instance{first, second} {
		if err := repo.CreateInstance(ctx, instance); err != nil {
			t.Fatal(err)
		}
	}
	got, err := repo.GetInstance(ctx, "i1")
	if err != nil || got.StrategyName != "demo" || got.Enabled || got.SessionID != nil || got.FactorTasks["spot"] != "ftask_a" || got.Health != HealthOK || string(got.ResolvedJSON) != "{}" || *got.LogicalAccountID != account {
		t.Fatalf("实例不符：%+v err=%v", got, err)
	}
	if err := repo.EnableInstance(ctx, Session{SessionID: "session-1", InstanceID: "i1", DSLHash: testHash, ResolvedJSON: spotResolved, CreatedAt: testNow}, "name: demo"); err != nil {
		t.Fatal(err)
	}
	// 同一账户不能被两个启用实例占用：唯一索引让第二个启用整体回滚，不留下会话。
	if err := repo.EnableInstance(ctx, Session{SessionID: "session-2", InstanceID: "i2", DSLHash: testHash, ResolvedJSON: `{"market":{"select":"spot","prefer":"spot"},"tasks":{"spot":{"dataset_id":"dataset_factor_b","factor_task_id":"ftask_b"}}}`, CreatedAt: testNow}, "name: demo"); err == nil {
		t.Fatal("同一账户不能被两个启用实例占用")
	}
	if sessions, err := repo.ListSessions(ctx, "i2"); err != nil || len(sessions) != 0 {
		t.Fatalf("被拒绝的启用不应留下会话：%+v err=%v", sessions, err)
	}
	enabled, err := repo.GetInstance(ctx, "i1")
	if err != nil || !enabled.Enabled || *enabled.SessionID != "session-1" || string(enabled.ResolvedJSON) != spotResolved {
		t.Fatalf("启用后的实例不符：%+v err=%v", enabled, err)
	}
	byView, err := repo.EnabledInstancesByDataset(ctx, "space", "dataset_factor_a")
	if err != nil || len(byView) != 1 || byView[0].InstanceID != "i1" {
		t.Fatalf("按 View 路由不符：%+v err=%v", byView, err)
	}
	enabledOnly := true
	listed, err := repo.ListInstances(ctx, "space", &enabledOnly)
	if err != nil || len(listed) != 1 {
		t.Fatalf("启用列表不符：%+v err=%v", listed, err)
	}
	// 绑定解析只存在会话里：停用后读最近一次会话的那份。
	if err := repo.DisableInstance(ctx, "i1", "session-1", testNow); err != nil {
		t.Fatal(err)
	}
	disabled, err := repo.GetInstance(ctx, "i1")
	if err != nil || disabled.Enabled || string(disabled.ResolvedJSON) != spotResolved {
		t.Fatalf("停用后应读最近一次会话的绑定解析：%+v err=%v", disabled, err)
	}
}

// 启用、停用各是一个事务（批 3 的 3.6）：没有“挂着会话的停用实例”，停用之后马上能改实例；启用期间实例被改了就整体回滚。
func TestEnableAndDisableAreSingleTransactions(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	if err := repo.CreateDefinition(ctx, Definition{StrategyID: "s1", Name: "demo", DSLYaml: "name: demo", DSLHash: testHash, CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	account := "acct-1"
	if err := repo.CreateInstance(ctx, Instance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", FactorTasks: map[string]string{"spot": "ftask_a"}, LogicalAccountID: &account, CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	countSessions := func() int {
		sessions, err := repo.ListSessions(ctx, "i1")
		if err != nil {
			t.Fatal(err)
		}
		return len(sessions)
	}
	// 解析期间定义被改（DSL 版本不一致）或任务对不上：整体拒绝，什么都不写。
	if err := repo.EnableInstance(ctx, Session{SessionID: "stale", InstanceID: "i1", DSLHash: "sha256:old", ResolvedJSON: spotResolved, CreatedAt: testNow}, "name: old"); err == nil || !strings.Contains(err.Error(), "被修改") {
		t.Fatalf("DSL 版本不一致应拒绝：%v", err)
	}
	if err := repo.EnableInstance(ctx, Session{SessionID: "stale", InstanceID: "i1", DSLHash: testHash, ResolvedJSON: `{"tasks":{"spot":{"factor_task_id":"ftask_other"}}}`, CreatedAt: testNow}, "name: demo"); err == nil || !strings.Contains(err.Error(), "被修改") {
		t.Fatalf("任务不一致应拒绝：%v", err)
	}
	if countSessions() != 0 {
		t.Fatal("被拒绝的启用不应写会话")
	}
	session := "session-1"
	if err := repo.EnableInstance(ctx, Session{SessionID: session, InstanceID: "i1", DSLHash: testHash, ResolvedJSON: spotResolved, CreatedAt: testNow}, "name: demo"); err != nil {
		t.Fatal(err)
	}
	opened, err := repo.GetSession(ctx, session)
	if err != nil || opened.ClosedAt != nil || opened.LogicalAccountID == nil || *opened.LogicalAccountID != account || opened.StartedAt <= 0 {
		t.Fatalf("会话应记下当时的账户与开始时刻：%+v err=%v", opened, err)
	}
	other := "session-2"
	if err := repo.EnableInstance(ctx, Session{SessionID: other, InstanceID: "i1", DSLHash: testHash, ResolvedJSON: spotResolved, CreatedAt: testNow}, "name: demo"); err == nil || !strings.Contains(err.Error(), "已处于启用状态") {
		t.Fatalf("已启用的实例不应再启用：%v", err)
	}
	if countSessions() != 1 {
		t.Fatal("重复启用不应多写会话")
	}
	if err := repo.MarkInstanceDegraded(ctx, "i1", session, testNow); err != nil {
		t.Fatal(err)
	}
	if err := repo.DisableInstance(ctx, "i1", other, testNow); err == nil || !strings.Contains(err.Error(), "会话已变化") {
		t.Fatalf("会话不符时不应停用：%v", err)
	}
	if err := repo.DisableInstance(ctx, "i1", session, testNow); err != nil {
		t.Fatal(err)
	}
	disabled, err := repo.GetInstance(ctx, "i1")
	if err != nil || disabled.Enabled || disabled.SessionID != nil {
		t.Fatalf("停用应同时清掉实例上的会话：%+v err=%v", disabled, err)
	}
	if closed, err := repo.GetSession(ctx, session); err != nil || closed.ClosedAt == nil {
		t.Fatalf("停用应同时关闭会话：%+v err=%v", closed, err)
	}
	if err := repo.DisableInstance(ctx, "i1", session, testNow); err == nil {
		t.Fatal("已停用的实例不应再停用一次")
	}
	// 停用之后马上能改实例：没有等待释放的中间状态。
	if err := repo.UpdateInstance(ctx, Instance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", FactorTasks: map[string]string{"spot": "ftask_b"}, UpdatedAt: testNow}); err == nil || !strings.Contains(err.Error(), "不能修改") {
		t.Fatalf("因子任务在创建时写定，不应可改：%v", err)
	}
	if err := repo.UpdateInstance(ctx, Instance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", FactorTasks: map[string]string{"spot": "ftask_a"}, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	updated, err := repo.GetInstance(ctx, "i1")
	if err != nil || updated.FactorTasks["spot"] != "ftask_a" || updated.LogicalAccountID != nil || updated.Health != HealthOK {
		t.Fatalf("修改后的实例不符：%+v err=%v", updated, err)
	}
	if err := repo.EnableInstance(ctx, Session{SessionID: other, InstanceID: "i1", DSLHash: testHash, ResolvedJSON: spotResolved, CreatedAt: testNow}, "name: demo"); err != nil {
		t.Fatal(err)
	}
	if reopened, err := repo.GetSession(ctx, other); err != nil || reopened.LogicalAccountID != nil || reopened.StartedAt <= opened.StartedAt {
		t.Fatalf("新会话记下改后的账户（观察实例为空），开始时刻更晚：%+v err=%v", reopened, err)
	}
	// 表约束兜底：c_session_id 不能为空串，且停用（c_session_id 为空）时不能保留确认时间。
	if err := repo.db.Exec(`UPDATE t_strategy_instances SET c_session_id = '' WHERE c_instance_id = 'i1'`).Error; err == nil {
		t.Fatal("会话 ID 不应允许空串")
	}
	if err := repo.db.Exec(`UPDATE t_strategy_instances SET c_session_id = NULL WHERE c_instance_id = 'i1'`).Error; err == nil {
		t.Fatal("停用的实例不应保留确认时间")
	}
}

// 最近一次挂过账户的会话：最近一次会话没挂账户时取更早挂过的那次；列表按开始时刻从新到旧（时钟不动也不乱序）。
func TestLatestAccountSessionSkipsObserverSessions(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	if err := repo.CreateDefinition(ctx, Definition{StrategyID: "s1", Name: "demo", DSLYaml: "name: demo", DSLHash: testHash, CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateInstance(ctx, Instance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", FactorTasks: map[string]string{"spot": "ftask_a"}, CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := repo.LatestAccountSession(ctx, "i1"); err != nil || found {
		t.Fatalf("没有会话时不应找到：found=%v err=%v", found, err)
	}
	cycle := func(sessionID string, account *string) {
		t.Helper()
		if err := repo.UpdateInstance(ctx, Instance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", FactorTasks: map[string]string{"spot": "ftask_a"}, LogicalAccountID: account, UpdatedAt: testNow}); err != nil {
			t.Fatal(err)
		}
		if err := repo.EnableInstance(ctx, Session{SessionID: sessionID, InstanceID: "i1", DSLHash: testHash, ResolvedJSON: spotResolved, CreatedAt: testNow}, "name: demo"); err != nil {
			t.Fatal(err)
		}
		if err := repo.DisableInstance(ctx, "i1", sessionID, testNow); err != nil {
			t.Fatal(err)
		}
	}
	cycle("s-a", ptr("acct-a"))
	cycle("s-observer", nil)
	latest, found, err := repo.LatestAccountSession(ctx, "i1")
	if err != nil || !found || latest.SessionID != "s-a" || *latest.LogicalAccountID != "acct-a" {
		t.Fatalf("最近一次会话没挂账户时应取更早挂过的那次：%+v found=%v err=%v", latest, found, err)
	}
	cycle("s-b", ptr("acct-b"))
	if latest, _, _ := repo.LatestAccountSession(ctx, "i1"); latest.SessionID != "s-b" || *latest.LogicalAccountID != "acct-b" {
		t.Fatalf("应取最近一次挂过账户的会话：%+v", latest)
	}
	sessions, err := repo.ListSessions(ctx, "i1")
	if err != nil || len(sessions) != 3 || sessions[0].SessionID != "s-b" || sessions[1].SessionID != "s-observer" || sessions[2].SessionID != "s-a" {
		t.Fatalf("会话应按开始时刻从新到旧：%+v err=%v", sessions, err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	session, err := repo.GetSession(ctx, "session-1")
	if err != nil || session.InstanceID != "i1" || session.DSLHash != testHash || session.ClosedAt != nil || session.ResolvedJSON != spotResolved {
		t.Fatalf("会话不符：%+v err=%v", session, err)
	}
	if version, err := repo.GetDefinitionVersion(ctx, testHash); err != nil || version.DSLYaml != "name: demo" {
		t.Fatalf("会话应登记 DSL 版本：%+v err=%v", version, err)
	}
	if err := repo.DisableInstance(ctx, "i1", "session-1", testNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	closed, err := repo.GetSession(ctx, "session-1")
	if err != nil || closed.ClosedAt == nil || !closed.ClosedAt.Equal(testNow.Add(time.Hour)) {
		t.Fatalf("会话应已关闭：%+v err=%v", closed, err)
	}
	if err := repo.EnableInstance(ctx, Session{SessionID: "session-2", InstanceID: "i1", DSLHash: "unknown", ResolvedJSON: `{}`, CreatedAt: testNow}, ""); err == nil {
		t.Fatal("缺少 DSL 文本的会话应被拒绝")
	}
}

// 健康状态：degraded 只从 ok 标记、由 ok 周期恢复。
func TestInstanceHealthTransitions(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	health := func() string {
		instance, err := repo.GetInstance(ctx, "i1")
		if err != nil {
			t.Fatal(err)
		}
		return instance.Health
	}
	if err := repo.MarkInstanceDegraded(ctx, "i1", "session-1", testNow); err != nil || health() != HealthDegraded {
		t.Fatalf("应标记 degraded：%s err=%v", health(), err)
	}
	if err := repo.RecoverInstanceHealth(ctx, "i1", "session-1", testNow); err != nil || health() != HealthOK {
		t.Fatalf("应恢复 ok：%s err=%v", health(), err)
	}
	// 别的会话的结论不改健康状态。
	if err := repo.MarkInstanceDegraded(ctx, "i1", "session-other", testNow); err != nil || health() != HealthOK {
		t.Fatalf("别的会话不应标记 degraded：%s err=%v", health(), err)
	}
}

// DisableInstance 在同一事务里停用实例并关闭会话。
func TestDisableInstanceClosesSessionAtomically(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	session := "session-1"
	if err := repo.DisableInstance(ctx, "i1", session, testNow); err != nil {
		t.Fatal(err)
	}
	instance, _ := repo.GetInstance(ctx, "i1")
	closed, _ := repo.GetSession(ctx, session)
	if instance.Enabled || instance.SessionID != nil || closed.ClosedAt == nil {
		t.Fatalf("应停用、清空并关闭会话：%+v %+v", instance, closed)
	}
}

// 实例可以绑定现货、永续两个因子任务；一个任务可以被多个实例登记（独占关联记在 Factor 侧，只给调参的实例）；
// 事件按任意一个任务路由到启用的实例。
func TestInstanceBindsTwoFactorTasks(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	if err := repo.CreateDefinition(ctx, Definition{StrategyID: "s1", Name: "demo", DSLYaml: "name: demo", DSLHash: testHash, CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	dual := Instance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", FactorTasks: map[string]string{"spot": "ftask_spot", "swap": "ftask_swap"}, CreatedAt: testNow}
	if err := repo.CreateInstance(ctx, dual); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetInstance(ctx, "i1")
	if err != nil || len(got.FactorTasks) != 2 || got.FactorTasks["spot"] != "ftask_spot" || got.FactorTasks["swap"] != "ftask_swap" {
		t.Fatalf("实例应记下两个任务：%+v err=%v", got, err)
	}
	shared := Instance{InstanceID: "i2", StrategyID: "s1", SpaceID: "space", FactorTasks: map[string]string{"swap": "ftask_swap"}, CreatedAt: testNow}
	if err := repo.CreateInstance(ctx, shared); err != nil {
		t.Fatalf("另一个实例也可以登记同一个任务：%v", err)
	}
	if ids, err := repo.InstanceIDsByTask(ctx, "ftask_swap"); err != nil || strings.Join(ids, ",") != "i1,i2" {
		t.Fatalf("引用 ftask_swap 的实例不符：%v err=%v", ids, err)
	}
	if ids, err := repo.InstanceIDsByTask(ctx, "ftask_spot"); err != nil || strings.Join(ids, ",") != "i1" {
		t.Fatalf("引用 ftask_spot 的实例不符：%v err=%v", ids, err)
	}
	for _, bad := range []map[string]string{nil, {"spot": "a", "swap": "b", "equity": "c"}, {"futures": "a"}, {"spot": " "}} {
		if err := repo.CreateInstance(ctx, Instance{InstanceID: "i3", StrategyID: "s1", SpaceID: "space", FactorTasks: bad, CreatedAt: testNow}); err == nil {
			t.Fatalf("非法的因子任务绑定应被拒绝：%v", bad)
		}
	}
	// 修改实例：任务集合必须不变。
	changed := dual
	changed.UpdatedAt = testNow
	changed.FactorTasks = map[string]string{"spot": "ftask_spot"}
	if err := repo.UpdateInstance(ctx, changed); err == nil {
		t.Fatal("实例的因子任务创建时写定，不能修改")
	}
	changed.FactorTasks = dual.FactorTasks
	if err := repo.UpdateInstance(ctx, changed); err != nil {
		t.Fatal(err)
	}
	session := "session-1"
	if err := repo.EnableInstance(ctx, Session{SessionID: session, InstanceID: "i1", DSLHash: testHash, ResolvedJSON: `{"market":{"select":"both","prefer":"spot"},"tasks":{"spot":{"dataset_id":"dataset_factor_spot","factor_task_id":"ftask_spot"},"swap":{"dataset_id":"dataset_factor_swap","factor_task_id":"ftask_swap"}}}`, CreatedAt: testNow}, "name: demo"); err != nil {
		t.Fatal(err)
	}
	for _, view := range []string{"dataset_factor_spot", "dataset_factor_swap"} {
		instances, err := repo.EnabledInstancesByDataset(ctx, "space", view)
		if err != nil || len(instances) != 1 || instances[0].InstanceID != "i1" || len(instances[0].FactorTasks) != 2 {
			t.Fatalf("View %s 的事件应路由到实例：%+v err=%v", view, instances, err)
		}
	}
}

// 任务键的市场只能是现货、永续、股票：multi 等未知市场在登记时拒绝。
func TestInstanceRejectsUnknownTaskMarket(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	if err := repo.CreateDefinition(ctx, Definition{StrategyID: "s1", Name: "demo", DSLYaml: "name: demo", DSLHash: testHash, CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	err := repo.CreateInstance(ctx, Instance{InstanceID: "iu", StrategyID: "s1", SpaceID: "space", FactorTasks: map[string]string{"multi": "ftask_unified"}, CreatedAt: testNow})
	if err == nil || !strings.Contains(err.Error(), "不受支持") {
		t.Fatalf("multi 应被拒绝：%v", err)
	}
}
