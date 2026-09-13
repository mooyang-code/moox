package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
)

func TestSubjectReceiptUsesDurableOutcomesAndIsReplayable(t *testing.T) {
	db, err := Open(&Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	ctx := context.Background()
	source := &storagepb.ViewSourceSubjectReady{SourceViewId: "v", SourceDatasetId: "bars", ActiveIndexId: "idx", SubjectId: "BTC", Frequency: "1m", PeriodTime: 60, InputContractVersion: "hash:1", SourceNodeId: "node", SourceStoreId: "store", SourceSequence: 1, SourceEventId: "source"}
	task := engine.FactorTask{TaskID: "task", TriggerEventID: "event", BindingID: "b", BindingGeneration: "g", SpaceID: "s", SourceViewID: "v", SubjectID: "BTC", Freq: "1m", PeriodTime: 60, InputContractVersion: "hash:1", FilterSourceSeriesTag: true, CatalogRevision: 1, SourceNodeID: "node", SourceStoreID: "store", SourceSequence: 1, SourceEventID: "source"}
	input := SubjectReceiptInput{SpaceID: "s", EventID: "event", CatalogRevision: 1, Source: source, Tasks: []engine.FactorTask{task}}
	task.ExpectedActiveIndexID = "idx"
	input.Tasks[0] = task
	_, err = db.AdmitSubjectTask(ctx, task)
	require.NoError(t, err)
	require.ErrorContains(t, db.CommitSubjectReceipt(ctx, input), "no persisted outcome")
	require.NoError(t, db.CompleteSubjectTask(ctx, task, "retryable"))
	require.NoError(t, db.CommitSubjectReceipt(ctx, input))
	var receipt subjectReceipt
	require.NoError(t, db.db.Take(&receipt).Error)
	require.Equal(t, "failed", receipt.Status)
	_, err = db.AdmitSubjectTask(ctx, task)
	require.NoError(t, err)
	require.NoError(t, db.CompleteSubjectTask(ctx, task, ""))
	for i := 0; i < 2; i++ {
		require.NoError(t, db.CommitSubjectReceipt(ctx, input))
	}
	require.NoError(t, db.db.Take(&receipt).Error)
	require.Equal(t, "complete", receipt.Status)
	var outcomes map[string]string
	require.NoError(t, json.Unmarshal([]byte(receipt.OutcomesJSON), &outcomes))
	require.Equal(t, map[string]string{"task": "complete"}, outcomes)
	dropped := input
	dropped.Tasks = nil
	require.ErrorContains(t, db.CommitSubjectReceipt(ctx, dropped), "task set changed")
	newer := task
	newer.TaskID, newer.SourceSequence, newer.SourceEventID = "newer", 2, "source-newer"
	_, err = db.AdmitSubjectTask(ctx, newer)
	require.NoError(t, err)
	require.NoError(t, db.CommitSubjectReceipt(ctx, input))
	require.NoError(t, db.db.Take(&receipt).Error)
	require.Equal(t, "superseded", receipt.Status, "newer pending head is not a complete source result")
	source.SourceDatasetId = "reused-event"
	require.ErrorContains(t, db.CommitSubjectReceipt(ctx, input), "identity was reused")
	input.CatalogRevision++
	input.Tasks = nil
	require.ErrorContains(t, db.CommitSubjectReceipt(ctx, input), "identity was reused", "catalog updates must not reset source event identity")
}
