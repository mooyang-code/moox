package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func paramSubmission(requestID string) *ParamSubmission {
	return &ParamSubmission{
		RequestID: requestID, SetID: "fset_i1", PeriodTime: testNow.Add(2 * time.Hour), BasedOnPeriod: testNow.Add(time.Hour),
		ParamsJSON: json.RawMessage(`{"fast":{"window":{"default":20,"overrides":{"BTC":10}}}}`), Notes: "n",
	}
}

// 参数与结果同事务落库：结果被已有记录吞掉（同周期重复提交）时不会多写一份待提交的参数。
func TestCommitResultWritesParamSubmissionInSameTransaction(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	result := okResult("r1", "i1", "session-1", 1, PublishNone)
	if _, created, err := repo.CommitResult(ctx, CommitRequest{Result: result, Now: testNow, Params: paramSubmission("p-r1")}); err != nil || !created {
		t.Fatalf("提交失败：created=%v err=%v", created, err)
	}
	if _, created, err := repo.CommitResult(ctx, CommitRequest{Result: okResult("r1-dup", "i1", "session-1", 1, PublishNone), Now: testNow, Params: paramSubmission("p-dup")}); err != nil || created {
		t.Fatalf("同周期重复提交应返回已有记录：created=%v err=%v", created, err)
	}
	listed, err := repo.ListParamSubmissions(ctx, "i1", 10)
	if err != nil || len(listed) != 1 || listed[0].RequestID != "p-r1" || listed[0].Status != ParamPending || listed[0].ResultID != "r1" {
		t.Fatalf("待提交记录不符：%+v err=%v", listed, err)
	}
	// 参数不合法时整个结果不落库。
	bad := paramSubmission("p-bad")
	bad.ParamsJSON = json.RawMessage(`{}`)
	if _, _, err := repo.CommitResult(ctx, CommitRequest{Result: okResult("r2", "i1", "session-1", 2, PublishNone), Now: testNow, Params: bad}); err == nil {
		t.Fatal("空参数快照应被拒绝")
	}
	if _, err := repo.GetResult(ctx, "r2"); err == nil {
		t.Fatal("参数被拒绝时结果也不应落库")
	}
}

func TestParamSubmissionLifecycle(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	mustCommit(t, repo, okResult("r1", "i1", "session-1", 1, PublishNone), nil, testNow)
	if _, _, err := repo.CommitResult(ctx, CommitRequest{Result: okResult("r2", "i1", "session-1", 2, PublishNone), Now: testNow, Params: paramSubmission("p-r2")}); err != nil {
		t.Fatal(err)
	}
	due, err := repo.DuePendingParamSubmissions(ctx, testNow.Add(time.Hour), 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("应有一条到期的待提交：%+v err=%v", due, err)
	}
	if err := repo.RetryParamSubmission(ctx, "p-r2", testNow.Add(3*time.Hour), testNow); err != nil {
		t.Fatal(err)
	}
	if due, _ := repo.DuePendingParamSubmissions(ctx, testNow.Add(time.Hour), 10); len(due) != 0 {
		t.Fatalf("退避期间不应到期：%+v", due)
	}
	if err := repo.FinishParamSubmission(ctx, "p-r2", ParamTooLate, json.RawMessage(`[{"member":"fast","result":"too_late"}]`), testNow); err != nil {
		t.Fatal(err)
	}
	// 终态之后不会再被改写，也不再待提交。
	if err := repo.FinishParamSubmission(ctx, "p-r2", ParamAccepted, nil, testNow); err != nil {
		t.Fatal(err)
	}
	listed, _ := repo.ListParamSubmissions(ctx, "i1", 10)
	if len(listed) != 1 || listed[0].Status != ParamTooLate || listed[0].Attempts != 2 {
		t.Fatalf("终态不符：%+v", listed)
	}
	if count, _ := repo.PendingParamSubmissionCount(ctx); count != 0 {
		t.Fatalf("不应还有待提交：%d", count)
	}
	if err := repo.FinishParamSubmission(ctx, "p-r2", "bogus", nil, testNow); err == nil {
		t.Fatal("无效终态应被拒绝")
	}
	if deleted, err := repo.DeleteParamSubmissionsBefore(ctx, testNow.Add(-time.Hour)); err != nil || deleted != 0 {
		t.Fatalf("创建时间晚于截止的记录不应被删：deleted=%d err=%v", deleted, err)
	}
	if deleted, err := repo.DeleteParamSubmissionsBefore(ctx, testNow.Add(time.Hour)); err != nil || deleted != 1 {
		t.Fatalf("过期的终态记录应被删除：deleted=%d err=%v", deleted, err)
	}
}

// 声明了 factors 的实例按启用时固化的解析路由到私有因子集的结果 View，而不是它绑定的 K 线 View。
func TestEnabledInstancesByViewRoutesOnResolvedView(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "plain", "session-p", nil)
	if err := repo.CreateInstance(ctx, Instance{InstanceID: "tuned", StrategyID: "s1", SpaceID: "space", ViewID: "view_a", CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	resolved := json.RawMessage(`{"view_id":"view_factor_i1","source_view_id":"view_a","factor_set_id":"fset_i1"}`)
	if err := repo.OpenSession(ctx, Session{SessionID: "session-t", InstanceID: "tuned", DSLHash: testHash, ResolvedJSON: string(resolved), CreatedAt: testNow}, "name: demo"); err != nil {
		t.Fatal(err)
	}
	attachAndEnable(t, repo, "tuned", "session-t", resolved)

	onSource, err := repo.EnabledInstancesByView(ctx, "space", "view_a")
	if err != nil || len(onSource) != 1 || onSource[0].InstanceID != "plain" {
		t.Fatalf("K 线 View 上只有没声明 factors 的实例：%+v err=%v", onSource, err)
	}
	onPrivate, err := repo.EnabledInstancesByView(ctx, "space", "view_factor_i1")
	if err != nil || len(onPrivate) != 1 || onPrivate[0].InstanceID != "tuned" {
		t.Fatalf("私有 View 上应路由到声明了 factors 的实例：%+v err=%v", onPrivate, err)
	}
}

func TestDisplaySettingsDefaultAllVisible(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	if hidden, err := repo.GetDisplayHidden(ctx, "i1"); err != nil || len(hidden.Columns) != 0 {
		t.Fatalf("默认应全部展示：%+v err=%v", hidden, err)
	}
	saved, err := repo.SetDisplayHidden(ctx, "i1", DisplayHidden{Categories: []string{"volume", "volume", " "}, Columns: []string{"fast_bias"}}, testNow)
	if err != nil || len(saved.Categories) != 1 || saved.Columns[0] != "fast_bias" {
		t.Fatalf("保存结果不符（去重去空）：%+v err=%v", saved, err)
	}
	if hidden, _ := repo.GetDisplayHidden(ctx, "i1"); len(hidden.Categories) != 1 || hidden.Categories[0] != "volume" {
		t.Fatalf("读回不符：%+v", hidden)
	}
	if _, err := repo.SetDisplayHidden(ctx, "i1", DisplayHidden{}, testNow); err != nil {
		t.Fatal(err)
	}
	var rows int64
	if err := repo.db.Raw(`SELECT COUNT(*) FROM t_strategy_display_settings`).Scan(&rows).Error; err != nil || rows != 0 {
		t.Fatalf("全部展示时应删除记录：rows=%d err=%v", rows, err)
	}
	if _, err := repo.SetDisplayHidden(ctx, "missing", DisplayHidden{Columns: []string{"x"}}, testNow); err == nil {
		t.Fatal("实例不存在应报错")
	}
}
