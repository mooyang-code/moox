package store

import (
	"context"
	"testing"
	"time"
)

// 会话开始时刻（批 3 T14）：取“当前毫秒”与“库里最大值加 1”中较大的一个，全库严格递增——同一毫秒、甚至时钟回拨时建的会话也比之前的晚；
// 会话同时记下建会话时实例绑的组合账户，之后改账户不影响它（停用后通知 Trade 关闭会话按它调）。
func TestOpenSessionStartsStrictlyLaterAndRecordsAccount(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	account := "logical-1"
	seedEnabledInstance(t, repo, "i1", "session-1", &account)
	first, err := repo.GetSession(ctx, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if first.StartedAt != testNow.UnixMilli() {
		t.Fatalf("第一个会话的开始时刻 = %d，期望取建会话时刻 %d", first.StartedAt, testNow.UnixMilli())
	}
	if first.LogicalAccountID == nil || *first.LogicalAccountID != account {
		t.Fatalf("会话记下的组合账户 = %v，期望 %s", first.LogicalAccountID, account)
	}

	if err := repo.CreateInstance(ctx, Instance{InstanceID: "observer", StrategyID: "s1", SpaceID: "space", FactorTasks: map[string]string{"spot": "ftask_o"}, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	opened := []string{"session-1"}
	// 同一毫秒、时钟回拨一小时、正常往后：开始时刻都严格递增。
	for index, createdAt := range []time.Time{testNow, testNow.Add(-time.Hour), testNow.Add(time.Hour)} {
		sessionID := []string{"session-same-ms", "session-clock-back", "session-later"}[index]
		if err := repo.OpenSession(ctx, Session{SessionID: sessionID, InstanceID: "observer", DSLHash: testHash, ResolvedJSON: `{}`, CreatedAt: createdAt}, "name: demo"); err != nil {
			t.Fatal(err)
		}
		opened = append(opened, sessionID)
	}
	var previous int64
	for _, sessionID := range opened {
		session, err := repo.GetSession(ctx, sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if session.StartedAt <= previous {
			t.Fatalf("%s 的开始时刻 %d 不晚于上一个会话的 %d", sessionID, session.StartedAt, previous)
		}
		previous = session.StartedAt
		if sessionID != "session-1" && session.LogicalAccountID != nil {
			t.Fatalf("观察实例的会话不带组合账户：%v", *session.LogicalAccountID)
		}
	}
	if later, _ := repo.GetSession(ctx, "session-later"); later.StartedAt != testNow.Add(time.Hour).UnixMilli() {
		t.Fatalf("时钟正常往后时开始时刻取当前毫秒：%d", later.StartedAt)
	}

	// 停用后改绑账户：已有会话记下的仍是原来的账户。
	if err := repo.DisableInstance(ctx, "i1", ptr("session-1"), nil, HealthOK, testNow); err != nil {
		t.Fatal(err)
	}
	instance, err := repo.GetInstance(ctx, "i1")
	if err != nil {
		t.Fatal(err)
	}
	if instance.SessionID != nil {
		if err := repo.ClearInstanceSession(ctx, "i1", *instance.SessionID, testNow); err != nil {
			t.Fatal(err)
		}
		instance, _ = repo.GetInstance(ctx, "i1")
	}
	other := "logical-2"
	instance.LogicalAccountID, instance.UpdatedAt = &other, testNow
	if err := repo.UpdateInstance(ctx, instance); err != nil {
		t.Fatal(err)
	}
	reread, err := repo.GetSession(ctx, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if reread.LogicalAccountID == nil || *reread.LogicalAccountID != account {
		t.Fatalf("改账户后会话记下的组合账户 = %v，期望仍是 %s", reread.LogicalAccountID, account)
	}
}

// 实例不存在时不写会话。
func TestOpenSessionRejectsUnknownInstance(t *testing.T) {
	repo := openTestStore(t)
	if err := repo.CreateDefinition(context.Background(), Definition{StrategyID: "s1", Name: "demo", DSLYaml: "name: demo", DSLHash: testHash, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if err := repo.OpenSession(context.Background(), Session{SessionID: "orphan", InstanceID: "missing", DSLHash: testHash, ResolvedJSON: `{}`, CreatedAt: testNow}, "name: demo"); err == nil {
		t.Fatal("实例不存在时应拒绝写会话")
	}
}
