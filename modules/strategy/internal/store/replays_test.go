package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func newReplay(id string, created time.Time) Replay {
	return Replay{ReplayID: id, DSLYaml: "name: demo", SpaceID: "space", ViewID: "view_a", StartTime: testNow, EndTime: testNow.Add(24 * time.Hour), FeeBps: 10, CreatedAt: created}
}

func TestReplayLifecycle(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	if err := repo.CreateReplay(ctx, newReplay("p1", testNow)); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateReplay(ctx, newReplay("p2", testNow.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetReplay(ctx, "p1")
	if err != nil || got.Status != ReplayPending || got.FeeBps != 10 || got.ProgressTime != nil || string(got.MetricsJSON) != "{}" {
		t.Fatalf("回放不符：%+v err=%v", got, err)
	}
	claimed, found, err := repo.ClaimNextReplay(ctx, testNow)
	if err != nil || !found || claimed.ReplayID != "p1" || claimed.Status != ReplayRunning {
		t.Fatalf("应认领最早的 pending 回放：%+v found=%v err=%v", claimed, found, err)
	}
	if err := repo.UpdateReplayProgress(ctx, "p1", testNow.Add(time.Hour), testNow); err != nil {
		t.Fatal(err)
	}
	if err := repo.AppendReplayBar(ctx, ReplayBar{ReplayID: "p1", BarEndTime: testNow.Add(time.Hour), Status: "ok", TargetsJSON: json.RawMessage(`[]`), PositionsJSON: json.RawMessage(`{}`), SummaryJSON: json.RawMessage(`{}`), Equity: 100}); err != nil {
		t.Fatal(err)
	}
	if err := repo.FinishReplay(ctx, "p1", ReplayDone, json.RawMessage(`{"total_return":0.1}`), "", testNow); err != nil {
		t.Fatal(err)
	}
	done, err := repo.GetReplay(ctx, "p1")
	if err != nil || done.Status != ReplayDone || string(done.MetricsJSON) != `{"total_return":0.1}` || done.ProgressTime == nil {
		t.Fatalf("完成状态不符：%+v err=%v", done, err)
	}
	bars, total, err := repo.ListReplayBars(ctx, "p1", 0, 10)
	if err != nil || total != 1 || len(bars) != 1 || bars[0].Equity != 100 {
		t.Fatalf("回放周期不符：%+v total=%d err=%v", bars, total, err)
	}
	if err := repo.CancelReplay(ctx, "p2", testNow); err != nil {
		t.Fatal(err)
	}
	if status, err := repo.ReplayStatus(ctx, "p2"); err != nil || status != ReplayCancelled {
		t.Fatalf("取消状态不符：%s err=%v", status, err)
	}
	if err := repo.CancelReplay(ctx, "p2", testNow); !errors.Is(err, ErrNotFound) {
		t.Fatalf("已取消的回放不应再次取消：%v", err)
	}
	listed, total, err := repo.ListReplays(ctx, "space", 0, 10)
	if err != nil || total != 2 || len(listed) != 2 || listed[0].ReplayID != "p2" {
		t.Fatalf("列表不符：%+v total=%d err=%v", listed, total, err)
	}
	if _, found, err := repo.ClaimNextReplay(ctx, testNow); err != nil || found {
		t.Fatalf("没有 pending 回放时不应认领：found=%v err=%v", found, err)
	}
}

// S24（存储部分）：进程重启把遗留的 running 回放标记为 failed(interrupted)，已写入的 bars 保留。
func TestMarkRunningReplaysInterruptedKeepsBars(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	if err := repo.CreateReplay(ctx, newReplay("p1", testNow)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.ClaimNextReplay(ctx, testNow); err != nil {
		t.Fatal(err)
	}
	if err := repo.AppendReplayBar(ctx, ReplayBar{ReplayID: "p1", BarEndTime: testNow.Add(time.Hour), Status: "ok", TargetsJSON: json.RawMessage(`[]`), PositionsJSON: json.RawMessage(`{}`), SummaryJSON: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	affected, err := repo.MarkRunningReplaysInterrupted(ctx, testNow.Add(time.Hour))
	if err != nil || affected != 1 {
		t.Fatalf("应标记 1 个回放：affected=%d err=%v", affected, err)
	}
	interrupted, err := repo.GetReplay(ctx, "p1")
	if err != nil || interrupted.Status != ReplayFailed || interrupted.Error != "interrupted" {
		t.Fatalf("状态不符：%+v err=%v", interrupted, err)
	}
	if _, total, err := repo.ListReplayBars(ctx, "p1", 0, 10); err != nil || total != 1 {
		t.Fatalf("已写入的周期应保留：total=%d err=%v", total, err)
	}
}

func TestRetentionDeletesOldItemsAndReplays(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	mustCommit(t, repo, okResult("r1", "i1", "session-1", 1, PublishNone), []ResultItem{{RuleID: "r", InstrumentID: "BTC-USDT", Stage: "weighted"}}, testNow)
	mustCommit(t, repo, okResult("r2", "i1", "session-1", 2, PublishNone), []ResultItem{{RuleID: "r", InstrumentID: "BTC-USDT", Stage: "weighted"}}, testNow)
	deleted, err := repo.DeleteResultItemsBefore(ctx, testNow.Add(90*time.Minute))
	if err != nil || deleted != 1 {
		t.Fatalf("应删除 1 条明细：deleted=%d err=%v", deleted, err)
	}
	if items, err := repo.ListResultItems(ctx, "r1"); err != nil || len(items) != 0 {
		t.Fatalf("旧结果的明细应被清理：%+v err=%v", items, err)
	}
	if got, err := repo.GetResult(ctx, "r1"); err != nil || got.ResultID != "r1" {
		t.Fatalf("结果主表应保留：%+v err=%v", got, err)
	}
	if err := repo.CreateReplay(ctx, newReplay("p1", testNow)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.ClaimNextReplay(ctx, testNow); err != nil {
		t.Fatal(err)
	}
	if removed, err := repo.DeleteReplaysBefore(ctx, testNow.Add(time.Hour)); err != nil || removed != 0 {
		t.Fatalf("运行中的回放不应被清理：removed=%d err=%v", removed, err)
	}
	if err := repo.FinishReplay(ctx, "p1", ReplayFailed, nil, "boom", testNow); err != nil {
		t.Fatal(err)
	}
	if removed, err := repo.DeleteReplaysBefore(ctx, testNow.Add(time.Hour)); err != nil || removed != 1 {
		t.Fatalf("已结束的旧回放应被清理：removed=%d err=%v", removed, err)
	}
}

// 明细按自身的 c_ctime 分批删除，直到没有过期行。
func TestRetentionDeletesItemsInBatches(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	items := []ResultItem{
		{RuleID: "r", InstrumentID: "A", Stage: "weighted"}, {RuleID: "r", InstrumentID: "B", Stage: "scored"},
		{RuleID: "r", InstrumentID: "C", Stage: "scored"}, {RuleID: "r", InstrumentID: "D", Stage: "missing"}, {RuleID: "r", InstrumentID: "E", Stage: "filtered"},
	}
	mustCommit(t, repo, okResult("r1", "i1", "session-1", 1, PublishNone), items, testNow)
	mustCommit(t, repo, okResult("r2", "i1", "session-1", 3, PublishNone), items[:1], testNow)
	previous := retentionBatch
	retentionBatch = 2
	t.Cleanup(func() { retentionBatch = previous })
	deleted, err := repo.DeleteResultItemsBefore(ctx, testNow.Add(2*time.Hour))
	if err != nil || deleted != 5 {
		t.Fatalf("应分批删除 5 条明细：deleted=%d err=%v", deleted, err)
	}
	if kept, err := repo.ListResultItems(ctx, "r2"); err != nil || len(kept) != 1 {
		t.Fatalf("未过期的明细应保留：%+v err=%v", kept, err)
	}
}

// 过期回放逐个分批删除周期记录后再删任务本身。
func TestRetentionDeletesReplaysInBatches(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	if err := repo.CreateReplay(ctx, newReplay("p1", testNow)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.ClaimNextReplay(ctx, testNow); err != nil {
		t.Fatal(err)
	}
	for bar := 1; bar <= 5; bar++ {
		if err := repo.AppendReplayBar(ctx, ReplayBar{ReplayID: "p1", BarEndTime: testNow.Add(time.Duration(bar) * time.Hour), Status: "ok", TargetsJSON: []byte(`[]`), PositionsJSON: []byte(`{}`), SummaryJSON: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.FinishReplay(ctx, "p1", ReplayDone, nil, "", testNow); err != nil {
		t.Fatal(err)
	}
	previous := retentionBatch
	retentionBatch = 2
	t.Cleanup(func() { retentionBatch = previous })
	deleted, err := repo.DeleteReplaysBefore(ctx, testNow.Add(time.Hour))
	if err != nil || deleted != 1 {
		t.Fatalf("应删除 1 个回放：deleted=%d err=%v", deleted, err)
	}
	if _, total, err := repo.ListReplayBars(ctx, "p1", 0, 10); err != nil || total != 0 {
		t.Fatalf("周期记录应全部删除：total=%d err=%v", total, err)
	}
}
