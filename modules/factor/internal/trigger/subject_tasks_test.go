package trigger

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestBuildSubjectTasksUsesDefinitionTypeAndEventPartition(t *testing.T) {
	snapshot := domain.CatalogSnapshot{Revision: 1}
	for _, kind := range []string{domain.FactorTypeTimeSeries, domain.FactorTypeCrossSection} {
		snapshot.Factors = append(snapshot.Factors, domain.FactorDef{FactorID: kind, Name: kind, FactorType: kind, Status: domain.FactorStatusEnabled, SourceHash: "hash", LookbackPeriods: 20, InputColumns: []string{"close"}})
		snapshot.Bindings = append(snapshot.Bindings, domain.FactorBinding{BindingID: kind, BindingGeneration: "generation", FactorID: kind, SpaceID: "space", SourceViewID: "prices", Freq: "1m", SubjectMode: domain.SubjectModeAll, Status: domain.BindingStatusEnabled, ResultDatasetID: "results"})
	}
	event := subjectEvent("BTC")
	factorsDir := t.TempDir()
	tasks, err := BuildSubjectTasks(snapshot, event, factorsDir)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	task := tasks[0]
	require.EqualValues(t, 1, task.CatalogRevision)
	require.Equal(t, "node", task.SourceNodeID)
	require.Equal(t, "store", task.SourceStoreID)
	require.EqualValues(t, 1, task.SourceSequence)
	require.Equal(t, "BTC", task.SourceEventID)
	require.Equal(t, "subject_ready", task.TriggerType)
	require.Equal(t, domain.FactorTypeTimeSeries, task.Factor.FactorType)
	require.Equal(t, "BTC", task.SubjectID)
	require.True(t, task.FilterSourceSeriesTag)
	require.Empty(t, task.SourceSeriesTag)
	require.Equal(t, "schema:1", task.InputContractVersion)
	require.Empty(t, task.ExpectedActiveIndexID)
	require.Zero(t, task.ExpectedActiveIndexRevision)
	slotted := subjectEvent("BTC")
	slotted.Ready.ActiveIndexId = "view_slot_b"
	slottedTasks, err := BuildSubjectTasks(snapshot, slotted, factorsDir)
	require.NoError(t, err)
	require.Equal(t, task.TaskID, slottedTasks[0].TaskID)
	require.Empty(t, slottedTasks[0].ExpectedActiveIndexID)
	unslotted := subjectEvent("BTC")
	unslotted.Ready.ActiveIndexId = ""
	unslottedTasks, err := BuildSubjectTasks(snapshot, unslotted, factorsDir)
	require.NoError(t, err)
	require.Equal(t, task.TaskID, unslottedTasks[0].TaskID)
	require.Equal(t, time.Unix(60, 0).UTC(), task.StartTime)
	require.Equal(t, time.Unix(120, 0).UTC(), task.EndTime)
	event.Ready.SeriesTag = "venue:other"
	changed, err := BuildSubjectTasks(snapshot, event, t.TempDir())
	require.NoError(t, err)
	require.NotEqual(t, task.TaskID, changed[0].TaskID)
	snapshot.Factors[0].LookbackPeriods = domain.MaxTimeSeriesLookback + 1
	_, err = BuildSubjectTasks(snapshot, event, "")
	require.ErrorContains(t, err, "lookback_periods must not exceed")
	snapshot.Factors[0].LookbackPeriods = 20
	snapshot.Bindings[0].Status = domain.BindingStatusPendingView
	tasks, err = BuildSubjectTasks(snapshot, event, "")
	require.NoError(t, err)
	require.Empty(t, tasks)
	snapshot.Bindings = append(snapshot.Bindings, domain.FactorBinding{
		BindingID: "ready", BindingGeneration: "generation", FactorID: domain.FactorTypeTimeSeries,
		SpaceID: "space", SourceViewID: "prices", Freq: "1m", SubjectMode: domain.SubjectModeAll,
		Status: domain.BindingStatusEnabled, ResultDatasetID: "results",
	})
	tasks, err = BuildSubjectTasks(snapshot, event, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, "ready", tasks[0].BindingID)
	snapshot.Bindings = snapshot.Bindings[:1]
	snapshot.Bindings[0].Status = domain.BindingStatusEnabled
	snapshot.Bindings[0].SubjectMode, snapshot.Bindings[0].SubjectsJSON = domain.SubjectModeInclude, `["ETH"]`
	tasks, err = BuildSubjectTasks(snapshot, event, "")
	require.NoError(t, err)
	require.Empty(t, tasks)
}
