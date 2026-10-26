package catalog

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
)

type fakeSubjects []string

func (f fakeSubjects) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return f, nil
}

// paramFixture 建一个关联了实例 inst-1 的任务，含一个已启用的可变成员 pmean__var（默认窗口 5）和一个已启用的固定成员。
func paramFixture(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	ctx := context.Background()
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := NewService(db, meta, WithSubjectSource(fakeSubjects{"BTC", "ETH"}))
	_, err := svc.CreateTask(ctx, newTask())
	require.NoError(t, err)
	_, err = svc.CreateFactor(ctx, factorWithSpec("pmean", paramSpec))
	require.NoError(t, err)
	for _, kind := range []string{domain.MemberKindVar, domain.MemberKindFixed} {
		member, err := svc.AddTaskMember(ctx, testTaskID(), "pmean", kind, nil)
		require.NoError(t, err)
		_, err = svc.SetTaskMemberStatus(ctx, testTaskID(), member.MemberKey, domain.MemberStatusEnabled)
		require.NoError(t, err)
	}
	_, err = svc.BindTask(ctx, testTaskID(), "inst-1")
	require.NoError(t, err)
	return svc, db
}

func window(defaultValue float64, overrides map[string]float64) domain.ParamSnapshot {
	return domain.ParamSnapshot{"window": {Default: defaultValue, Overrides: overrides}}
}

var base = time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)

func TestSubmitParamsAcceptsMergesAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	svc, _ := paramFixture(t)

	first, err := svc.SubmitParams(ctx, "inst-1", testTaskID(), base, "req-1",
		[]MemberSubmission{{MemberKey: "pmean__var", Snapshot: window(10, map[string]float64{"BTC": 6, "ETH": 7})}})
	require.NoError(t, err)
	require.Equal(t, []store.ParamSubmitOutcome{{MemberKey: "pmean__var", Result: store.SubmitAccepted}}, first)

	again, err := svc.SubmitParams(ctx, "inst-1", testTaskID(), base, "req-1",
		[]MemberSubmission{{MemberKey: "pmean__var", Snapshot: window(10, map[string]float64{"BTC": 6, "ETH": 7})}})
	require.NoError(t, err)
	require.Equal(t, store.SubmitDuplicate, again[0].Result)

	different, err := svc.SubmitParams(ctx, "inst-1", testTaskID(), base, "req-2",
		[]MemberSubmission{{MemberKey: "pmean__var", Snapshot: window(11, nil)}})
	require.NoError(t, err)
	require.Equal(t, store.SubmitRejected, different[0].Result, "同期不同 request_id 拒绝")

	_, err = svc.SubmitParams(ctx, "inst-1", testTaskID(), base.Add(time.Minute), "req-3",
		[]MemberSubmission{{MemberKey: "pmean__var", Snapshot: window(12, map[string]float64{"BTC": 8})}})
	require.NoError(t, err)

	states, err := svc.GetParams(ctx, testTaskID())
	require.NoError(t, err)
	require.Len(t, states, 1, "只有可变成员")
	got := states[0]
	require.Equal(t, "pmean__var", got.MemberKey)
	require.Equal(t, base.Add(time.Minute).Unix(), got.LastSubmittedPeriod)
	require.Equal(t, "req-3", got.LastRequestID)
	require.Equal(t, 12.0, got.LastSubmittedParams.Value("window", "SOL"))
	require.Equal(t, 8.0, got.LastSubmittedParams.Value("window", "BTC"), "本次带了的标的覆盖旧值")
	require.Equal(t, 7.0, got.LastSubmittedParams.Value("window", "ETH"), "没带的标的保留旧值")
}

func TestSubmitParamsRejectsPeriodRegression(t *testing.T) {
	ctx := context.Background()
	svc, _ := paramFixture(t)
	submit := func(period time.Time, id string) store.ParamSubmitOutcome {
		out, err := svc.SubmitParams(ctx, "inst-1", testTaskID(), period, id, []MemberSubmission{{MemberKey: "pmean__var", Snapshot: window(10, nil)}})
		require.NoError(t, err)
		return out[0]
	}
	require.Equal(t, store.SubmitAccepted, submit(base.Add(time.Minute), "req-1").Result)
	late := submit(base, "req-2")
	require.Equal(t, store.SubmitRejected, late.Result)
	require.Contains(t, late.Reason, "早于")
}

func TestSubmitParamsValidation(t *testing.T) {
	ctx := context.Background()
	svc, _ := paramFixture(t)

	_, err := svc.SubmitParams(ctx, "inst-2", testTaskID(), base, "req", []MemberSubmission{{MemberKey: "pmean__var", Snapshot: window(10, nil)}})
	require.ErrorContains(t, err, "不是因子任务")
	_, err = svc.SubmitParams(ctx, "", testTaskID(), base, "req", nil)
	require.ErrorContains(t, err, "写入方")
	_, err = svc.SubmitParams(ctx, "inst-1", testTaskID(), base.Add(time.Second), "req", nil)
	require.ErrorContains(t, err, "not aligned")
	_, err = svc.SubmitParams(ctx, "inst-1", testTaskID(), base, "", nil)
	require.ErrorContains(t, err, "request_id")

	reasons := map[string]string{}
	for name, submission := range map[string]MemberSubmission{
		"固定成员":   {MemberKey: "pmean", Snapshot: window(10, nil)},
		"不存在":    {MemberKey: "nope__var", Snapshot: window(10, nil)},
		"越界":     {MemberKey: "pmean__var", Snapshot: window(99, nil)},
		"整数给小数":  {MemberKey: "pmean__var", Snapshot: window(7.5, nil)},
		"标的不在范围": {MemberKey: "pmean__var", Snapshot: window(10, map[string]float64{"SOL": 6})},
		"缺参数":    {MemberKey: "pmean__var", Snapshot: domain.ParamSnapshot{}},
		"多参数":    {MemberKey: "pmean__var", Snapshot: domain.ParamSnapshot{"window": {Default: 10}, "extra": {Default: 1}}},
	} {
		out, err := svc.SubmitParams(ctx, "inst-1", testTaskID(), base, "req-"+name, []MemberSubmission{submission})
		require.NoError(t, err, name)
		require.Equal(t, store.SubmitRejected, out[0].Result, name)
		reasons[name] = out[0].Reason
	}
	require.Contains(t, reasons["固定成员"], "固定成员")
	require.Contains(t, reasons["标的不在范围"], "SOL")

	// 被拒绝的提交不留下痕迹：之后同一目标期的提交仍然被接受。
	out, err := svc.SubmitParams(ctx, "inst-1", testTaskID(), base, "req-ok", []MemberSubmission{{MemberKey: "pmean__var", Snapshot: window(10, nil)}})
	require.NoError(t, err)
	require.Equal(t, store.SubmitAccepted, out[0].Result)
}

func TestSubmitParamsRejectsDisabledMember(t *testing.T) {
	ctx := context.Background()
	svc, _ := paramFixture(t)
	_, err := svc.SetTaskMemberStatus(ctx, testTaskID(), "pmean__var", domain.MemberStatusDisabled)
	require.NoError(t, err)
	out, err := svc.SubmitParams(ctx, "inst-1", testTaskID(), base, "req", []MemberSubmission{{MemberKey: "pmean__var", Snapshot: window(10, nil)}})
	require.NoError(t, err)
	require.Equal(t, store.SubmitRejected, out[0].Result)
	require.Contains(t, out[0].Reason, "未启用")
}

// 提交只写运行状态列，不改变成员的 updated_at（目录指纹不变，引擎不会因为每期提交而重新同步目录）。
func TestSubmitParamsDoesNotChangeMemberUpdatedAt(t *testing.T) {
	ctx := context.Background()
	svc, db := paramFixture(t)
	before, err := db.GetMember(ctx, testTaskID(), "pmean__var")
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)
	out, err := svc.SubmitParams(ctx, "inst-1", testTaskID(), base, "req", []MemberSubmission{{MemberKey: "pmean__var", Snapshot: window(10, nil)}})
	require.NoError(t, err)
	require.Equal(t, store.SubmitAccepted, out[0].Result)
	after, err := db.GetMember(ctx, testTaskID(), "pmean__var")
	require.NoError(t, err)
	require.Equal(t, before.UpdatedAt, after.UpdatedAt)
}
