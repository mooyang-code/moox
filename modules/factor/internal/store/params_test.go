package store

import (
	"context"
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
	_, err := s.AddMember(ctx, taskID, key, factorID, domain.MemberKindVar, map[string]float64{"span": 20}, "")
	require.NoError(t, err)
	return key
}

func snapshot(defaultSpan float64, overrides map[string]float64) domain.ParamSnapshot {
	return domain.ParamSnapshot{"span": {Default: defaultSpan, Overrides: overrides}}
}

func submit(t *testing.T, s *Store, period int64, requestID string, key string, snap domain.ParamSnapshot) ParamSubmitOutcome {
	t.Helper()
	outcomes, err := s.SubmitMemberParams(context.Background(), "task_prices", period, requestID,
		[]ParamSubmission{{MemberKey: key, Snapshot: snap}})
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	return outcomes[0]
}

// 一个因子在同一个任务里最多一个可变成员；固定成员不受限制。
func TestVarMemberLimitedToOnePerFactor(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateTask(ctx, testTask("task_prices", domain.TaskStatusEnabled)))
	addVarMember(t, s, "task_prices", "ema")
	_, err := s.AddMember(ctx, "task_prices", "ema__other", "ema", domain.MemberKindVar, nil, "")
	require.ErrorIs(t, err, ErrConflict)
	_, err = s.AddMember(ctx, "task_prices", "ema", "ema", domain.MemberKindFixed, map[string]float64{"span": 20}, "")
	require.NoError(t, err)
	_, err = s.AddMember(ctx, "task_prices", "ema__30", "ema", domain.MemberKindFixed, map[string]float64{"span": 30}, "")
	require.NoError(t, err)
	_, err = s.AddMember(ctx, "task_prices", "ema__x", "ema", "other", nil, "")
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
	require.False(t, states[0].LastSubmittedAt.IsZero())
	require.Equal(t, 50.0, states[0].Params.Value("span", "BTC"))
}

// 整份替换：这次没带覆盖的标的回到默认值，不保留上一期的覆盖。
func TestSubmitMemberParamsReplacesWholeSnapshot(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateTask(ctx, testTask("task_prices", domain.TaskStatusEnabled)))
	key := addVarMember(t, s, "task_prices", "ema")

	submit(t, s, 1000, "req-1", key, snapshot(30, map[string]float64{"BTC": 10, "ETH": 15}))
	submit(t, s, 2000, "req-2", key, snapshot(40, map[string]float64{"BTC": 12, "SOL": 8}))

	states, err := s.ListMemberParamStates(ctx, "task_prices")
	require.NoError(t, err)
	got := states[0].Params
	require.Equal(t, 12.0, got.Value("span", "BTC"))
	require.Equal(t, 40.0, got.Value("span", "ETH"))
	require.Equal(t, 8.0, got.Value("span", "SOL"))
	require.Equal(t, 40.0, got.Value("span", "XRP"))
}

// 提交只写参数表，不碰成员行的 c_mtime，所以不改变目录指纹；改成员行（状态）才更新 c_mtime。
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

// 删成员时它的提交状态跟着删掉（外键级联）：同名成员重新加入后目标期从头开始。
func TestRemoveMemberDropsSubmission(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateTask(ctx, testTask("task_prices", domain.TaskStatusEnabled)))
	key := addVarMember(t, s, "task_prices", "ema")
	require.Equal(t, SubmitAccepted, submit(t, s, 1000, "req-1", key, snapshot(30, nil)).Result)

	require.NoError(t, s.RemoveMember(ctx, "task_prices", key))
	var n int
	require.NoError(t, s.db.Raw("SELECT COUNT(*) FROM t_factor_member_params").Scan(&n).Error)
	require.Zero(t, n)
	addVarMember(t, s, "task_prices", "ema")
	require.Equal(t, SubmitAccepted, submit(t, s, 500, "req-2", key, snapshot(40, nil)).Result)
}

func TestRecalcJobDefaultFilledRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateTask(ctx, testTask("task_prices", domain.TaskStatusEnabled)))
	job, err := s.CreateRecalcJob(ctx, RecalcJob{JobID: "job-1", TaskID: "task_prices", StartTime: 100, EndTime: 1000})
	require.NoError(t, err)
	require.Empty(t, job.DefaultFilled)
	now := time.Unix(500, 0)
	pulled, found, err := s.PullRecalcJob(ctx, now, nil)
	require.NoError(t, err)
	require.True(t, found)

	filled := map[string][]TimeRange{"ema__var": {{Start: 100, End: 160}, {Start: 300, End: 300}}}
	updated, err := s.ReportRecalcProgress(ctx, pulled.JobID, 400, RecalcStatusRunning, "", filled, now)
	require.NoError(t, err)
	require.Equal(t, filled, updated.DefaultFilled)

	// nil 表示没有变化；非 nil 整体替换。
	updated, err = s.ReportRecalcProgress(ctx, pulled.JobID, 500, RecalcStatusRunning, "", nil, now)
	require.NoError(t, err)
	require.Equal(t, filled, updated.DefaultFilled)
	done, err := s.ReportRecalcProgress(ctx, pulled.JobID, 1000, RecalcStatusSucceeded, "", map[string][]TimeRange{}, now)
	require.NoError(t, err)
	require.Empty(t, done.DefaultFilled)
}
