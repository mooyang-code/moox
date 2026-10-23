package store

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

func period(minute int) time.Time { return time.Unix(int64(1_800_000_000+minute*60), 0).UTC() }

func snapshotOf(window float64) domain.ParamSnapshot {
	return domain.ParamSnapshot{"window": {Default: window}}
}

func submit(t *testing.T, s *Store, requestID string, at time.Time, window float64) (domain.ParamRequest, error) {
	t.Helper()
	return s.SubmitParamRequest(context.Background(), domain.ParamRequest{RequestID: requestID, SetID: "set", MemberAlias: "fast",
		PeriodTime: at, Snapshot: snapshotOf(window), Writer: "inst"})
}

func freeze(t *testing.T, s *Store, at time.Time) domain.ParamFreeze {
	t.Helper()
	freezes, err := s.FreezeParams(context.Background(), "set", at, at.Add(-time.Minute),
		[]FreezeMember{{Alias: "fast", Defaults: snapshotOf(20)}})
	require.NoError(t, err)
	require.Len(t, freezes, 1)
	return freezes[0]
}

// 冻结选库内序号最大的已接受请求；没有请求时沿用上一期；从未有过冻结时用默认值。冻结之后的同期请求 too_late。
func TestFreezeSelectsLatestRequestCarriesAndDefaults(t *testing.T) {
	s := openTestStore(t)
	first := freeze(t, s, period(0))
	require.Equal(t, domain.ParamsDefault, first.Source)
	require.Equal(t, 20.0, first.Snapshot.Value("window", "BTC"))

	_, err := submit(t, s, "r1", period(1), 30)
	require.NoError(t, err)
	_, err = submit(t, s, "r2", period(1), 40)
	require.NoError(t, err)
	got := freeze(t, s, period(1))
	require.Equal(t, domain.ParamsRequested, got.Source)
	require.Equal(t, "r2", got.RequestID)
	require.Equal(t, 40.0, got.Snapshot.Value("window", "BTC"))

	_, err = submit(t, s, "late", period(1), 50)
	require.True(t, IsTooLate(err), "err = %v", err)

	carried := freeze(t, s, period(2))
	require.Equal(t, domain.ParamsCarried, carried.Source)
	require.Equal(t, 40.0, carried.Snapshot.Value("window", "BTC"))

	// 重复冻结同一期返回同一份（引擎重试、重启后重算）。
	again := freeze(t, s, period(1))
	require.Equal(t, got.SnapshotHash, again.SnapshotHash)
	require.Equal(t, domain.ParamsRequested, again.Source)
	require.Equal(t, "r2", again.RequestID)

	requests, err := s.ListParamRequests(context.Background(), "set", "fast", period(0), period(5), 0)
	require.NoError(t, err)
	statuses := map[string]string{}
	for _, request := range requests {
		statuses[request.RequestID] = request.Status
	}
	require.Equal(t, map[string]string{"r1": domain.ParamRequestSuperseded, "r2": domain.ParamRequestApplied}, statuses)
}

// 快照与来源相同的连续期次压缩成一个区间；快照或来源变化时新开区间。
func TestFreezeIntervalsCompress(t *testing.T) {
	s := openTestStore(t)
	for minute := 0; minute < 5; minute++ {
		freeze(t, s, period(minute))
	}
	_, err := submit(t, s, "r", period(5), 30)
	require.NoError(t, err)
	freeze(t, s, period(5))
	_, err = submit(t, s, "r6", period(6), 30)
	require.NoError(t, err)
	freeze(t, s, period(6))
	freeze(t, s, period(7))
	intervals, err := s.ListParamIntervals(context.Background(), "set", "", period(0), period(10))
	require.NoError(t, err)
	type span struct {
		start, end int
		source     string
	}
	var got []span
	for _, interval := range intervals {
		got = append(got, span{int(interval.Start.Sub(period(0)) / time.Minute), int(interval.End.Sub(period(0)) / time.Minute), interval.Source})
	}
	require.Equal(t, []span{{0, 4, domain.ParamsDefault}, {5, 6, domain.ParamsRequested}, {7, 7, domain.ParamsCarried}}, got)

	// 区间查询带上 from 之前最近的一个区间（from 没有记录时沿用它）。
	later, err := s.ListParamIntervals(context.Background(), "set", "fast", period(20), period(30))
	require.NoError(t, err)
	require.Len(t, later, 1)
	require.Equal(t, period(7), later[0].End)
}

// 同一 request_id 重复提交返回同一结果；内容不同报冲突。
func TestSubmitParamRequestIdempotent(t *testing.T) {
	s := openTestStore(t)
	first, err := submit(t, s, "r", period(3), 30)
	require.NoError(t, err)
	again, err := submit(t, s, "r", period(3), 30)
	require.NoError(t, err)
	require.Equal(t, first.Seq, again.Seq)
	_, err = submit(t, s, "r", period(3), 31)
	require.ErrorIs(t, err, ErrConflict)
}

// 离线冻结补报：管理端没有记录的期次写入 carried_offline 并把那期已接受的请求标为未生效；已有不同记录的期次作为冲突返回。
func TestApplyOfflineFreezes(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	freeze(t, s, period(0))
	_, err := submit(t, s, "r", period(1), 30)
	require.NoError(t, err)
	previous := func(at time.Time) time.Time { return at.Add(-time.Minute) }
	conflicts, err := s.ApplyOfflineFreezes(ctx, []domain.ParamFreeze{
		{SetID: "set", MemberAlias: "fast", PeriodTime: period(0), Snapshot: snapshotOf(99)},
		{SetID: "set", MemberAlias: "fast", PeriodTime: period(1), Snapshot: snapshotOf(20)},
	}, previous)
	require.NoError(t, err)
	require.Len(t, conflicts, 1)
	require.Equal(t, period(0), conflicts[0].PeriodTime)
	require.Equal(t, 20.0, conflicts[0].Snapshot.Value("window", "BTC"))

	through, ok, err := s.FrozenThrough(ctx, "set")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, period(1), through)
	got := freeze(t, s, period(1))
	require.Equal(t, domain.ParamsCarriedOffline, got.Source)
	requests, err := s.ListParamRequests(ctx, "set", "fast", period(1), period(1), 0)
	require.NoError(t, err)
	require.Equal(t, domain.ParamRequestNotApplied, requests[0].Status)
}

// 清理：过期请求、保留期之外的冻结区间，以及不再被引用的快照。
func TestCleanupParams(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for minute := 0; minute < 3; minute++ {
		freeze(t, s, period(minute))
	}
	_, err := submit(t, s, "r", period(3), 30)
	require.NoError(t, err)
	freeze(t, s, period(3))
	removed, err := s.CleanupParams(ctx, time.Now().Add(time.Hour), map[string]time.Time{"set": period(3)})
	require.NoError(t, err)
	require.Positive(t, removed)
	intervals, err := s.ListParamIntervals(ctx, "set", "", period(0), period(10))
	require.NoError(t, err)
	require.Len(t, intervals, 1)
	require.Equal(t, period(3), intervals[0].Start)
	var snapshots int64
	require.NoError(t, s.db.Raw("SELECT COUNT(*) FROM t_factor_param_snapshots").Scan(&snapshots).Error)
	require.Equal(t, int64(1), snapshots)
}

// 每个成员的最后一个区间在清理后仍保留：长期没有冻结之后恢复，沿用的基线不能丢。
func TestCleanupKeepsLastIntervalOfEachMember(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, err := submit(t, s, "r", period(0), 30)
	require.NoError(t, err)
	got := freeze(t, s, period(0))
	require.Equal(t, 30.0, got.Snapshot.Value("window", "BTC"))
	_, err = s.CleanupParams(ctx, time.Now().Add(time.Hour), map[string]time.Time{"set": period(100)})
	require.NoError(t, err)
	intervals, err := s.ListParamIntervals(ctx, "set", "", period(0), period(200))
	require.NoError(t, err)
	require.Len(t, intervals, 1)
	carried := freeze(t, s, period(101))
	require.Equal(t, domain.ParamsCarried, carried.Source)
	require.Equal(t, 30.0, carried.Snapshot.Value("window", "BTC"))
}

// 参数声明在两期之间变过（新增参数）：旧快照缺参数，不能沿用，改回当前声明的默认值。
func TestFreezeDoesNotCarryAcrossDeclarationChange(t *testing.T) {
	s := openTestStore(t)
	first := freeze(t, s, period(0))
	require.Equal(t, domain.ParamsDefault, first.Source)
	defaults := domain.ParamSnapshot{"window": {Default: 20}, "k": {Default: 2}}
	freezes, err := s.FreezeParams(context.Background(), "set", period(1), period(0), []FreezeMember{{Alias: "fast", Defaults: defaults}})
	require.NoError(t, err)
	require.Len(t, freezes, 1)
	require.Equal(t, domain.ParamsDefault, freezes[0].Source)
	require.Equal(t, 2.0, freezes[0].Snapshot.Value("k", "BTC"))
}
