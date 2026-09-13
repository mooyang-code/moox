package trigger

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/modules/factor/internal/taskrunner"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/stretchr/testify/require"
)

func newSubjectLedger(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	return db
}

type subjectCatalogFunc func(context.Context) (*domain.CatalogSnapshot, error)

func (f subjectCatalogFunc) CatalogSnapshot(ctx context.Context) (*domain.CatalogSnapshot, error) {
	return f(ctx)
}

type subjectTasksFunc func(context.Context, []taskrunner.Task) []taskrunner.Result

func (f subjectTasksFunc) RunAll(ctx context.Context, tasks []taskrunner.Task) []taskrunner.Result {
	return f(ctx, tasks)
}

func TestSubjectRunnerHoldsSharedGateThroughCommit(t *testing.T) {
	gate := taskrunner.NewOperationGate()
	assertHeld := func(ctx context.Context) {
		check, cancel := context.WithTimeout(ctx, time.Millisecond)
		defer cancel()
		release, err := gate.AcquireContext(check)
		if release != nil {
			release()
		}
		require.ErrorIs(t, err, context.DeadlineExceeded)
	}
	snapshot := &domain.CatalogSnapshot{Revision: 7, Factors: []domain.FactorDef{{FactorID: "f", Name: "f", FactorType: domain.FactorTypeTimeSeries, Status: domain.FactorStatusEnabled, SourceHash: "hash", LookbackPeriods: 2}}, Bindings: []domain.FactorBinding{{BindingID: "b", BindingGeneration: "g", FactorID: "f", SpaceID: "space", SourceViewID: "prices", Freq: "1m", SubjectMode: domain.SubjectModeAll, Status: domain.BindingStatusEnabled, ResultDatasetID: "out"}}}
	catalogCalls, runCalls, commits := 0, 0, 0
	failure := errors.New("persist unavailable")
	r, err := NewSubjectRunner(subjectCatalogFunc(func(ctx context.Context) (*domain.CatalogSnapshot, error) {
		assertHeld(ctx)
		catalogCalls++
		return snapshot, nil
	}), newSubjectLedger(t), subjectTasksFunc(func(ctx context.Context, tasks []taskrunner.Task) []taskrunner.Result {
		assertHeld(ctx)
		runCalls++
		require.Len(t, tasks, 1)
		require.EqualValues(t, 7, tasks[0].CatalogRevision)
		require.Equal(t, "store", tasks[0].SourceStoreID)
		return []taskrunner.Result{{Task: tasks[0]}}
	}), gate, func(ctx context.Context, receipt SubjectBatchReceipt) error {
		revision, events, results := receipt.CatalogRevision, receipt.Events, receipt.Results
		assertHeld(ctx)
		commits++
		require.EqualValues(t, 7, revision)
		require.Len(t, events, 2)
		require.Len(t, results, 1)
		return failure
	}, t.TempDir())
	require.NoError(t, err)
	event := subjectEvent("BTC")
	require.ErrorIs(t, r.Execute(context.Background(), []SubjectEvent{event, event}), failure)
	require.Equal(t, 1, catalogCalls)
	require.Equal(t, 1, runCalls)
	require.Equal(t, 1, commits)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err := gate.AcquireContext(ctx)
	require.NoError(t, err)
	release()
}

func TestSubjectRunnerRejectsMissingResultBeforeCommit(t *testing.T) {
	snapshot := &domain.CatalogSnapshot{Revision: 1, Factors: []domain.FactorDef{{FactorID: "f", Name: "f", FactorType: domain.FactorTypeTimeSeries, Status: domain.FactorStatusEnabled, SourceHash: "hash", LookbackPeriods: 2}}, Bindings: []domain.FactorBinding{{BindingID: "b", BindingGeneration: "g", FactorID: "f", SpaceID: "space", SourceViewID: "prices", Freq: "1m", SubjectMode: domain.SubjectModeAll, Status: domain.BindingStatusEnabled, ResultDatasetID: "out"}}}
	r, err := NewSubjectRunner(subjectCatalogFunc(func(context.Context) (*domain.CatalogSnapshot, error) { return snapshot, nil }), newSubjectLedger(t), subjectTasksFunc(func(context.Context, []taskrunner.Task) []taskrunner.Result { return nil }), taskrunner.NewOperationGate(), func(context.Context, SubjectBatchReceipt) error {
		t.Fatal("missing results must not commit")
		return nil
	}, t.TempDir())
	require.NoError(t, err)
	require.ErrorContains(t, r.Execute(context.Background(), []SubjectEvent{subjectEvent("BTC")}), "omitted task results")
}

func TestSubjectRunnerAdmitsNewestRevisionAndReplaysCommitWithoutComputing(t *testing.T) {
	snapshot := &domain.CatalogSnapshot{Revision: 1, Factors: []domain.FactorDef{{FactorID: "f", Name: "f", FactorType: domain.FactorTypeTimeSeries, Status: domain.FactorStatusEnabled, SourceHash: "hash", LookbackPeriods: 2}}, Bindings: []domain.FactorBinding{{BindingID: "b", BindingGeneration: "g", FactorID: "f", SpaceID: "space", SourceViewID: "prices", Freq: "1m", SubjectMode: domain.SubjectModeAll, Status: domain.BindingStatusEnabled, ResultDatasetID: "out"}}}
	runs, commits := 0, 0
	failure := errors.New("receipt unavailable")
	ledger := newSubjectLedger(t)
	durableCommit, err := NewSubjectReceiptCommit(ledger)
	require.NoError(t, err)
	r, err := NewSubjectRunner(subjectCatalogFunc(func(context.Context) (*domain.CatalogSnapshot, error) { return snapshot, nil }), ledger, subjectTasksFunc(func(_ context.Context, tasks []taskrunner.Task) []taskrunner.Result {
		runs++
		require.Len(t, tasks, 1)
		require.EqualValues(t, 2, tasks[0].SourceSequence)
		return []taskrunner.Result{{Task: tasks[0]}}
	}), taskrunner.NewOperationGate(), func(ctx context.Context, receipt SubjectBatchReceipt) error {
		results := receipt.Results
		require.NotEmpty(t, receipt.Tasks, "receipts retain skipped task identities")
		commits++
		if commits == 1 {
			require.Len(t, results, 1)
			return failure
		}
		require.Empty(t, results, "completed task state is durable before event receipt retry")
		return durableCommit(ctx, receipt)
	}, t.TempDir())
	require.NoError(t, err)
	older, newer := subjectEvent("BTC"), subjectEvent("BTC")
	older.EventID, older.Ready.SourceEventId = "old", "source-old"
	newer.EventID, newer.Ready.SourceEventId, newer.Ready.SourceSequence = "new", "source-new", 2
	require.ErrorIs(t, r.Execute(context.Background(), []SubjectEvent{older, newer}), failure)
	require.NoError(t, r.Execute(context.Background(), []SubjectEvent{older, newer}))
	require.NoError(t, r.Execute(context.Background(), []SubjectEvent{older}))
	require.Equal(t, 1, runs)
	require.Equal(t, 3, commits)
	conflicting := subjectEvent("BTC")
	conflicting.Ready.SourceSequence = 3
	require.ErrorContains(t, r.Execute(context.Background(), []SubjectEvent{subjectEvent("BTC"), conflicting}), "different provenance")
	require.Equal(t, 1, runs)
}

func TestSubjectRunnerEmptyErrorRemainsRetryable(t *testing.T) {
	snapshot := &domain.CatalogSnapshot{Revision: 1, Factors: []domain.FactorDef{{FactorID: "f", Name: "f", FactorType: domain.FactorTypeTimeSeries, Status: domain.FactorStatusEnabled, SourceHash: "hash", LookbackPeriods: 2}}, Bindings: []domain.FactorBinding{{BindingID: "b", BindingGeneration: "g", FactorID: "f", SpaceID: "space", SourceViewID: "prices", Freq: "1m", SubjectMode: domain.SubjectModeAll, Status: domain.BindingStatusEnabled, ResultDatasetID: "out"}}}
	runs := 0
	r, err := NewSubjectRunner(subjectCatalogFunc(func(context.Context) (*domain.CatalogSnapshot, error) { return snapshot, nil }), newSubjectLedger(t), subjectTasksFunc(func(_ context.Context, tasks []taskrunner.Task) []taskrunner.Result {
		runs++
		result := taskrunner.Result{Task: tasks[0]}
		if runs == 1 {
			result.Err = errors.New("")
		}
		return []taskrunner.Result{result}
	}), taskrunner.NewOperationGate(), func(context.Context, SubjectBatchReceipt) error { return nil }, t.TempDir())
	require.NoError(t, err)
	require.Error(t, r.Execute(context.Background(), []SubjectEvent{subjectEvent("BTC")}))
	require.NoError(t, r.Execute(context.Background(), []SubjectEvent{subjectEvent("BTC")}))
	require.Equal(t, 2, runs)
}
