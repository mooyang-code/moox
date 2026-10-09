package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// chineseTimeout 模拟上一层已经换成中文说明的超时错误（例如 Trade 请求超时、结果未知），其文本里没有英文原文。
type chineseTimeout struct{}

func (chineseTimeout) Error() string { return "Trade 账户释放超时，结果未知" }
func (chineseTimeout) Unwrap() error { return context.DeadlineExceeded }

// 超时已经有中文说明时原样保留，不被整句换成“请求超时，请稍后重试”而丢掉“已停用、结果未知”这些上下文。
func TestFriendlyMessageKeepsChineseTimeout(t *testing.T) {
	err := fmt.Errorf("已停用但 Trade 释放未确认，稍后自动重试：%w", chineseTimeout{})
	message, replaced := FriendlyMessage(err)
	if replaced || message != "已停用但 Trade 释放未确认，稍后自动重试：Trade 账户释放超时，结果未知" {
		t.Fatalf("应保留中文说明：%q replaced=%v", message, replaced)
	}
}

// 已经发出的事件在发布期间被更新的结果并发取消：允许 cancelled 变回 sent；其余迁移仍然拒绝。
func TestTransitionPublishStatusAllowsSentAfterConcurrentCancel(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	account := "acct-1"
	seedEnabledInstance(t, repo, "i1", "session-1", &account)
	mustCommit(t, repo, okResult("r1", "i1", "session-1", 1, PublishPending), nil, testNow)
	if err := repo.TransitionPublishStatus(ctx, "r1", PublishPending, PublishCancelled); err != nil {
		t.Fatal(err)
	}
	if err := repo.TransitionPublishStatus(ctx, "r1", PublishCancelled, PublishSent); err != nil {
		t.Fatalf("已发出的事件应能记为 sent：%v", err)
	}
	if got, _ := repo.GetResult(ctx, "r1"); got.PublishStatus != PublishSent {
		t.Fatalf("状态应为 sent：%+v", got)
	}
	if err := repo.TransitionPublishStatus(ctx, "r1", PublishSent, PublishCancelled); err == nil {
		t.Fatal("sent 不应再被取消")
	}
	if err := repo.TransitionPublishStatus(ctx, "r1", PublishCancelled, PublishPending); err == nil {
		t.Fatal("cancelled 不应回到 pending")
	}
}

// 停用核对调用方看到的会话：不能关掉或清空一个之后才挂上的会话；挂会话只作用于停用的实例。
func TestDisableInstanceComparesSession(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	if err := repo.DisableInstance(ctx, "i1", ptr("session-other"), nil, "", testNow); err == nil || !strings.Contains(err.Error(), "会话已变化") {
		t.Fatalf("会话不符时不应停用：%v", err)
	}
	if err := repo.DisableInstance(ctx, "i1", nil, nil, "", testNow); err == nil {
		t.Fatal("实例有会话时，不带会话的停用应被拒绝")
	}
	if err := repo.SetInstanceEnabled(ctx, "i1", false, ptr("session-1"), nil, testNow); err == nil || !strings.Contains(err.Error(), "已处于启用状态") {
		t.Fatalf("启用中的实例不应再挂会话：%v", err)
	}
	if err := repo.DisableInstance(ctx, "i1", ptr("session-1"), nil, "", testNow); err != nil {
		t.Fatal(err)
	}
	instance, err := repo.GetInstance(ctx, "i1")
	if err != nil || instance.Enabled || instance.SessionID != nil {
		t.Fatalf("停用后应清空会话：%+v err=%v", instance, err)
	}
	if session, err := repo.GetSession(ctx, "session-1"); err != nil || session.ClosedAt == nil {
		t.Fatalf("停用应关闭会话：%+v err=%v", session, err)
	}
	if err := repo.SetInstanceEnabled(ctx, "i1", false, nil, nil, testNow); err == nil {
		t.Fatal("挂会话必须给出会话 ID")
	}
	if !errors.Is(repo.DisableInstance(ctx, "missing", nil, nil, "", testNow), ErrNotFound) {
		t.Fatal("不存在的实例应返回 ErrNotFound")
	}
}

// 被取消、没有送到 Trade 的目标按原因计数：被更新的结果替代、过期、实例停用、永久发布错误。
func TestCancelObserverCountsReasons(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	counts := map[string]int64{}
	repo.SetCancelObserver(func(reason string, count int64) { counts[reason] += count })
	account := "acct-1"
	seedEnabledInstance(t, repo, "i1", "session-1", &account)
	mustCommit(t, repo, okResult("r1", "i1", "session-1", 1, PublishPending), nil, testNow)
	mustCommit(t, repo, okResult("r2", "i1", "session-1", 2, PublishPending), nil, testNow)
	if _, valid, err := repo.PreparePendingResult(ctx, "r2", testNow.Add(10*time.Hour)); err != nil || valid {
		t.Fatalf("过期结果应被取消：valid=%v err=%v", valid, err)
	}
	mustCommit(t, repo, okResult("r3", "i1", "session-1", 3, PublishPending), nil, testNow)
	if err := repo.TransitionPublishStatus(ctx, "r3", PublishPending, PublishCancelled); err != nil {
		t.Fatal(err)
	}
	mustCommit(t, repo, okResult("r4", "i1", "session-1", 4, PublishPending), nil, testNow)
	if err := repo.DisableInstance(ctx, "i1", ptr("session-1"), ptr("session-1"), "", testNow); err != nil {
		t.Fatal(err)
	}
	if _, valid, err := repo.PreparePendingResult(ctx, "r4", testNow); err != nil || valid {
		t.Fatalf("停用实例的结果应被取消：valid=%v err=%v", valid, err)
	}
	want := map[string]int64{CancelSuperseded: 1, CancelExpired: 1, CancelRejected: 1, CancelInactive: 1}
	for reason, count := range want {
		if counts[reason] != count {
			t.Fatalf("取消原因计数不符：%v", counts)
		}
	}
}
