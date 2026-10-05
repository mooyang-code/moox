package recalc

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/stretchr/testify/require"
)

func TestSubmitIsIdempotentByRequestID(t *testing.T) {
	db := openRecalcStore(t)
	seedRecalcSet(t, db, domain.SubjectModeInclude, []string{"BTC"},
		testRecalcFactor("close_factor", domain.MemberStatusEnabled, 3),
	)
	svc := NewService(db, WithClock(periodclock.Continuous{}))
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)
	one, err := svc.Submit(context.Background(), "fset_bars_1m", nil, nil, "req-1", start, end)
	require.NoError(t, err)
	two, err := svc.Submit(context.Background(), "fset_bars_1m", nil, nil, "req-1", start, end)
	require.NoError(t, err)
	require.Equal(t, one, two)
}

func TestSubmitRetriesOmittedSelectorsAfterEnvironmentChanges(t *testing.T) {
	db := openRecalcStore(t)
	seedRecalcSet(t, db, domain.SubjectModeAll, nil,
		testRecalcFactor("close_factor", domain.MemberStatusEnabled, 3),
	)
	subjects := &changingSubjects{values: []string{"BTC"}}
	svc := NewService(db, WithClock(periodclock.Continuous{}), WithSubjectProvider(subjects))
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)
	accepted, err := svc.Submit(context.Background(), "fset_bars_1m", nil, nil, "stable-omitted", start, end)
	require.NoError(t, err)
	require.Equal(t, []string{"close_factor"}, accepted.FactorIDs)
	require.Equal(t, []string{"BTC"}, accepted.Subjects)

	addRecalcMember(t, db, "fset_bars_1m", testRecalcFactor("new_factor", domain.MemberStatusEnabled, 1))
	subjects.values = []string{"BTC", "ETH"}
	retried, err := svc.Submit(context.Background(), "fset_bars_1m", nil, nil, "stable-omitted", start, end)
	require.NoError(t, err)
	require.Equal(t, accepted, retried)

	_, err = svc.Submit(context.Background(), "fset_bars_1m", []string{"close_factor"}, nil, "stable-omitted", start, end)
	require.ErrorContains(t, err, "different recalc request")
}

type changingSubjects struct{ values []string }

func (s *changingSubjects) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return append([]string(nil), s.values...), nil
}

func TestEnableBackfillAcceptsDisabledFactorWithAtomicVisibility(t *testing.T) {
	db := openRecalcStore(t)
	factor := testRecalcFactor("new_factor", domain.MemberStatusDisabled, 2)
	seedRecalcSet(t, db, domain.SubjectModeInclude, []string{"BTC"}, factor)
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	svc := NewService(db, WithClock(periodclock.Continuous{}))
	job, err := svc.PrepareEnableBackfill(context.Background(), "fset_bars_1m", "new_factor", "enable-new-factor", start, start.Add(3*time.Minute))
	require.NoError(t, err)
	require.Equal(t, []string{"new_factor"}, job.FactorIDs)
	require.Equal(t, []string{"BTC"}, job.Subjects)
	stored, err := db.EnableMemberWithRecalcJob(context.Background(), "fset_bars_1m", "new_factor", domain.MemberStatusDisabled, job)
	require.NoError(t, err)
	require.Equal(t, store.RecalcStatusAccepted, stored.Status)
	active, err := db.GetMember(context.Background(), "fset_bars_1m", "new_factor")
	require.NoError(t, err)
	require.Equal(t, domain.MemberStatusEnabled, active.Status)
}

func TestEnableBackfillWithEmptyDatasetCompletesAsNoop(t *testing.T) {
	db := openRecalcStore(t)
	factor := testRecalcFactor("new_factor", domain.MemberStatusDisabled, 2)
	seedRecalcSet(t, db, domain.SubjectModeAll, nil, factor)
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	svc := NewService(db, WithClock(periodclock.Continuous{}), WithSubjectProvider(emptySubjects{}))
	job, err := svc.PrepareEnableBackfill(context.Background(), "fset_bars_1m", "new_factor", "enable-empty", start, start.Add(time.Minute))
	require.NoError(t, err)
	require.Empty(t, job.Subjects, "an empty source dataset yields a job the engine completes without computing")
	stored, err := db.EnableMemberWithRecalcJob(context.Background(), "fset_bars_1m", "new_factor", domain.MemberStatusDisabled, job)
	require.NoError(t, err)
	require.Equal(t, store.RecalcStatusAccepted, stored.Status)
}

type emptySubjects struct{}

func (emptySubjects) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return nil, nil
}

func TestRecalcUsesSelectedFactorsOnly(t *testing.T) {
	db := openRecalcStore(t)
	seedRecalcSet(t, db, domain.SubjectModeInclude, []string{"BTC"},
		testRecalcFactor("one", domain.MemberStatusEnabled, 2),
		testRecalcFactor("two", domain.MemberStatusEnabled, 5),
	)
	svc := NewService(db, WithClock(periodclock.Continuous{}))
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	job, err := svc.Submit(context.Background(), "fset_bars_1m", []string{"two"}, nil, "req-1", start, start.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, []string{"two"}, job.FactorIDs)
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

func seedRecalcSet(t *testing.T, db *store.Store, mode string, subjects []string, members ...recalcMember) {
	t.Helper()
	set := domain.FactorSet{
		SetID: "fset_bars_1m", SpaceID: "crypto", SourceDatasetID: "dataset_bars_1m",
		Freq: "1m", SubjectMode: mode, Subjects: subjects,
		ResultDatasetID: "dataset_factor_bars_1m", Status: domain.SetStatusEnabled,
	}
	require.NoError(t, db.CreateSet(context.Background(), set))
	for _, member := range members {
		addRecalcMember(t, db, set.SetID, member)
	}
}

func addRecalcMember(t *testing.T, db *store.Store, setID string, member recalcMember) {
	t.Helper()
	require.NoError(t, db.CreateFactor(context.Background(), member.def))
	_, err := db.AddMember(context.Background(), setID, member.def.FactorID)
	require.NoError(t, err)
	if member.status == domain.MemberStatusEnabled {
		require.NoError(t, db.SetMemberStatus(context.Background(), setID, member.def.FactorID,
			domain.MemberStatusDisabled, domain.MemberStatusEnabled))
	}
}

func testRecalcFactor(id, status string, lookback int) recalcMember {
	return recalcMember{status: status, def: domain.FactorDef{
		FactorID: id, Name: id, FactorType: domain.FactorTypeTimeSeries,
		SourceCode: "def compute(df, params, context): return df", SourceHash: domain.SourceHash("def compute(df, params, context): return df"),
		InputColumns: []string{"close"}, Outputs: []string{id + "_value"}, ParamsJSON: "{}",
		LookbackPeriods: lookback,
	}}
}

func TestRecalcRejectsFactorThatIsNotAMember(t *testing.T) {
	db := openRecalcStore(t)
	seedRecalcSet(t, db, domain.SubjectModeInclude, []string{"BTC"},
		testRecalcFactor("close_factor", domain.MemberStatusEnabled, 3),
	)
	// A definition that exists but was never added to this set.
	require.NoError(t, db.CreateFactor(context.Background(), testRecalcFactor("outsider", "", 1).def))
	svc := NewService(db, WithClock(periodclock.Continuous{}))
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)

	_, err := svc.Submit(context.Background(), "fset_bars_1m", []string{"outsider"}, nil, "req-outsider", start, start.Add(time.Minute))
	require.ErrorContains(t, err, "not a member")
}

func TestRecalcRejectsDisabledMember(t *testing.T) {
	db := openRecalcStore(t)
	seedRecalcSet(t, db, domain.SubjectModeInclude, []string{"BTC"},
		testRecalcFactor("close_factor", domain.MemberStatusEnabled, 3),
		testRecalcFactor("parked", domain.MemberStatusDisabled, 3),
	)
	svc := NewService(db, WithClock(periodclock.Continuous{}))
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)

	_, err := svc.Submit(context.Background(), "fset_bars_1m", []string{"parked"}, nil, "req-parked", start, start.Add(time.Minute))
	require.ErrorContains(t, err, "not enabled")
}

func TestRecalcDefaultsToAllEnabledMembers(t *testing.T) {
	db := openRecalcStore(t)
	seedRecalcSet(t, db, domain.SubjectModeInclude, []string{"BTC"},
		testRecalcFactor("a_factor", domain.MemberStatusEnabled, 3),
		testRecalcFactor("b_parked", domain.MemberStatusDisabled, 3),
		testRecalcFactor("c_factor", domain.MemberStatusEnabled, 3),
	)
	svc := NewService(db, WithClock(periodclock.Continuous{}))
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)

	job, err := svc.Submit(context.Background(), "fset_bars_1m", nil, nil, "req-default", start, start.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, []string{"a_factor", "c_factor"}, job.FactorIDs)
}
