package paramsubmit

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"github.com/mooyang-code/moox/packages/commonpb"
)

var now = time.Date(2026, 10, 23, 8, 0, 0, 0, time.UTC)

type factorsFake struct {
	calls   []input.ParamSubmission
	results []input.MemberSubmitResult
	err     error
}

func (f *factorsFake) EnsureInstanceFactorTask(context.Context, input.InstanceFactorTaskRequest) (input.InstanceFactorTask, error) {
	return input.InstanceFactorTask{}, nil
}
func (f *factorsFake) SetInstanceFactorTaskEnabled(context.Context, string, bool) error { return nil }
func (f *factorsFake) ReleaseInstanceFactorTask(context.Context, string) error          { return nil }
func (f *factorsFake) SubmitFactorParams(_ context.Context, submission input.ParamSubmission) ([]input.MemberSubmitResult, error) {
	f.calls = append(f.calls, submission)
	return f.results, f.err
}

// harness 建一个启用的实例和它的一条待提交参数（目标期次在 now 之后一小时）。
func harness(t *testing.T) (*store.Store, *Submitter, *factorsFake) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "strategy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := db.CreateDefinition(ctx, store.Definition{StrategyID: "s1", Name: "demo", DSLYaml: "name: demo", DSLHash: "sha256:x", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateInstance(ctx, store.Instance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", ViewID: "view_a", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	resolved := json.RawMessage(`{"view_id":"view_factor_i1","source_view_id":"view_a","factor_task_id":"ftask_i1"}`)
	if err := db.OpenSession(ctx, store.Session{SessionID: "session-1", InstanceID: "i1", DSLHash: "sha256:x", ResolvedJSON: string(resolved), CreatedAt: now}, "name: demo"); err != nil {
		t.Fatal(err)
	}
	session := "session-1"
	if err := db.SetInstanceEnabled(ctx, "i1", false, &session, resolved, now); err != nil {
		t.Fatal(err)
	}
	if err := db.SetInstanceEnabled(ctx, "i1", true, &session, resolved, now); err != nil {
		t.Fatal(err)
	}
	result := store.Result{
		ResultID: "r1", InstanceID: "i1", SessionID: "session-1", BarEndTime: now, ValidUntil: now.Add(2 * time.Hour), Status: store.StatusOK, DSLHash: "sha256:x",
		InputJSON: json.RawMessage(`{"view_id":"view_factor_i1"}`), TargetsJSON: json.RawMessage(`[]`), RuleStatesJSON: json.RawMessage(`{}`),
		SummaryJSON: json.RawMessage(`{}`), PublishStatus: store.PublishNone, CreatedAt: now,
	}
	submission := &store.ParamSubmission{
		RequestID: "p-r1", TaskID: "ftask_i1", PeriodTime: now.Add(time.Hour), BasedOnPeriod: now,
		ParamsJSON: json.RawMessage(`{"fast":{"window":{"default":20,"overrides":{"BTC":10}}}}`),
	}
	if _, _, err := db.CommitResult(ctx, store.CommitRequest{Result: result, Now: now, Params: submission}); err != nil {
		t.Fatal(err)
	}
	fake := &factorsFake{results: []input.MemberSubmitResult{{Member: "fast", Result: input.SubmitAccepted}}}
	return db, &Submitter{Store: db, Factors: fake, Now: func() time.Time { return now }}, fake
}

func status(t *testing.T, db *store.Store) store.ParamSubmission {
	t.Helper()
	listed, err := db.ListParamSubmissions(context.Background(), "i1", 10)
	if err != nil || len(listed) != 1 {
		t.Fatalf("记录不符：%+v err=%v", listed, err)
	}
	return listed[0]
}

// 接受：以实例为写入方、带上完整快照提交，并记终态。
func TestSubmitterDeliversAndRecordsAccepted(t *testing.T) {
	db, submitter, fake := harness(t)
	var finished []string
	submitter.OnResult = func(status string) { finished = append(finished, status) }
	if n, err := submitter.RunOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("应提交一次：%+v", fake.calls)
	}
	call := fake.calls[0]
	if call.Writer != "i1" || call.TaskID != "ftask_i1" || call.RequestID != "p-r1" || !call.PeriodTime.Equal(now.Add(time.Hour)) ||
		call.Members["fast"]["window"].Default != 20 || call.Members["fast"]["window"].Overrides["BTC"] != 10 {
		t.Fatalf("提交内容不符：%+v", call)
	}
	if got := status(t, db); got.Status != store.ParamAccepted || len(finished) != 1 || finished[0] != store.ParamAccepted {
		t.Fatalf("终态不符：%+v %v", got, finished)
	}
	if n, _ := submitter.RunOnce(context.Background()); n != 0 {
		t.Fatalf("终态之后不再处理：%d", n)
	}
}

// 任一成员 too_late 记 too_late，任一被拒绝记 rejected（拒绝优先）。
func TestSubmitterRecordsTooLateAndRejected(t *testing.T) {
	db, submitter, fake := harness(t)
	fake.results = []input.MemberSubmitResult{{Member: "fast", Result: input.SubmitAccepted}, {Member: "slow", Result: input.SubmitTooLate}}
	if _, err := submitter.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := status(t, db); got.Status != store.ParamTooLate {
		t.Fatalf("应为 too_late：%+v", got)
	}
	if terminalStatus([]input.MemberSubmitResult{{Result: input.SubmitTooLate}, {Result: input.SubmitRejected}}) != store.ParamRejected {
		t.Fatal("拒绝应优先于晚到")
	}
}

// 调用失败（FactorMgr 不可达）退避重试，保持待提交；请求本身无效（INVALID_PARAM）直接记 rejected。
func TestSubmitterRetriesTransientAndRejectsInvalid(t *testing.T) {
	db, submitter, fake := harness(t)
	fake.err = errors.New("dial tcp: connection refused")
	var reported int
	submitter.OnError = func(string, error) { reported++ }
	if _, err := submitter.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := status(t, db)
	if got.Status != store.ParamPending || got.Attempts != 1 || !got.NextAttempt.After(now) || reported != 1 {
		t.Fatalf("应退避重试：%+v reported=%d", got, reported)
	}
	if n, _ := submitter.RunOnce(context.Background()); n != 0 {
		t.Fatalf("退避期间不应重试：%d", n)
	}
	submitter.Now = func() time.Time { return now.Add(5 * time.Second) }
	fake.err = &input.ServiceError{Operation: "提交因子参数", Code: commonpb.ErrorCode_INVALID_PARAM, Raw: errors.New("bad")}
	if _, err := submitter.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := status(t, db); got.Status != store.ParamRejected {
		t.Fatalf("无效请求应记 rejected：%+v", got)
	}
}

// 实例停用（或换了会话）、超过重试期限的记录放弃，不去影响私有因子任务；暂停时保持待提交。
func TestSubmitterAbandonsAndHonorsPause(t *testing.T) {
	db, submitter, fake := harness(t)
	submitter.Allowed = func(string) bool { return false }
	if _, err := submitter.RunOnce(context.Background()); err != nil || len(fake.calls) != 0 || status(t, db).Status != store.ParamPending {
		t.Fatalf("暂停时应保持待提交：%+v", status(t, db))
	}
	submitter.Allowed = nil
	submitter.Now = func() time.Time { return now.Add(3 * time.Hour) }
	if _, err := submitter.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := status(t, db); got.Status != store.ParamAbandoned || len(fake.calls) != 0 {
		t.Fatalf("超过期限应放弃：%+v", got)
	}

	db2, submitter2, fake2 := harness(t)
	session := "session-1"
	if err := db2.DisableInstance(context.Background(), "i1", &session, nil, "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := submitter2.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := status(t, db2); got.Status != store.ParamAbandoned || len(fake2.calls) != 0 {
		t.Fatalf("实例停用应放弃：%+v", got)
	}
}

// 暂停的记录被推迟扫描（不占每批的前几条，不计失败次数）；恢复后到了时间照常提交。
func TestSubmitterDefersPausedRecords(t *testing.T) {
	db, submitter, fake := harness(t)
	submitter.Allowed = func(string) bool { return false }
	if n, err := submitter.RunOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	submitter.Allowed = nil
	if n, _ := submitter.RunOnce(context.Background()); n != 0 {
		t.Fatalf("推迟期间不应再被扫到：%d", n)
	}
	if got := status(t, db); got.Attempts != 0 {
		t.Fatalf("推迟不计尝试：%+v", got)
	}
	submitter.Now = func() time.Time { return now.Add(pausedRecheck + time.Second) }
	if n, err := submitter.RunOnce(context.Background()); err != nil || n != 1 || len(fake.calls) != 1 {
		t.Fatalf("恢复后应提交：n=%d err=%v calls=%d", n, err, len(fake.calls))
	}
}
