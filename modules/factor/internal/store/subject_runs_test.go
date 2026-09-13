package store

import (
	"context"
	"math"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/stretchr/testify/require"
)

func TestSubjectAdmissionSurvivesRestartAndFencesOldSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factor.db")
	db, err := Open(&Options{Path: path})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	ctx := context.Background()
	task := engine.FactorTask{TaskID: "task-1", BindingID: "b", BindingGeneration: "g", SpaceID: "s", SourceViewID: "v", SubjectID: "BTC", Freq: "1m", PeriodTime: 60, InputContractVersion: "hash:1", FilterSourceSeriesTag: true, CatalogRevision: 1, SourceNodeID: "node", SourceStoreID: "store", SourceSequence: 1, SourceEventID: "source-1"}
	admitted, err := db.AdmitSubjectTask(ctx, task)
	require.NoError(t, err)
	require.True(t, admitted)
	require.NoError(t, db.Close())
	db, err = Open(&Options{Path: path})
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	admitted, err = db.AdmitSubjectTask(ctx, task)
	require.NoError(t, err)
	require.True(t, admitted, "pending work must replay after restart")
	require.NoError(t, db.CompleteSubjectTask(ctx, task, ""))
	admitted, err = db.AdmitSubjectTask(ctx, task)
	require.NoError(t, err)
	require.False(t, admitted, "completed work must not run again")
	newer := task
	newer.TaskID, newer.SourceEventID, newer.SourceSequence = "task-2", "source-2", 2
	admitted, err = db.AdmitSubjectTask(ctx, newer)
	require.NoError(t, err)
	require.True(t, admitted)
	admitted, err = db.AdmitSubjectTask(ctx, task)
	require.NoError(t, err)
	require.False(t, admitted, "older event must not write over newer admission")
	require.ErrorContains(t, db.CompleteSubjectTask(ctx, task, ""), "no longer")
	require.NoError(t, db.CompleteSubjectTask(ctx, newer, "temporary write failure"))
	admitted, err = db.AdmitSubjectTask(ctx, newer)
	require.NoError(t, err)
	require.True(t, admitted)
	otherStore := newer
	otherStore.SourceStoreID = "recreated"
	_, err = db.AdmitSubjectTask(ctx, otherStore)
	require.ErrorContains(t, err, "incomparable")
	alias := newer
	alias.SourceEventID = "reused-sequence"
	_, err = db.AdmitSubjectTask(ctx, alias)
	require.ErrorContains(t, err, "reused")
	otherTag := task
	otherTag.TaskID, otherTag.SourceSeriesTag = "other-tag", "venue:other"
	admitted, err = db.AdmitSubjectTask(ctx, otherTag)
	require.NoError(t, err)
	require.True(t, admitted)
	maximum := newer
	maximum.TaskID, maximum.SourceEventID, maximum.SourceSequence = "max", "source-max", math.MaxUint64
	admitted, err = db.AdmitSubjectTask(ctx, maximum)
	require.NoError(t, err)
	require.True(t, admitted)
	require.NoError(t, db.CompleteSubjectTask(ctx, maximum, ""))
}

func TestSubjectAdmissionRollsBackHeadWhenRunPersistenceFails(t *testing.T) {
	db, err := Open(&Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	require.NoError(t, db.db.Exec("CREATE TRIGGER reject_subject_run BEFORE INSERT ON t_factor_subject_runs BEGIN SELECT RAISE(ABORT, 'injected persistence failure'); END;").Error)
	task := engine.FactorTask{TaskID: "task", BindingID: "b", BindingGeneration: "g", SpaceID: "s", SourceViewID: "v", SubjectID: "BTC", Freq: "1m", PeriodTime: 60, InputContractVersion: "hash:1", FilterSourceSeriesTag: true, CatalogRevision: 1, SourceNodeID: "node", SourceStoreID: "store", SourceSequence: 1, SourceEventID: "source"}
	admitted, err := db.AdmitSubjectTask(context.Background(), task)
	require.ErrorContains(t, err, "injected persistence failure")
	require.False(t, admitted)
	var count int64
	require.NoError(t, db.db.Model(&subjectHead{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestSubjectAdmissionRevertedDefinitionMustRestoreItsOutput(t *testing.T) {
	db, err := Open(&Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	ctx := context.Background()
	task := engine.FactorTask{TaskID: "definition-a", BindingID: "b", BindingGeneration: "g", SpaceID: "s", SourceViewID: "v", SubjectID: "BTC", Freq: "1m", PeriodTime: 60, InputContractVersion: "hash:1", FilterSourceSeriesTag: true, CatalogRevision: 1, SourceNodeID: "node", SourceStoreID: "store", SourceSequence: 1, SourceEventID: "source"}
	for _, version := range []struct {
		id       string
		revision int64
	}{{"definition-a", 1}, {"definition-b", 2}, {"definition-a", 3}} {
		task.TaskID, task.CatalogRevision = version.id, version.revision
		admitted, err := db.AdmitSubjectTask(ctx, task)
		require.NoError(t, err)
		require.True(t, admitted)
		require.NoError(t, db.CompleteSubjectTask(ctx, task, ""))
	}
	task.CatalogRevision = 4
	admitted, err := db.AdmitSubjectTask(ctx, task)
	require.NoError(t, err)
	require.False(t, admitted)
	task.TaskID, task.CatalogRevision, task.SourceSequence = "old-catalog-new-source", 3, 2
	admitted, err = db.AdmitSubjectTask(ctx, task)
	require.NoError(t, err)
	require.False(t, admitted, "new source sequence cannot bypass catalog fence")
}

func TestSubjectLedgerGCRequiresCompletionAndRetainsReplayFence(t *testing.T) {
	db, err := Open(&Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	ctx := context.Background()
	task := engine.FactorTask{TaskID: "task", BindingID: "b", BindingGeneration: "g", SpaceID: "s", SourceViewID: "v", SubjectID: "BTC", Freq: "1m", PeriodTime: 60, InputContractVersion: "hash:1", FilterSourceSeriesTag: true, CatalogRevision: 1, SourceNodeID: "node", SourceStoreID: "store", SourceSequence: 1, SourceEventID: "source"}
	_, err = db.AdmitSubjectTask(ctx, task)
	require.NoError(t, err)
	require.ErrorContains(t, db.PruneSubjectRunsBefore(ctx, 120), "unfinished")
	task.TaskID, task.SourceSequence, task.SourceEventID = "replacement", 2, "replacement-source"
	_, err = db.AdmitSubjectTask(ctx, task)
	require.NoError(t, err)
	require.NoError(t, db.CompleteSubjectTask(ctx, task, ""))
	require.NoError(t, db.PruneSubjectRunsBefore(ctx, 120))
	var count int64
	require.NoError(t, db.db.Model(&subjectRun{}).Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, db.db.Model(&subjectHead{}).Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, db.PruneSubjectRunsBefore(ctx, 30), "barrier cannot move backwards")
	task.TaskID, task.SourceSequence = "late-correction", 2
	admitted, err := db.AdmitSubjectTask(ctx, task)
	require.NoError(t, err)
	require.False(t, admitted)
	task.PeriodTime = 120
	admitted, err = db.AdmitSubjectTask(ctx, task)
	require.NoError(t, err)
	require.True(t, admitted, "barrier is exclusive")
}
