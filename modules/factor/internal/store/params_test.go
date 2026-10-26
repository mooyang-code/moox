package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

func addVarMember(t *testing.T, s *Store, taskID, factorID string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := s.GetFactor(ctx, factorID); err != nil {
		require.NoError(t, s.CreateFactor(ctx, testFactorDef(factorID)))
	}
	key := factorID + "__var"
	_, err := s.AddMember(ctx, taskID, key, factorID, domain.MemberKindVar, map[string]float64{"span": 20})
	require.NoError(t, err)
	return key
}

func snapshot(defaultSpan float64, overrides map[string]float64) domain.ParamSnapshot {
	return domain.ParamSnapshot{"span": {Default: defaultSpan, Overrides: overrides}}
}

func submit(t *testing.T, s *Store, period int64, requestID string, key string, snap domain.ParamSnapshot) ParamSubmitOutcome {
	t.Helper()
	outcomes, err := s.SubmitMemberParams(context.Background(), "task_prices", period, requestID,
		[]ParamSubmission{{MemberKey: key, Snapshot: snap}}, nil)
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	return outcomes[0]
}

// 一个因子在同一个任务里最多一个可变成员；固定成员不受限制。
func TestVarMemberIsUniquePerFactor(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateTask(ctx, testTask("task_prices", domain.TaskStatusEnabled)))
	addVarMember(t, s, "task_prices", "ema")
	_, err := s.AddMember(ctx, "task_prices", "ema__other", "ema", domain.MemberKindVar, nil)
	require.ErrorIs(t, err, ErrConflict)
	_, err = s.AddMember(ctx, "task_prices", "ema", "ema", domain.MemberKindFixed, map[string]float64{"span": 20})
	require.NoError(t, err)
	_, err = s.AddMember(ctx, "task_prices", "ema__30", "ema", domain.MemberKindFixed, map[string]float64{"span": 30})
	require.NoError(t, err)
	_, err = s.AddMember(ctx, "task_prices", "ema__x", "ema", "other", nil)
	require.ErrorContains(t, err, "member kind")
}

// 目标期不能倒退；同期同 request_id 幂等；同期不同 request_id 拒绝；成员必须存在且可变。
func TestSubmitMemberParamsPeriodAndIdempotency(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateTask(ctx, testTask("task_prices", domain.TaskStatusEnabled)))
	key := addVarMember(t, s, "task_prices", "ema")
	addTestMember(t, s, "task_prices", "fixed_one", domain.MemberStatusDisabled)

	first := submit(t, s, 1000, "req-1", key, snapshot(30, nil))
	require.Equal(t, SubmitAccepted, first.Result)
	require.Equal(t, SubmitDuplicate, submit(t, s, 1000, "req-1", key, snapshot(30, nil)).Result)

	other := submit(t, s, 1000, "req-2", key, snapshot(40, nil))
	require.Equal(t, SubmitRejected, other.Result)
	require.Contains(t, other.Reason, "request_id req-1")

	back := submit(t, s, 900, "req-3", key, snapshot(40, nil))
	require.Equal(t, SubmitRejected, back.Result)
	require.Contains(t, back.Reason, "早于")

	forward := submit(t, s, 2000, "req-4", key, snapshot(50, nil))
	require.Equal(t, SubmitAccepted, forward.Result)

	fixed := submit(t, s, 3000, "req-5", "fixed_one", snapshot(30, nil))
	require.Equal(t, SubmitRejected, fixed.Result)
	require.Contains(t, fixed.Reason, "固定成员")
	missing := submit(t, s, 3000, "req-6", "nope__var", snapshot(30, nil))
	require.Equal(t, SubmitRejected, missing.Result)

	states, err := s.ListMemberParamStates(ctx, "task_prices")
	require.NoError(t, err)
	require.Len(t, states, 1)
	require.Equal(t, int64(2000), states[0].LastSubmittedPeriod)
	require.Equal(t, "req-4", states[0].LastRequestID)
	require.NotNil(t, states[0].LastSubmittedAt)
	require.Equal(t, 50.0, states[0].LastSubmittedParams.Value("span", "BTC"))
}

// 按标的合并：默认值取本次提交的；本次带了的标的覆盖旧值，没带的标的保留旧值。
func TestSubmitMemberParamsMergesBySubject(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateTask(ctx, testTask("task_prices", domain.TaskStatusEnabled)))
	key := addVarMember(t, s, "task_prices", "ema")

	submit(t, s, 1000, "req-1", key, snapshot(30, map[string]float64{"BTC": 10, "ETH": 15}))
	submit(t, s, 2000, "req-2", key, snapshot(40, map[string]float64{"BTC": 12, "SOL": 8}))

	states, err := s.ListMemberParamStates(ctx, "task_prices")
	require.NoError(t, err)
	got := states[0].LastSubmittedParams
	require.Equal(t, 12.0, got.Value("span", "BTC"))
	require.Equal(t, 15.0, got.Value("span", "ETH"))
	require.Equal(t, 8.0, got.Value("span", "SOL"))
	require.Equal(t, 40.0, got.Value("span", "XRP"))
}

// 合并结果由调用方校验：校验失败的成员被拒绝，其他成员照常接受，被拒绝的成员状态不变。
func TestSubmitMemberParamsChecksMergedSnapshotPerMember(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateTask(ctx, testTask("task_prices", domain.TaskStatusEnabled)))
	a := addVarMember(t, s, "task_prices", "ema")
	b := addVarMember(t, s, "task_prices", "wma")

	outcomes, err := s.SubmitMemberParams(ctx, "task_prices", 1000, "req-1", []ParamSubmission{
		{MemberKey: a, Snapshot: snapshot(30, nil)},
		{MemberKey: b, Snapshot: snapshot(99, nil)},
	}, func(memberKey string, merged domain.ParamSnapshot) error {
		if merged.Value("span", "") > 50 {
			return errors.New("span 超过上限")
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, []ParamSubmitOutcome{
		{MemberKey: a, Result: SubmitAccepted},
		{MemberKey: b, Result: SubmitRejected, Reason: "span 超过上限"},
	}, outcomes)
	states, err := s.ListMemberParamStates(ctx, "task_prices")
	require.NoError(t, err)
	require.Equal(t, int64(1000), states[0].LastSubmittedPeriod)
	require.Equal(t, int64(0), states[1].LastSubmittedPeriod)
}

// 提交只写 c_last_* 运行状态列，不更新 c_mtime，所以不改变目录指纹；改配置列（状态）才更新 c_mtime。
func TestSubmitMemberParamsDoesNotTouchMtime(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateTask(ctx, testTask("task_prices", domain.TaskStatusEnabled)))
	key := addVarMember(t, s, "task_prices", "ema")
	require.NoError(t, s.db.Exec("UPDATE t_factor_task_members SET c_mtime = '2000-01-01 00:00:00'").Error)

	require.Equal(t, SubmitAccepted, submit(t, s, 1000, "req-1", key, snapshot(30, nil)).Result)
	var mtime string
	require.NoError(t, s.db.Raw("SELECT c_mtime FROM t_factor_task_members WHERE c_member_key = ?", key).Scan(&mtime).Error)
	require.Contains(t, mtime, "2000-01-01")

	require.NoError(t, s.SetMemberStatus(ctx, "task_prices", key, domain.MemberStatusDisabled, domain.MemberStatusEnabled))
	require.NoError(t, s.db.Raw("SELECT c_mtime FROM t_factor_task_members WHERE c_member_key = ?", key).Scan(&mtime).Error)
	require.NotContains(t, mtime, "2000-01-01")
}

// 固定成员不能带提交状态（CHECK 约束）。
func TestFixedMemberCannotCarrySubmission(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateTask(ctx, testTask("task_prices", domain.TaskStatusEnabled)))
	addTestMember(t, s, "task_prices", "factor_close", domain.MemberStatusDisabled)
	require.Error(t, s.db.Exec("UPDATE t_factor_task_members SET c_last_submitted_period = 5 WHERE c_member_key = 'factor_close'").Error)
}

func TestRecalcJobDefaultFilledRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateTask(ctx, testTask("task_prices", domain.TaskStatusEnabled)))
	job, err := s.CreateRecalcJob(ctx, RecalcJob{JobID: "job-1", RequestID: "req-1", TaskID: "task_prices", StartTime: 100, EndTime: 1000})
	require.NoError(t, err)
	require.Empty(t, job.DefaultFilled)
	now := time.Unix(500, 0)
	pulled, found, err := s.PullRecalcJob(ctx, "engine-1", now, time.Minute, nil, 0)
	require.NoError(t, err)
	require.True(t, found)

	filled := map[string][]TimeRange{"ema__var": {{Start: 100, End: 160}, {Start: 300, End: 300}}}
	updated, err := s.ReportRecalcProgress(ctx, pulled.JobID, pulled.LeaseToken, 400, RecalcStatusRunning, "", filled, now, time.Minute)
	require.NoError(t, err)
	require.Equal(t, filled, updated.DefaultFilled)

	// nil 表示没有变化；非 nil 整体替换。
	updated, err = s.ReportRecalcProgress(ctx, pulled.JobID, pulled.LeaseToken, 500, RecalcStatusRunning, "", nil, now, time.Minute)
	require.NoError(t, err)
	require.Equal(t, filled, updated.DefaultFilled)
	done, err := s.ReportRecalcProgress(ctx, pulled.JobID, pulled.LeaseToken, 1000, RecalcStatusSucceeded, "", map[string][]TimeRange{}, now, time.Minute)
	require.NoError(t, err)
	require.Empty(t, done.DefaultFilled)
}
