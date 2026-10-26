package recalc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/stretchr/testify/require"
)

func TestSubmitIsIdempotentByRequestID(t *testing.T) {
	db := openRecalcStore(t)
	seedRecalcTask(t, db,
		testRecalcFactor("close_factor", domain.MemberStatusEnabled, 3),
	)
	svc := NewService(db, WithClock(periodclock.Continuous{}), WithSubjectProvider(recalcSubjects{"BTC"}))
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
	svc := NewService(db, WithClock(periodclock.Continuous{}), WithSubjectProvider(subjects))
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
	svc := NewService(db, WithClock(periodclock.Continuous{}), WithSubjectProvider(recalcSubjects{"BTC"}))
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
	svc := NewService(db, WithClock(periodclock.Continuous{}), WithSubjectProvider(recalcSubjects{"BTC"}))
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
	svc := NewService(db, WithClock(periodclock.Continuous{}), WithSubjectProvider(recalcSubjects{"BTC"}))
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
	svc := NewService(db, WithClock(periodclock.Continuous{}), WithSubjectProvider(recalcSubjects{"BTC"}))
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
