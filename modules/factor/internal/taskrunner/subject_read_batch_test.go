package taskrunner

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

func TestReadGroupsDoNotMergeBeyondSingleSubjectCellBudget(t *testing.T) {
	a := oneBarTask("BTC", time.Unix(60, 0))
	a.PeriodTime, a.TriggerType, a.InputContractVersion = 60, "subject_ready", "schema:1"
	a.LookbackPeriods = 10000
	a.Factor.InputColumns = make([]string, 30)
	for i := range a.Factor.InputColumns {
		a.Factor.InputColumns[i] = fmt.Sprintf("a%d", i)
	}
	b := a
	b.Factor.InputColumns = make([]string, 30)
	for i := range b.Factor.InputColumns {
		b.Factor.InputColumns[i] = fmt.Sprintf("b%d", i)
	}
	groups, singles := buildPeriodReadGroups([]Task{a, b})
	require.Empty(t, singles)
	require.Len(t, groups, 2, "individually valid factors must not become one impossible read")
	batches := clusterPeriodReadGroups(groups)
	require.Len(t, batches, 2)
	for _, batch := range batches {
		require.Len(t, batch.columns, 30)
	}
	b.Factor.InputColumns = a.Factor.InputColumns
	groups, _ = buildPeriodReadGroups([]Task{a, b})
	require.Len(t, groups, 1)
	require.Len(t, clusterPeriodReadGroups(groups), 1)
}

func TestSubjectReadBatchSharesInputsWithoutReplacingEventIdentity(t *testing.T) {
	start := time.Unix(60, 0)
	makeTask := func(subject, event, contract string) Task {
		return Task{TriggerType: "subject_ready", FactorTask: engine.FactorTask{
			SpaceID: "space", SourceViewID: "prices", SourceDataset: "prices", SubjectID: subject,
			Freq: "1m", PeriodTime: 60, StartTime: start, EndTime: start.Add(time.Minute),
			ExpectedActiveIndexID: "index", InputContractVersion: contract, TriggerEventID: event,
			LookbackPeriods: 20, Factor: engine.FactorSpec{InputColumns: []string{"close"}},
		}}
	}
	tasks := []Task{makeTask("BTC", "btc-event", "schema:1"), makeTask("ETH", "eth-event", "schema:1"), makeTask("SOL", "sol-event", "schema:2")}
	groups, singles := buildPeriodReadGroups(tasks)
	require.Empty(t, singles)
	batches := clusterPeriodReadGroups(groups)
	require.Len(t, batches, 2)
	require.Len(t, batches[0].groups, 2)
	require.Equal(t, "btc-event", batches[0].groups[0].members[0].task.TriggerEventID)
	require.Equal(t, "eth-event", batches[0].groups[1].members[0].task.TriggerEventID)
	// Without an explicit contract, unrelated events must not share a read.
	tasks[0].InputContractVersion, tasks[1].InputContractVersion = "", ""
	groups, _ = buildPeriodReadGroups(tasks[:2])
	require.Len(t, clusterPeriodReadGroups(groups), 2)
}

func TestRunAllSubjectEventsUseOneBulkRead(t *testing.T) {
	storage := &batchPeriodReadStorage{chunks: map[string]*storageio.RangeChunk{}}
	base := time.Unix(60, 0).UTC()
	var tasks []Task
	for _, subject := range []string{"BTC", "ETH"} {
		storage.chunks[subject] = &storageio.RangeChunk{Frame: &engine.DataFrame{
			Columns: []string{"close"}, Rows: [][]any{{1.0}}, DataTimes: []time.Time{base},
		}, TargetPeriods: []time.Time{base}, Complete: true}
		task := oneBarTask(subject, base)
		task.PeriodTime = base.Unix()
		task.TriggerType = "subject_ready"
		task.TriggerEventID = subject + "-source-event"
		task.InputContractVersion = "schema:1"
		tasks = append(tasks, task)
	}
	runner := NewService(2, storage, &fakeExecutor{}, WithViewReadConfig(2, time.Second))
	results := runner.RunAll(context.Background(), tasks)
	require.Len(t, results, 2)
	for _, result := range results {
		require.NoError(t, result.Err)
		require.Equal(t, result.Task.SubjectID+"-source-event", result.Task.TriggerEventID)
	}
	require.Equal(t, 1, storage.calls)
	require.Zero(t, storage.singleCalls)
	require.Equal(t, []string{"BTC", "ETH"}, storage.lastIDs)
}

func TestSubjectReadyMissingTargetCannotSucceed(t *testing.T) {
	base := time.Unix(60, 0).UTC()
	task := oneBarTask("BTC", base)
	task.TriggerType, task.InputContractVersion = "subject_ready", "schema:1"
	task.PeriodTime = base.Unix()
	storage := &batchPeriodReadStorage{chunks: map[string]*storageio.RangeChunk{"BTC": {Frame: &engine.DataFrame{}}}}
	runner := NewService(1, storage, &fakeExecutor{}, WithViewReadConfig(1, time.Second))
	results := runner.RunAll(context.Background(), []Task{task})
	require.Len(t, results, 1)
	require.ErrorContains(t, results[0].Err, "subject-ready target is missing")
	require.ErrorContains(t, runner.runValidated(context.Background(), task, &storageio.RangeChunk{Frame: &engine.DataFrame{}}), "subject-ready target is missing")
}

func TestSourceSeriesConstraintsSeparateReadGroups(t *testing.T) {
	base := oneBarTask("BTC", time.Unix(60, 0))
	base.PeriodTime = 60
	base.TriggerType = "subject_ready"
	base.TriggerEventID = "ready"
	base.InputContractVersion = "schema:1"
	a, b, unfiltered := base, base, base
	a.FilterSourceSeriesTag, a.SourceSeriesTag = true, "venue:a"
	b.FilterSourceSeriesTag, b.SourceSeriesTag = true, ""
	groups, _ := buildPeriodReadGroups([]Task{a, b, unfiltered})
	require.Len(t, groups, 3)
	require.Len(t, clusterPeriodReadGroups(groups), 3)
}
