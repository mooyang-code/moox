package store

import (
	"context"
	"testing"
)

// 发布期间被更新的 ok 结果取消、随后确认已发出的结果改记 sent：取消计数只增不减，这类结果另行计数，
// 两者相减才是真正没有送到 Trade 的目标数。
func TestSentAfterCancelIsCountedSeparately(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	cancelled, sentAfterCancel := map[string]int64{}, 0
	repo.SetCancelObserver(func(reason string, count int64) { cancelled[reason] += count })
	repo.SetSentAfterCancelObserver(func() { sentAfterCancel++ })
	account := "acct-1"
	seedEnabledInstance(t, repo, "i1", "session-1", &account)
	mustCommit(t, repo, okResult("r1", "i1", "session-1", 1, PublishPending), nil, testNow)
	// r1 正在发布时下一期的 ok 结果提交，把 r1 取消。
	mustCommit(t, repo, okResult("r2", "i1", "session-1", 2, PublishPending), nil, testNow)
	if err := repo.TransitionPublishStatus(ctx, "r1", PublishCancelled, PublishSent); err != nil {
		t.Fatal(err)
	}
	if cancelled[CancelSuperseded] != 1 || sentAfterCancel != 1 {
		t.Fatalf("取消计数不回退，取消后发出另计一次：cancelled=%v sentAfterCancel=%d", cancelled, sentAfterCancel)
	}
	if err := repo.TransitionPublishStatus(ctx, "r2", PublishPending, PublishSent); err != nil || sentAfterCancel != 1 {
		t.Fatalf("正常发出不应计入取消后发出：%d err=%v", sentAfterCancel, err)
	}
}
