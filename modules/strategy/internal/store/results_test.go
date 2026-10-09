package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func inputJSON(viewID string, factors map[string]string) json.RawMessage {
	raw, _ := json.Marshal(InputRecord{ViewID: viewID, Bar: "1h", Calendar: "crypto_24x7", BarStart: testNow, Factors: factors})
	return raw
}

func okResult(id, instanceID, sessionID string, bar int, publish PublishStatus) Result {
	barEnd := testNow.Add(time.Duration(bar) * time.Hour)
	result := Result{
		ResultID: id, InstanceID: instanceID, SessionID: sessionID, BarEndTime: barEnd, ValidUntil: barEnd.Add(2 * time.Hour),
		Status: StatusOK, DSLHash: testHash, InputJSON: inputJSON("view_a", map[string]string{"ma": "h1"}),
		TargetsJSON: json.RawMessage(`[{"instrument_id":"BTC-USDT","target_weight":"0.5"}]`), RuleStatesJSON: json.RawMessage(`{"r":{"held":["BTC-USDT"]}}`),
		SummaryJSON: json.RawMessage(`{"universe":1}`), PublishStatus: publish, CreatedAt: barEnd.Add(time.Second),
	}
	if publish == PublishPending {
		result.EventData = []byte("event-" + id)
	}
	return result
}

func skippedResult(id, instanceID, sessionID string, bar int, reason string) Result {
	barEnd := testNow.Add(time.Duration(bar) * time.Hour)
	return Result{
		ResultID: id, InstanceID: instanceID, SessionID: sessionID, BarEndTime: barEnd, ValidUntil: barEnd.Add(2 * time.Hour),
		Status: StatusSkipped, SkipReason: reason, DSLHash: testHash, InputJSON: inputJSON("view_a", map[string]string{"ma": "h1"}),
		TargetsJSON: json.RawMessage(`[]`), RuleStatesJSON: json.RawMessage(`{}`), SummaryJSON: json.RawMessage(`{}`),
		PublishStatus: PublishNone, CreatedAt: barEnd.Add(time.Second),
	}
}

func mustCommit(t *testing.T, repo *Store, result Result, items []ResultItem, now time.Time) Result {
	t.Helper()
	committed, created, err := repo.CommitResult(context.Background(), CommitRequest{Result: result, Items: items, Now: now})
	if err != nil || !created {
		t.Fatalf("提交 %s 失败：created=%v err=%v", result.ResultID, created, err)
	}
	return committed
}

func TestCommitResultStoresItemsAndTargets(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	items := []ResultItem{
		{RuleID: "r", InstrumentID: "BTC-USDT", Stage: "weighted", Score: ptr(0.75), Rank: ptr(1), Weight: ptr("0.5")},
		{RuleID: "r", InstrumentID: "ETH-USDT", Stage: "missing", Reason: "missing:ma"},
	}
	mustCommit(t, repo, okResult("r1", "i1", "session-1", 1, PublishNone), items, testNow)
	got, err := repo.GetResult(ctx, "r1")
	if err != nil || got.Status != StatusOK || string(got.TargetsJSON) != `[{"instrument_id":"BTC-USDT","target_weight":"0.5"}]` || got.PublishStatus != PublishNone {
		t.Fatalf("结果不符：%+v err=%v", got, err)
	}
	listed, err := repo.ListResultItems(ctx, "r1")
	if err != nil || len(listed) != 2 || listed[0].InstrumentID != "BTC-USDT" || *listed[0].Score != 0.75 || *listed[0].Rank != 1 || *listed[0].Weight != "0.5" || listed[1].Reason != "missing:ma" || listed[1].Score != nil {
		t.Fatalf("解释明细不符：%+v err=%v", listed, err)
	}
	// 同一周期重复提交返回已有记录。
	again, created, err := repo.CommitResult(ctx, CommitRequest{Result: okResult("r1-dup", "i1", "session-1", 1, PublishNone), Now: testNow})
	if err != nil || created || again.ResultID != "r1" {
		t.Fatalf("重复周期应返回已有记录：%+v created=%v err=%v", again, created, err)
	}
	// 大量明细分块写入。
	many := make([]ResultItem, 0, 450)
	for i := 0; i < 450; i++ {
		many = append(many, ResultItem{RuleID: "r", InstrumentID: fmt.Sprintf("I%03d", i), Stage: "scored"})
	}
	mustCommit(t, repo, okResult("r2", "i1", "session-1", 2, PublishNone), many, testNow)
	if listed, err := repo.ListResultItems(ctx, "r2"); err != nil || len(listed) != 450 {
		t.Fatalf("分块写入的明细数量不符：%d err=%v", len(listed), err)
	}
}

// S9：上期 ok 待发送、本期 skipped：ok 行仍是最新 ok 决策、仍可发送；最近处理周期是 skipped 行。
func TestS9SkippedDoesNotCancelPendingOkResult(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	account := "acct-1"
	seedEnabledInstance(t, repo, "i1", "session-1", &account)
	mustCommit(t, repo, okResult("r1", "i1", "session-1", 1, PublishPending), nil, testNow)
	mustCommit(t, repo, skippedResult("r2", "i1", "session-1", 2, "factor_missing"), nil, testNow)
	latestOK, ok, err := repo.LatestOk(ctx, "i1", "session-1")
	if err != nil || !ok || latestOK.ResultID != "r1" {
		t.Fatalf("最新 ok 决策应为 r1：%+v ok=%v err=%v", latestOK, ok, err)
	}
	processed, ok, err := repo.LatestProcessed(ctx, "i1", "session-1")
	if err != nil || !ok || processed.ResultID != "r2" {
		t.Fatalf("最近处理周期应为 r2：%+v ok=%v err=%v", processed, ok, err)
	}
	prepared, valid, err := repo.PreparePendingResult(ctx, "r1", testNow.Add(2*time.Hour))
	if err != nil || !valid || prepared.ResultID != "r1" {
		t.Fatalf("r1 应仍可投递：valid=%v err=%v", valid, err)
	}
	pending, err := repo.ListPendingResults(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0].ResultID != "r1" {
		t.Fatalf("待投递列表不符：%+v err=%v", pending, err)
	}
	stats, err := repo.PendingOutboxStats(ctx)
	if err != nil || stats.PendingCount != 1 || !stats.OldestPending.Equal(testNow.Add(time.Hour+time.Second)) {
		t.Fatalf("outbox 统计不符：%+v err=%v", stats, err)
	}
	// 新的 ok 决策才取消旧的待投递结果。
	mustCommit(t, repo, okResult("r3", "i1", "session-1", 3, PublishPending), nil, testNow)
	cancelled, err := repo.GetResult(ctx, "r1")
	if err != nil || cancelled.PublishStatus != PublishCancelled {
		t.Fatalf("旧的待投递结果应被取消：%+v err=%v", cancelled, err)
	}
	if err := repo.TransitionPublishStatus(ctx, "r3", PublishPending, PublishSent); err != nil {
		t.Fatal(err)
	}
	if err := repo.TransitionPublishStatus(ctx, "r3", PublishPending, PublishSent); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复转换应返回 ErrNotFound：%v", err)
	}
}

// S16 / S17：skipped(expired) 允许过去的 valid_until；ok 结果过期则拒绝。
func TestExpiredResultsOnlyAllowedAsSkipped(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	late := testNow.Add(48 * time.Hour)
	if _, _, err := repo.CommitResult(ctx, CommitRequest{Result: okResult("r1", "i1", "session-1", 1, PublishNone), Now: late}); !errors.Is(err, ErrResultExpired) {
		t.Fatalf("过期的 ok 结果应被拒绝：%v", err)
	}
	expired := skippedResult("r1", "i1", "session-1", 1, SkipExpired)
	if _, created, err := repo.CommitResult(ctx, CommitRequest{Result: expired, Now: late}); err != nil || !created {
		t.Fatalf("skipped(expired) 应允许过去的 valid_until：created=%v err=%v", created, err)
	}
	if _, ok, err := repo.LatestOk(ctx, "i1", "session-1"); err != nil || ok {
		t.Fatalf("skipped 不应成为 ok 决策：ok=%v err=%v", ok, err)
	}
}

// S10（存储部分）：ok 结果不能早于最新周期，乱序周期以 skipped(out_of_order) 记录。
func TestOlderOkRejectedButOutOfOrderSkipAccepted(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	mustCommit(t, repo, okResult("r2", "i1", "session-1", 2, PublishNone), nil, testNow)
	if _, _, err := repo.CommitResult(ctx, CommitRequest{Result: okResult("r1", "i1", "session-1", 1, PublishNone), Now: testNow}); !errors.Is(err, ErrResultOlder) {
		t.Fatalf("更早的 ok 结果应被拒绝：%v", err)
	}
	mustCommit(t, repo, skippedResult("r1", "i1", "session-1", 1, SkipOutOfOrder), nil, testNow)
	processed, _, err := repo.LatestProcessed(ctx, "i1", "session-1")
	if err != nil || processed.ResultID != "r2" {
		t.Fatalf("乱序记录不应改变最近处理周期：%+v err=%v", processed, err)
	}
}

func TestCommitResultCompareAndSwap(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	if _, _, err := repo.CommitResult(ctx, CommitRequest{Result: okResult("r1", "i1", "session-1", 1, PublishNone), ExpectedLatestResultID: ptr("ghost"), Now: testNow}); !errors.Is(err, ErrResultCASConflict) {
		t.Fatalf("期望记录不存在时应冲突：%v", err)
	}
	if _, created, err := repo.CommitResult(ctx, CommitRequest{Result: okResult("r1", "i1", "session-1", 1, PublishNone), ExpectedLatestResultID: ptr(""), Now: testNow}); err != nil || !created {
		t.Fatalf("期望为空且无记录时应成功：created=%v err=%v", created, err)
	}
	if _, _, err := repo.CommitResult(ctx, CommitRequest{Result: okResult("r2", "i1", "session-1", 2, PublishNone), ExpectedLatestResultID: ptr(""), Now: testNow}); !errors.Is(err, ErrResultCASConflict) {
		t.Fatalf("已有记录时期望为空应冲突：%v", err)
	}
	if _, created, err := repo.CommitResult(ctx, CommitRequest{Result: okResult("r2", "i1", "session-1", 2, PublishNone), ExpectedLatestResultID: ptr("r1"), Now: testNow}); err != nil || !created {
		t.Fatalf("期望匹配时应成功：created=%v err=%v", created, err)
	}
}

func TestCommitResultRequiresActiveInstanceAndValidPayload(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	if _, _, err := repo.CommitResult(ctx, CommitRequest{Result: okResult("r1", "i1", "other-session", 1, PublishNone), Now: testNow}); !errors.Is(err, ErrResultInstanceNotActive) {
		t.Fatalf("会话不匹配应拒绝：%v", err)
	}
	bad := okResult("r1", "i1", "session-1", 1, PublishNone)
	bad.SkipReason = "x"
	if _, _, err := repo.CommitResult(ctx, CommitRequest{Result: bad, Now: testNow}); !errors.Is(err, ErrResultInvalid) {
		t.Fatalf("带跳过原因的 ok 结果应无效：%v", err)
	}
	skippedPending := skippedResult("r1", "i1", "session-1", 1, "factor_missing")
	skippedPending.PublishStatus = PublishPending
	skippedPending.EventData = []byte("x")
	if _, _, err := repo.CommitResult(ctx, CommitRequest{Result: skippedPending, Now: testNow}); !errors.Is(err, ErrResultInvalid) {
		t.Fatalf("skipped 不能投递：%v", err)
	}
	nullTargets := okResult("r1", "i1", "session-1", 1, PublishNone)
	nullTargets.TargetsJSON = json.RawMessage(`null`)
	if _, _, err := repo.CommitResult(ctx, CommitRequest{Result: nullTargets, Now: testNow}); !errors.Is(err, ErrResultInvalid) {
		t.Fatalf("null 目标应无效：%v", err)
	}
	if err := repo.SetInstanceEnabled(ctx, "i1", false, nil, nil, testNow); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.CommitResult(ctx, CommitRequest{Result: okResult("r1", "i1", "session-1", 1, PublishNone), Now: testNow}); !errors.Is(err, ErrResultInstanceNotActive) {
		t.Fatalf("停用实例应拒绝提交：%v", err)
	}
}

func TestPreparePendingResultCancelsStaleRows(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	account := "acct-1"
	seedEnabledInstance(t, repo, "i1", "session-1", &account)
	mustCommit(t, repo, okResult("r1", "i1", "session-1", 1, PublishPending), nil, testNow)
	// 过期：取消。
	if _, valid, err := repo.PreparePendingResult(ctx, "r1", testNow.Add(10*time.Hour)); err != nil || valid {
		t.Fatalf("过期结果应被取消：valid=%v err=%v", valid, err)
	}
	if got, _ := repo.GetResult(ctx, "r1"); got.PublishStatus != PublishCancelled {
		t.Fatalf("状态应为 cancelled：%+v", got)
	}
	if _, _, err := repo.PreparePendingResult(ctx, "r1", testNow); !errors.Is(err, ErrNotFound) {
		t.Fatalf("已取消的结果不应再被准备：%v", err)
	}
	// 实例停用：取消。
	mustCommit(t, repo, okResult("r2", "i1", "session-1", 2, PublishPending), nil, testNow)
	if err := repo.SetInstanceEnabled(ctx, "i1", false, ptr("session-1"), nil, testNow); err != nil {
		t.Fatal(err)
	}
	if _, valid, err := repo.PreparePendingResult(ctx, "r2", testNow.Add(2*time.Hour)); err != nil || valid {
		t.Fatalf("停用实例的结果应被取消：valid=%v err=%v", valid, err)
	}
}

// S20 / S23：相邻记录跨会话查找，优先当前会话；改绑过 View 的旧记录不可信；缺因子指纹的记录不可信。
func TestAdjacentRecordTrustRules(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	bar1 := testNow.Add(time.Hour)
	// 旧会话在 view_a 上的记录。
	mustCommit(t, repo, okResult("old", "i1", "session-1", 1, PublishNone), nil, testNow)
	// 停用并以新会话重新启用。
	if err := repo.SetInstanceEnabled(ctx, "i1", false, nil, nil, testNow); err != nil {
		t.Fatal(err)
	}
	if err := repo.OpenSession(ctx, Session{SessionID: "session-2", InstanceID: "i1", DSLHash: testHash, ResolvedJSON: `{"view_id":"view_a"}`, CreatedAt: testNow}, "name: demo"); err != nil {
		t.Fatal(err)
	}
	attachAndEnable(t, repo, "i1", "session-2", nil)
	record, input, found, err := repo.AdjacentRecord(ctx, "i1", "view_a", "1h", "crypto_24x7", bar1, []string{"ma"}, "session-2")
	if err != nil || !found || record.ResultID != "old" || input.Factors["ma"] != "h1" {
		t.Fatalf("应找到旧会话的可信记录：found=%v record=%+v err=%v", found, record, err)
	}
	if _, _, found, err := repo.AdjacentRecord(ctx, "i1", "view_b", "1h", "crypto_24x7", bar1, []string{"ma"}, "session-2"); err != nil || found {
		t.Fatalf("其他 View 的记录不可信：found=%v err=%v", found, err)
	}
	if _, _, found, err := repo.AdjacentRecord(ctx, "i1", "view_a", "1h", "crypto_24x7", bar1, []string{"ma", "mom"}, "session-2"); err != nil || found {
		t.Fatalf("缺少因子指纹的记录不可信：found=%v err=%v", found, err)
	}
	if _, _, found, err := repo.AdjacentRecord(ctx, "i1", "view_a", "4h", "crypto_24x7", bar1, []string{"ma"}, "session-2"); err != nil || found {
		t.Fatalf("不同 bar 的记录不可信：found=%v err=%v", found, err)
	}
	// 当前会话同一根 bar 的 skipped 记录优先。
	current := skippedResult("new", "i1", "session-2", 1, "previous_version_unknown")
	current.InputJSON = inputJSON("view_a", map[string]string{"ma": "h2"})
	mustCommit(t, repo, current, nil, testNow)
	record, input, found, err = repo.AdjacentRecord(ctx, "i1", "view_a", "1h", "crypto_24x7", bar1, []string{"ma"}, "session-2")
	if err != nil || !found || record.ResultID != "new" || input.Factors["ma"] != "h2" {
		t.Fatalf("应优先当前会话：found=%v record=%+v err=%v", found, record, err)
	}
}

func TestListResultsPaging(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	for bar := 1; bar <= 5; bar++ {
		mustCommit(t, repo, okResult(fmt.Sprintf("r%d", bar), "i1", "session-1", bar, PublishNone), nil, testNow)
	}
	page, total, err := repo.ListResults(ctx, "i1", "", 0, 2)
	if err != nil || total != 5 || len(page) != 2 || page[0].ResultID != "r5" || page[1].ResultID != "r4" {
		t.Fatalf("分页不符：total=%d page=%+v err=%v", total, page, err)
	}
	last, _, err := repo.ListResults(ctx, "i1", "session-1", 4, 2)
	if err != nil || len(last) != 1 || last[0].ResultID != "r1" {
		t.Fatalf("末页不符：%+v err=%v", last, err)
	}
	if _, found, err := repo.ResultAtBar(ctx, "i1", "session-1", testNow.Add(3*time.Hour)); err != nil || !found {
		t.Fatalf("应找到第 3 根的记录：found=%v err=%v", found, err)
	}
}

// 确定性写入错误：结果校验失败与违反表约束；上下文取消等可重试错误不算。
func TestIsPermanentWriteError(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	result := okResult("r-dup", "i1", "session-1", 1, PublishNone)
	items := []ResultItem{
		{RuleID: "r", InstrumentID: "A", Stage: "weighted"},
		{RuleID: "r", InstrumentID: "A", Stage: "filtered"},
	}
	_, _, err := repo.CommitResult(ctx, CommitRequest{Result: result, Items: items, Now: testNow})
	if err == nil || !IsPermanentWriteError(err) {
		t.Fatalf("重复明细应是确定性写入错误：%v", err)
	}
	_, _, err = repo.CommitResult(ctx, CommitRequest{Result: Result{ResultID: "r-bad"}, Now: testNow})
	if err == nil || !IsPermanentWriteError(err) {
		t.Fatalf("结果校验失败应是确定性写入错误：%v", err)
	}
	if IsPermanentWriteError(context.Canceled) || IsPermanentWriteError(ErrResultCASConflict) {
		t.Fatal("可重试的错误不应判为确定性错误")
	}
}
