package recalc

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/pyexec"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/stretchr/testify/require"
)

type recordingRunner struct {
	mu       sync.Mutex
	plans    []pipeline.Plan
	onRun    func(context.Context, pipeline.Plan) (pipeline.Outcome, error)
	progress []int64
	db       *store.Store
	jobID    string
}

func (r *recordingRunner) Run(ctx context.Context, plan pipeline.Plan) (pipeline.Outcome, error) {
	r.mu.Lock()
	r.plans = append(r.plans, plan)
	r.mu.Unlock()
	if r.db != nil {
		job, err := r.db.GetRecalcJob(ctx, r.jobID)
		if err != nil {
			return pipeline.Outcome{}, err
		}
		r.mu.Lock()
		r.progress = append(r.progress, job.ProgressTime)
		r.mu.Unlock()
	}
	if r.onRun != nil {
		return r.onRun(ctx, plan)
	}
	return pipeline.Outcome{Status: "complete"}, nil
}

func (r *recordingRunner) snapshot() []pipeline.Plan {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]pipeline.Plan(nil), r.plans...)
}

func (r *recordingRunner) progressSnapshot() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.progress...)
}

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

func pipelineSelection(factors ...recalcMember) Selection {
	defs := make([]domain.FactorDef, 0, len(factors))
	for _, factor := range factors {
		defs = append(defs, factor.def)
	}
	return Selection{
		Set: domain.FactorSet{
			SetID: "fset_bars_1m", SpaceID: "crypto", SourceDatasetID: "dataset_bars_1m", Freq: "1m",
			SubjectMode: domain.SubjectModeInclude, Subjects: []string{"BTC"},
			ResultDatasetID: "dataset_factor_bars_1m", Status: domain.SetStatusEnabled,
		},
		Factors: defs, Subjects: []string{"BTC"}, Columns: []string{"close"},
	}
}

func TestChunkReadRangeIncludesLookback(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	storage := &recalcPipelineStore{}
	runner := pipeline.NewRunner(storage, &recalcExecutor{}, periodclock.Continuous{}, pipeline.Config{PythonWorkers: 1})
	executor := NewExecutor(runner, WithClock(periodclock.Continuous{}), WithChunkPeriods(3))
	selection := pipelineSelection(
		testRecalcFactor("short", domain.MemberStatusEnabled, 2),
		testRecalcFactor("long", domain.MemberStatusEnabled, 3),
	)
	var records []progressRecord

	err := executor.Run(context.Background(), Window{Start: start, End: start.Add(3 * time.Minute)}, fixedSource(selection), recordProgress(&records, 0))

	require.NoError(t, err)
	require.Len(t, storage.readRequests, 1)
	require.Equal(t, start.Add(-2*time.Minute), storage.readRequests[0].Start)
	require.Equal(t, start.Add(3*time.Minute), storage.readRequests[0].End)
	require.Len(t, storage.written, 3)
	require.Equal(t, start, storage.written[0].DataTime)
	require.Equal(t, 7.0, storage.written[0].Fields["long_value"])
	require.Equal(t, 7.0, storage.written[0].Fields["short_value"])
}

func TestRecalcDoesNotReportMarker(t *testing.T) {
	storage := &recalcPipelineStore{}
	runner := pipeline.NewRunner(storage, &recalcExecutor{}, periodclock.Continuous{}, pipeline.Config{PythonWorkers: 1})
	executor := NewExecutor(runner, WithClock(periodclock.Continuous{}))
	period := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)

	_, err := executor.RunChunk(context.Background(), pipelineSelection(testRecalcFactor("close_factor", domain.MemberStatusEnabled, 3)), period, period.Add(time.Minute))

	require.NoError(t, err)
	require.Equal(t, 0, storage.markerCalls)
}

type recalcPipelineStore struct {
	readRequests []storageio.ReadRequest
	markerCalls  int
	written      []storageio.ResultRow
}

func (*recalcPipelineStore) DatasetColumns(context.Context, string, string) ([]string, error) {
	return []string{"close"}, nil
}

func (s *recalcPipelineStore) ReadWindow(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
	s.readRequests = append(s.readRequests, req)
	frame := &storageio.Frame{SubjectID: "BTC", Columns: []string{"data_time", "series_tag", "close"}}
	for at := req.Start; at.Before(req.End); at = at.Add(time.Minute) {
		frame.Rows = append(frame.Rows, []any{at, "source", 7.0})
	}
	return map[string]*storageio.Frame{"BTC": frame}, nil
}

func (s *recalcPipelineStore) WriteRows(_ context.Context, _, _, _ string, rows []storageio.ResultRow) error {
	s.written = append(s.written, rows...)
	return nil
}

func (s *recalcPipelineStore) ReportComputed(context.Context, storageio.PeriodMarker) error {
	s.markerCalls++
	return nil
}

func (*recalcPipelineStore) ComputedExists(context.Context, string, string, string, int64) (bool, error) {
	return false, nil
}

type recalcExecutor struct{}

func (*recalcExecutor) Exec(_ context.Context, req pyexec.Request) ([]pyexec.ItemResult, error) {
	targets, ok := req.Context["target_period_times"].([]string)
	if !ok {
		return nil, errors.New("recalc target periods are missing")
	}
	items := make([]pyexec.ItemResult, 0, len(req.Factors))
	for _, factor := range req.Factors {
		rows := make([][]any, 0, len(targets))
		for _, raw := range targets {
			at, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				return nil, err
			}
			rows = append(rows, []any{at, "source", 7.0})
		}
		items = append(items, pyexec.ItemResult{FactorID: factor.FactorID, Columns: []string{"data_time", "series_tag", factor.Outputs[0]}, Rows: rows})
	}
	return items, nil
}

func (*recalcExecutor) Busy() int    { return 0 }
func (*recalcExecutor) Close() error { return nil }

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
