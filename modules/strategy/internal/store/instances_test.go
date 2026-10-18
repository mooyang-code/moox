package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestInstanceRoundTripAndEnabledAccountUniqueness(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	if err := repo.CreateDefinition(ctx, Definition{StrategyID: "s1", Name: "demo", DSLYaml: "name: demo", DSLHash: testHash, CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	account := "acct-1"
	first := Instance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", ViewID: "view_a", LogicalAccountID: &account, CreatedAt: testNow}
	second := first
	second.InstanceID = "i2"
	for _, instance := range []Instance{first, second} {
		if err := repo.CreateInstance(ctx, instance); err != nil {
			t.Fatal(err)
		}
	}
	got, err := repo.GetInstance(ctx, "i1")
	if err != nil || got.Enabled || got.SessionID != nil || got.ViewID != "view_a" || got.Health != HealthOK || string(got.ResolvedJSON) != "{}" || *got.LogicalAccountID != account {
		t.Fatalf("实例不符：%+v err=%v", got, err)
	}
	session, secondSession := "session-1", "session-2"
	for _, opened := range []Session{{SessionID: session, InstanceID: "i1"}, {SessionID: secondSession, InstanceID: "i2"}} {
		opened.DSLHash, opened.ResolvedJSON, opened.CreatedAt = testHash, `{"view_id":"view_a"}`, testNow
		if err := repo.OpenSession(ctx, opened, "name: demo"); err != nil {
			t.Fatal(err)
		}
	}
	attachAndEnable(t, repo, "i1", session, json.RawMessage(`{"view_id":"view_a"}`))
	if err := repo.SetInstanceEnabled(ctx, "i2", false, &secondSession, nil, testNow); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetInstanceEnabled(ctx, "i2", true, &secondSession, nil, testNow); err == nil {
		t.Fatal("同一账户不能被两个启用实例占用")
	}
	enabled, err := repo.GetInstance(ctx, "i1")
	if err != nil || !enabled.Enabled || *enabled.SessionID != session || string(enabled.ResolvedJSON) != `{"view_id":"view_a"}` {
		t.Fatalf("启用后的实例不符：%+v err=%v", enabled, err)
	}
	byView, err := repo.EnabledInstancesByView(ctx, "space", "view_a")
	if err != nil || len(byView) != 1 || byView[0].InstanceID != "i1" {
		t.Fatalf("按 View 路由不符：%+v err=%v", byView, err)
	}
	enabledOnly := true
	listed, err := repo.ListInstances(ctx, "space", &enabledOnly)
	if err != nil || len(listed) != 1 {
		t.Fatalf("启用列表不符：%+v err=%v", listed, err)
	}
}

func TestSetInstanceEnabledHandshakeSemantics(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	if err := repo.CreateDefinition(ctx, Definition{StrategyID: "s1", Name: "demo", DSLYaml: "name: demo", DSLHash: testHash, CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	account := "acct-1"
	if err := repo.CreateInstance(ctx, Instance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", ViewID: "view_a", LogicalAccountID: &account, CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	session := "session-1"
	if err := repo.OpenSession(ctx, Session{SessionID: session, InstanceID: "i1", DSLHash: testHash, ResolvedJSON: `{"view_id":"view_a"}`, CreatedAt: testNow}, "name: demo"); err != nil {
		t.Fatal(err)
	}
	// 启用前必须先把会话写到实例上（启用流程先写会话再联系 Trade）。
	if err := repo.SetInstanceEnabled(ctx, "i1", true, &session, nil, testNow); err == nil {
		t.Fatal("会话尚未挂到实例上时不应能启用")
	}
	// 启用进行中：停用状态 + 会话。
	if err := repo.SetInstanceEnabled(ctx, "i1", false, &session, nil, testNow); err != nil {
		t.Fatal(err)
	}
	other := "session-2"
	if err := repo.SetInstanceEnabled(ctx, "i1", true, &other, nil, testNow); err == nil {
		t.Fatal("带有其他会话的实例不应能用新会话启用")
	}
	if err := repo.SetInstanceEnabled(ctx, "i1", true, &session, nil, testNow); err != nil {
		t.Fatal(err)
	}
	// 重复启用同一会话幂等。
	if err := repo.SetInstanceEnabled(ctx, "i1", true, &session, nil, testNow); err != nil {
		t.Fatalf("重复启用应幂等：%v", err)
	}
	if err := repo.SetInstanceEnabled(ctx, "i1", true, &other, nil, testNow); err == nil {
		t.Fatal("已启用实例不应接受另一个会话")
	}
	if err := repo.SetInstanceEnabled(ctx, "i1", false, &session, nil, testNow); err == nil {
		t.Fatal("启用中的实例不应再挂会话")
	}
	// 带会话停用：保留会话直到 Trade 确认释放。
	if err := repo.DisableInstance(ctx, "i1", &session, &session, "", testNow); err != nil {
		t.Fatal(err)
	}
	pending, err := repo.GetInstance(ctx, "i1")
	if err != nil || pending.Enabled || pending.SessionID == nil {
		t.Fatalf("停用中应保留会话：%+v err=%v", pending, err)
	}
	if err := repo.UpdateInstance(ctx, Instance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", ViewID: "view_b", UpdatedAt: testNow}); err == nil {
		t.Fatal("停用操作未完成时不应可修改")
	}
	if err := repo.SoftDeleteInstance(ctx, "i1", testNow); err == nil {
		t.Fatal("停用操作未完成时不应可删除")
	}
	if err := repo.ClearInstanceSession(ctx, "i1", "wrong", testNow); !errors.Is(err, ErrNotFound) {
		t.Fatalf("会话不匹配时不应清除：%v", err)
	}
	if err := repo.ClearInstanceSession(ctx, "i1", session, testNow); err != nil {
		t.Fatal(err)
	}
	cleared, err := repo.GetInstance(ctx, "i1")
	if err != nil || cleared.SessionID != nil {
		t.Fatalf("会话应已清空：%+v err=%v", cleared, err)
	}
	if err := repo.UpdateInstance(ctx, Instance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", ViewID: "view_b", UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	updated, err := repo.GetInstance(ctx, "i1")
	if err != nil || updated.ViewID != "view_b" || updated.LogicalAccountID != nil {
		t.Fatalf("修改后的实例不符：%+v err=%v", updated, err)
	}
	if err := repo.SetInstanceHealth(ctx, "i1", HealthDegraded, testNow); err != nil {
		t.Fatal(err)
	}
	if degraded, err := repo.GetInstance(ctx, "i1"); err != nil || degraded.Health != HealthDegraded {
		t.Fatalf("健康状态应为 degraded：%+v err=%v", degraded, err)
	}
	if err := repo.OpenSession(ctx, Session{SessionID: other, InstanceID: "i1", DSLHash: testHash, ResolvedJSON: `{"view_id":"view_b"}`, CreatedAt: testNow}, "name: demo"); err != nil {
		t.Fatal(err)
	}
	attachAndEnable(t, repo, "i1", other, nil)
	if healthy, err := repo.GetInstance(ctx, "i1"); err != nil || healthy.Health != HealthOK {
		t.Fatalf("启用应重置健康状态：%+v err=%v", healthy, err)
	}
}

// S25（实例部分）：有会话历史的已停用实例可以软删除；列表与路由不再出现；Get 仍可读。
func TestSoftDeleteInstanceKeepsHistory(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	if err := repo.SoftDeleteInstance(ctx, "i1", testNow); err == nil || !strings.Contains(err.Error(), "停用") {
		t.Fatalf("启用中的实例不应可删除：%v", err)
	}
	if err := repo.DisableInstance(ctx, "i1", ptr("session-1"), nil, "", testNow); err != nil {
		t.Fatal(err)
	}
	if err := repo.SoftDeleteInstance(ctx, "i1", testNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	deleted, err := repo.GetInstance(ctx, "i1")
	if err != nil || deleted.DeletedAt == nil {
		t.Fatalf("已删除实例仍应可读：%+v err=%v", deleted, err)
	}
	if listed, err := repo.ListInstances(ctx, "space", nil); err != nil || len(listed) != 0 {
		t.Fatalf("列表不应出现已删除实例：%+v err=%v", listed, err)
	}
	if routed, err := repo.EnabledInstancesByView(ctx, "space", "view_a"); err != nil || len(routed) != 0 {
		t.Fatalf("路由不应出现已删除实例：%+v err=%v", routed, err)
	}
	sessions, err := repo.ListSessions(ctx, "i1")
	if err != nil || len(sessions) != 1 || sessions[0].SessionID != "session-1" {
		t.Fatalf("会话应保留：%+v err=%v", sessions, err)
	}
	session := "session-2"
	if err := repo.SetInstanceEnabled(ctx, "i1", true, &session, nil, testNow); !errors.Is(err, ErrNotFound) {
		t.Fatalf("已删除实例不应可启用：%v", err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	session, err := repo.GetSession(ctx, "session-1")
	if err != nil || session.InstanceID != "i1" || session.DSLHash != testHash || session.ClosedAt != nil || session.ResolvedJSON != `{"view_id":"view_a"}` {
		t.Fatalf("会话不符：%+v err=%v", session, err)
	}
	if version, err := repo.GetDefinitionVersion(ctx, testHash); err != nil || version.DSLYaml != "name: demo" {
		t.Fatalf("会话应登记 DSL 版本：%+v err=%v", version, err)
	}
	if err := repo.CloseSession(ctx, "session-1", testNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	closed, err := repo.GetSession(ctx, "session-1")
	if err != nil || closed.ClosedAt == nil || !closed.ClosedAt.Equal(testNow.Add(time.Hour)) {
		t.Fatalf("会话应已关闭：%+v err=%v", closed, err)
	}
	if err := repo.OpenSession(ctx, Session{SessionID: "session-2", InstanceID: "i1", DSLHash: "unknown", ResolvedJSON: `{}`, CreatedAt: testNow}, ""); err == nil {
		t.Fatal("缺少 DSL 文本的会话应被拒绝")
	}
}

// 健康状态：degraded 只从 ok 标记、由 ok 周期恢复；session_unverified 不被这两者改变。
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
	if err := repo.SetInstanceHealth(ctx, "i1", HealthSessionUnverified, testNow); err != nil {
		t.Fatal(err)
	}
	_ = repo.MarkInstanceDegraded(ctx, "i1", "session-1", testNow)
	_ = repo.RecoverInstanceHealth(ctx, "i1", "session-1", testNow)
	if health() != HealthSessionUnverified {
		t.Fatalf("session_unverified 不应被求值类状态改变：%s", health())
	}
}

// DisableInstance 在同一事务里停用实例并关闭会话。
func TestDisableInstanceClosesSessionAtomically(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	session := "session-1"
	if err := repo.DisableInstance(ctx, "i1", &session, nil, "", testNow); err != nil {
		t.Fatal(err)
	}
	instance, _ := repo.GetInstance(ctx, "i1")
	closed, _ := repo.GetSession(ctx, session)
	if instance.Enabled || instance.SessionID != nil || closed.ClosedAt == nil {
		t.Fatalf("应停用、清空并关闭会话：%+v %+v", instance, closed)
	}
}
