package recalc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/stretchr/testify/require"
)

func TestSubmitIsIdempotentByRequestID(t *testing.T) {
	db := openRecalcStore(t)
	seedRecalcTask(t, db,
		testRecalcFactor("close_factor", domain.MemberStatusEnabled, 3),
	)
	svc := NewService(db, WithSubjectProvider(recalcSubjects{"BTC"}))
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)
	one, err := svc.Submit(context.Background(), "ftask_bars_1m", nil, nil, "req-1", start, end)
	require.NoError(t, err)
	two, err := svc.Submit(context.Background(), "ftask_bars_1m", nil, nil, "req-1", start, end)
	require.NoError(t, err)
	require.Equal(t, one, two)
}

func TestSubmitRetriesOmittedSelectorsAfterEnvironmentChanges(t *testing.T) {
	db := openRecalcStore(t)
	seedRecalcTask(t, db,
		testRecalcFactor("close_factor", domain.MemberStatusEnabled, 3),
	)
	subjects := &changingSubjects{values: []string{"BTC"}}
	svc := NewService(db, WithSubjectProvider(subjects))
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)
	accepted, err := svc.Submit(context.Background(), "ftask_bars_1m", nil, nil, "stable-omitted", start, end)
	require.NoError(t, err)
	require.Equal(t, []string{"close_factor"}, accepted.Members)
	require.Equal(t, []string{"BTC"}, accepted.Subjects)

	addRecalcMember(t, db, "ftask_bars_1m", testRecalcFactor("new_factor", domain.MemberStatusEnabled, 1))
	subjects.values = []string{"BTC", "ETH"}
	retried, err := svc.Submit(context.Background(), "ftask_bars_1m", nil, nil, "stable-omitted", start, end)
	require.NoError(t, err)
	require.Equal(t, accepted, retried)

	_, err = svc.Submit(context.Background(), "ftask_bars_1m", []string{"close_factor"}, nil, "stable-omitted", start, end)
	require.ErrorContains(t, err, "different recalc request")
}

type changingSubjects struct{ values []string }

func (s *changingSubjects) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return append([]string(nil), s.values...), nil
}

type emptySubjects struct{}

func (emptySubjects) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return nil, nil
}

func TestRecalcUsesSelectedFactorsOnly(t *testing.T) {
	db := openRecalcStore(t)
	seedRecalcTask(t, db,
		testRecalcFactor("one", domain.MemberStatusEnabled, 2),
		testRecalcFactor("two", domain.MemberStatusEnabled, 5),
	)
	svc := NewService(db, WithSubjectProvider(recalcSubjects{"BTC"}))
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	job, err := svc.Submit(context.Background(), "ftask_bars_1m", []string{"two"}, nil, "req-1", start, start.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, []string{"two"}, job.Members)
	require.Equal(t, []string{"BTC"}, job.Subjects)
}

func openRecalcStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(&store.Options{Path: t.TempDir() + "/factor.db"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

// recalcMember is a test definition together with the member status it gets in the set.
type recalcMember struct {
	def    domain.FactorDef
	status string
}

func seedRecalcTask(t *testing.T, db *store.Store, members ...recalcMember) {
	t.Helper()
	set := domain.FactorTask{
		TaskID: "ftask_bars_1m", SpaceID: "crypto", Name: "行情因子", SourceDatasetID: "dataset_bars_1m",
		Freq:            "1m",
		ResultDatasetID: "dataset_factor_bars_1m", Status: domain.TaskStatusEnabled,
	}
	require.NoError(t, db.CreateTask(context.Background(), set))
	for _, member := range members {
		addRecalcMember(t, db, set.TaskID, member)
	}
}

func addRecalcMember(t *testing.T, db *store.Store, taskID string, member recalcMember) {
	t.Helper()
	require.NoError(t, db.CreateFactor(context.Background(), member.def))
	_, err := db.AddMember(context.Background(), taskID, member.def.FactorID, member.def.FactorID, domain.MemberKindFixed, nil)
	require.NoError(t, err)
	if member.status == domain.MemberStatusEnabled {
		require.NoError(t, db.SetMemberStatus(context.Background(), taskID, member.def.FactorID,
			domain.MemberStatusDisabled, domain.MemberStatusEnabled))
	}
}

func testRecalcFactor(id, status string, lookback int) recalcMember {
	return recalcMember{status: status, def: domain.MustDefinition(id, id,
		fmt.Sprintf("output: ts_mean(close, %d)\n", lookback))}
}

func TestRecalcRejectsFactorThatIsNotAMember(t *testing.T) {
	db := openRecalcStore(t)
	seedRecalcTask(t, db,
		testRecalcFactor("close_factor", domain.MemberStatusEnabled, 3),
	)
	// A definition that exists but was never added to this set.
	require.NoError(t, db.CreateFactor(context.Background(), testRecalcFactor("outsider", "", 1).def))
	svc := NewService(db, WithSubjectProvider(recalcSubjects{"BTC"}))
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)

	_, err := svc.Submit(context.Background(), "ftask_bars_1m", []string{"outsider"}, nil, "req-outsider", start, start.Add(time.Minute))
	require.ErrorContains(t, err, "不在因子任务里")
}

func TestRecalcRejectsDisabledMember(t *testing.T) {
	db := openRecalcStore(t)
	seedRecalcTask(t, db,
		testRecalcFactor("close_factor", domain.MemberStatusEnabled, 3),
		testRecalcFactor("parked", domain.MemberStatusDisabled, 3),
	)
	svc := NewService(db, WithSubjectProvider(recalcSubjects{"BTC"}))
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)

	_, err := svc.Submit(context.Background(), "ftask_bars_1m", []string{"parked"}, nil, "req-parked", start, start.Add(time.Minute))
	require.ErrorContains(t, err, "not enabled")
}

func TestRecalcDefaultsToAllEnabledMembers(t *testing.T) {
	db := openRecalcStore(t)
	seedRecalcTask(t, db,
		testRecalcFactor("a_factor", domain.MemberStatusEnabled, 3),
		testRecalcFactor("b_parked", domain.MemberStatusDisabled, 3),
		testRecalcFactor("c_factor", domain.MemberStatusEnabled, 3),
	)
	svc := NewService(db, WithSubjectProvider(recalcSubjects{"BTC"}))
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)

	job, err := svc.Submit(context.Background(), "ftask_bars_1m", nil, nil, "req-default", start, start.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, []string{"a_factor", "c_factor"}, job.Members)
}

// recalcSubjects 是测试用的源数据集标的清单。
type recalcSubjects []string

func (r recalcSubjects) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return append([]string(nil), r...), nil
}

// A 股补算区间吸附到交易 K 线：起点取不早于它的第一根开盘，终点取不晚于它的最近收盘，按真实时间入库；
// 吸附后区间里没有 K 线才报错。crypto 仍严格按周期对齐。
func TestAlignRangeSnapsStockCNToTradingBars(t *testing.T) {
	cst := time.FixedZone("CST", 8*3600)
	at := func(value string) time.Time {
		parsed, err := time.ParseInLocation("2006-01-02 15:04", value, cst)
		require.NoError(t, err)
		return parsed.UTC()
	}
	for _, c := range []struct{ start, end, wantStart, wantEnd string }{
		// 交易日 10:07 打开页面的默认区间：终点 10:07 吸附到 10:05（10:00 那根收盘），起点 08:47 吸附到 09:30 开盘。
		{"2026-10-09 08:47", "2026-10-09 10:07", "2026-10-09 09:30", "2026-10-09 10:05"},
		// 终点 20:00 吸附到当天 15:00 收盘。
		{"2026-10-09 13:00", "2026-10-09 20:00", "2026-10-09 13:00", "2026-10-09 15:00"},
		// 起点在周日：吸附到周一 09:30 开盘。
		{"2026-10-11 10:00", "2026-10-12 10:00", "2026-10-12 09:30", "2026-10-12 10:00"},
		// 起点在盘中（10:07）：吸附到下一根 10:10；终点 13:00 是午休后第一根的开盘，吸附到 11:30 收盘。
		{"2026-10-09 10:07", "2026-10-09 13:00", "2026-10-09 10:10", "2026-10-09 11:30"},
		// 已经对齐的区间不变；日历最后一天的收盘能作终点。
		{"2027-12-31 14:00", "2027-12-31 15:00", "2027-12-31 14:00", "2027-12-31 15:00"},
	} {
		start, end, err := AlignRange("stockcn", "5m", at(c.start), at(c.end))
		require.NoError(t, err, c)
		require.Equal(t, at(c.wantStart), start, c)
		require.Equal(t, at(c.wantEnd), end, c)
	}
	// 午休、周末里没有 K 线。
	_, _, err := AlignRange("stockcn", "5m", at("2026-10-09 11:40"), at("2026-10-09 12:50"))
	require.Error(t, err)
	_, _, err = AlignRange("stockcn", "5m", at("2026-10-10 10:00"), at("2026-10-11 20:00"))
	require.Error(t, err)

	// crypto 不吸附：没对齐就拒绝。
	_, _, err = AlignRange("crypto", "5m", at("2026-10-09 10:07"), at("2026-10-09 11:00"))
	require.Error(t, err)
	start, end, err := AlignRange("crypto", "5m", at("2026-10-09 10:05"), at("2026-10-09 11:00"))
	require.NoError(t, err)
	require.Equal(t, at("2026-10-09 10:05"), start)
	require.Equal(t, at("2026-10-09 11:00"), end)
}
