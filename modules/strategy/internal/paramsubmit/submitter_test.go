package paramsubmit

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
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

func (f *factorsFake) BindFactorTask(context.Context, string, string) (input.BoundTask, error) {
	return input.BoundTask{}, nil
}
func (f *factorsFake) SubmitFactorParams(_ context.Context, submission input.ParamSubmission) ([]input.MemberSubmitResult, error) {
	f.calls = append(f.calls, submission)
	return f.results, f.err
}

// commit 给实例 i1 的会话 session-1 提交第 bar 期的结果，附上下一期（now 之后 bar+1 小时）的参数。
func commit(t *testing.T, db *store.Store, bar int) string {
	t.Helper()
	barEnd := now.Add(time.Duration(bar) * time.Hour)
	members := json.RawMessage(`{"bias__var":{"window":{"default":20,"overrides":{"BTC":10}}}}`)
	next, err := store.EncodeNextParams(barEnd.Add(time.Hour), members)
	if err != nil {
		t.Fatal(err)
	}
	id := "r" + string(rune('0'+bar))
	result := store.Result{
		ResultID: id, InstanceID: "i1", SessionID: "session-1", BarEndTime: barEnd, ValidUntil: barEnd.Add(2 * time.Hour), Status: store.StatusOK, DSLHash: "sha256:x",
		InputJSON: json.RawMessage(`{"view_id":"view_factor_i1"}`), TargetsJSON: json.RawMessage(`[]`), RuleStatesJSON: json.RawMessage(`{}`),
		SummaryJSON: json.RawMessage(`{}`), PublishStatus: store.PublishNone, NextParamsJSON: next, CreatedAt: barEnd,
	}
	if _, _, err := db.CommitResult(context.Background(), store.CommitRequest{Result: result, Now: now}); err != nil {
		t.Fatal(err)
	}
	return id
}

// harness 建一个启用的实例和它的第 0 期结果（带下一期参数，目标期在 now 之后一小时）。
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
	if err := db.CreateInstance(ctx, store.Instance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", FactorTaskID: "ftask_i1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	resolved := json.RawMessage(`{"view_id":"view_factor_i1","factor_task_id":"ftask_i1"}`)
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
	commit(t, db, 0)
	fake := &factorsFake{results: []input.MemberSubmitResult{{Member: "bias__var", Result: input.SubmitAccepted}}}
	return db, &Submitter{Store: db, Factors: fake, Now: func() time.Time { return now }}, fake
}

func result(t *testing.T, db *store.Store, id string) store.Result {
	t.Helper()
	got, err := db.GetResult(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// 接受：以实例为写入方、结果 ID 作幂等键、带上完整快照提交，并记送达。
func TestSubmitterDeliversAndRecordsDelivered(t *testing.T) {
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
	if call.Writer != "i1" || call.TaskID != "ftask_i1" || call.RequestID != "r0" || !call.PeriodTime.Equal(now.Add(time.Hour)) ||
		call.Members["bias__var"]["window"].Default != 20 || call.Members["bias__var"]["window"].Overrides["BTC"] != 10 {
		t.Fatalf("提交内容不符：%+v", call)
	}
	if got := result(t, db, "r0"); got.ParamsDeliveredAt == nil || got.ParamsError != "" || len(finished) != 1 || finished[0] != ResultDelivered {
		t.Fatalf("送达状态不符：%+v %v", got, finished)
	}
	if n, _ := submitter.RunOnce(context.Background()); n != 0 {
		t.Fatalf("送达之后不再处理：%d", n)
	}
}

// 只投会话里最新的一条：更早未送达的被覆盖。
func TestSubmitterDeliversOnlyTheLatest(t *testing.T) {
	db, submitter, fake := harness(t)
	commit(t, db, 1)
	if _, err := submitter.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 1 || fake.calls[0].RequestID != "r1" {
		t.Fatalf("应只投最新的一条：%+v", fake.calls)
	}
}

// 任一成员被拒绝，整条记为被拒绝并记下原因，不再重试；请求本身无效（INVALID_PARAM）同样记被拒绝。
func TestSubmitterRecordsRejected(t *testing.T) {
	db, submitter, fake := harness(t)
	var finished []string
	submitter.OnResult = func(status string) { finished = append(finished, status) }
	fake.results = []input.MemberSubmitResult{{Member: "bias__var", Result: input.SubmitRejected, Reason: "超出范围"}}
	if _, err := submitter.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := result(t, db, "r0")
	if got.ParamsDeliveredAt != nil || !strings.Contains(got.ParamsError, "超出范围") || !strings.Contains(got.ParamsError, "bias__var") {
		t.Fatalf("应记被拒绝及原因：%+v", got)
	}
	if n, _ := submitter.RunOnce(context.Background()); n != 0 || len(fake.calls) != 1 {
		t.Fatalf("被拒绝后不再重试：n=%d calls=%d", n, len(fake.calls))
	}

	commit(t, db, 1)
	fake.err = &input.ServiceError{Operation: "提交因子参数", Code: commonpb.ErrorCode_INVALID_PARAM, Raw: errors.New("bad")}
	if _, err := submitter.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := result(t, db, "r1"); got.ParamsError == "" || got.ParamsDeliveredAt != nil {
		t.Fatalf("无效请求应记被拒绝：%+v", got)
	}
	if len(finished) != 2 || finished[0] != ResultRejected || finished[1] != ResultRejected {
		t.Fatalf("指标不符：%v", finished)
	}
}

// 调用失败（FactorMgr 不可达）退避重试，保持待送达并上报异常；退避期过后重试，成功后记送达。
func TestSubmitterRetriesTransientWithBackoff(t *testing.T) {
	db, submitter, fake := harness(t)
	fake.err = errors.New("dial tcp: connection refused")
	var reported int
	submitter.OnError = func(string, error) { reported++ }
	if _, err := submitter.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := result(t, db, "r0"); got.ParamsDeliveredAt != nil || got.ParamsError != "" || reported != 1 || len(fake.calls) != 1 {
		t.Fatalf("应保持待送达：%+v reported=%d calls=%d", got, reported, len(fake.calls))
	}
	if _, err := submitter.RunOnce(context.Background()); err != nil || len(fake.calls) != 1 {
		t.Fatalf("退避期间不应重试：calls=%d err=%v", len(fake.calls), err)
	}
	submitter.Now = func() time.Time { return now.Add(5 * time.Second) }
	fake.err = nil
	if _, err := submitter.RunOnce(context.Background()); err != nil || len(fake.calls) != 2 {
		t.Fatalf("退避期过后应重试：calls=%d err=%v", len(fake.calls), err)
	}
	if got := result(t, db, "r0"); got.ParamsDeliveredAt == nil {
		t.Fatalf("重试成功应记送达：%+v", got)
	}
}

// 暂停时不提交、记录保持待送达，恢复后照常提交；实例停用后不再投递。
func TestSubmitterHonorsPauseAndDisable(t *testing.T) {
	db, submitter, fake := harness(t)
	submitter.Allowed = func(string) bool { return false }
	if _, err := submitter.RunOnce(context.Background()); err != nil || len(fake.calls) != 0 || result(t, db, "r0").ParamsDeliveredAt != nil {
		t.Fatalf("暂停时应保持待送达：calls=%d err=%v", len(fake.calls), err)
	}
	submitter.Allowed = nil
	if _, err := submitter.RunOnce(context.Background()); err != nil || len(fake.calls) != 1 {
		t.Fatalf("恢复后应提交：calls=%d err=%v", len(fake.calls), err)
	}

	db2, submitter2, fake2 := harness(t)
	session := "session-1"
	if err := db2.DisableInstance(context.Background(), "i1", &session, nil, "", now); err != nil {
		t.Fatal(err)
	}
	if n, err := submitter2.RunOnce(context.Background()); err != nil || n != 0 || len(fake2.calls) != 0 {
		t.Fatalf("实例停用后不应投递：n=%d calls=%d err=%v", n, len(fake2.calls), err)
	}
}
