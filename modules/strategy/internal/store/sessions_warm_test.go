package store

import (
	"context"
	"testing"
	"time"
)

const poolMembersJSON = `[{"id":"a","strategy_id":"s_a","dsl_hash":"sha256:a","dsl_yaml":"name: a","resolved":{}}]`

// 池会话带成员快照，预热结果只写一次，会话关闭后不再写；还没预热的启用池会话在启动时被找出来。
func TestSessionWarmUpResultIsWrittenOnce(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "plain", "session-plain", nil)
	if err := repo.CreateInstance(ctx, Instance{InstanceID: "pool", StrategyID: "s1", SpaceID: "space", FactorTasks: map[string]string{"spot": "ftask_pool"}, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	resolved := `{"market":{"select":"spot","prefer":"spot"},"tasks":{"spot":{"view_id":"view_factor_pool","factor_task_id":"ftask_pool"}}}`
	if err := repo.EnableInstance(ctx, Session{SessionID: "session-pool", InstanceID: "pool", DSLHash: testHash, ResolvedJSON: resolved, MembersJSON: poolMembersJSON, CreatedAt: testNow}, "name: demo"); err != nil {
		t.Fatal(err)
	}
	session, err := repo.GetSession(ctx, "session-pool")
	if err != nil || session.MembersJSON != poolMembersJSON || session.WarmedThrough != nil {
		t.Fatalf("预热前的池会话不符：%+v %v", session, err)
	}
	pending, err := repo.PendingWarmSessions(ctx)
	if err != nil || len(pending) != 1 || pending[0].SessionID != "session-pool" {
		t.Fatalf("只有启用的池会话需要预热（非池会话没有成员快照）：%+v %v", pending, err)
	}
	through := testNow.Add(5 * time.Hour)
	written, err := repo.SetSessionWarmed(ctx, "session-pool", `{"targets":{"A|spot":"0.5"}}`, through, testNow)
	if err != nil || !written {
		t.Fatalf("第一次写入应成功：%v %v", written, err)
	}
	if again, err := repo.SetSessionWarmed(ctx, "session-pool", `{}`, through.Add(time.Hour), testNow); err != nil || again {
		t.Fatalf("已有预热结果时不再写入（比较交换）：%v %v", again, err)
	}
	session, err = repo.GetSession(ctx, "session-pool")
	if err != nil || session.WarmedThrough == nil || !session.WarmedThrough.Equal(through) {
		t.Fatalf("预热结果不符：%+v %v", session, err)
	}
	// 预热出的状态与预热终点同事务写进实例的状态行；比较交换失败的那次写入没有动它。
	state, found, err := repo.GetState(ctx, "pool")
	if err != nil || !found || state.SessionID != "session-pool" || state.DSLHash != testHash || !state.BarEndTime.Equal(through) || string(state.StateJSON) != `{"targets":{"A|spot":"0.5"}}` {
		t.Fatalf("预热出的状态不符：%+v found=%v err=%v", state, found, err)
	}
	if pending, _ := repo.PendingWarmSessions(ctx); len(pending) != 0 {
		t.Fatalf("预热完成后不再待预热：%+v", pending)
	}
	if err := repo.DisableInstance(ctx, "plain", "session-plain", testNow); err != nil {
		t.Fatal(err)
	}
	if written, err := repo.SetSessionWarmed(ctx, "session-plain", `{}`, through, testNow); err != nil || written {
		t.Fatalf("会话已关闭或没有成员快照时不写入：%v %v", written, err)
	}
	if _, found, _ := repo.GetState(ctx, "plain"); found {
		t.Fatal("没有写入预热结果时不应留下状态行")
	}
	// 成员快照必须是非空数组。
	if err := repo.EnableInstance(ctx, Session{SessionID: "bad", InstanceID: "pool", DSLHash: testHash, ResolvedJSON: resolved, MembersJSON: `{}`, CreatedAt: testNow}, "name: demo"); err == nil {
		t.Fatal("members_json 不是数组应被拒绝")
	}
	if _, err := repo.SetSessionWarmed(ctx, "session-pool", "", through, testNow); err == nil {
		t.Fatal("预热结果缺状态应被拒绝")
	}
}
