package taskrunner

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestSubjectTaskIdentityIncludesInputRevision(t *testing.T) {
	task := oneBarTask("BTC", time.Unix(60, 0))
	task.TriggerType = "subject_ready"
	task.TriggerEventID = "ready-store-a-sequence-1"
	task.InputContractVersion = "schema:1"
	initial := DeterministicTaskID(task)
	require.Equal(t, initial, DeterministicTaskID(task), "redelivery must remain idempotent")
	task.TriggerEventID = "ready-store-a-sequence-2"
	require.NotEqual(t, initial, DeterministicTaskID(task), "a corrected bar requires a new task identity")
	task.TriggerEventID = "ready-store-a-sequence-1"
	task.InputContractVersion = "schema:2"
	require.NotEqual(t, initial, DeterministicTaskID(task), "source contract replacement must not reuse a task")
}

func TestBuildSubjectTaskRequiresSourceIdentity(t *testing.T) {
	scope := TaskScope{TriggerType: "subject_ready", BindingGeneration: "generation", SubjectID: "BTC", StartTime: time.Unix(60, 0), EndTime: time.Unix(120, 0), TriggerEventID: "ready", InputContractVersion: "schema:1"}
	factor := domain.FactorDef{FactorType: domain.FactorTypeTimeSeries, Status: domain.FactorStatusEnabled, SourceHash: "hash"}
	_, err := BuildTask(scope, factor, "/factors")
	require.NoError(t, err)
	scope.TriggerEventID = ""
	_, err = BuildTask(scope, factor, "/factors")
	require.ErrorContains(t, err, "source event")
	scope.TriggerEventID, scope.InputContractVersion = "ready", ""
	_, err = BuildTask(scope, factor, "/factors")
	require.ErrorContains(t, err, "input contract")
}

func TestBuildTaskPreservesExplicitEmptySourceSeries(t *testing.T) {
	scope := TaskScope{BindingGeneration: "generation", SubjectID: "BTC", StartTime: time.Unix(60, 0), EndTime: time.Unix(120, 0)}
	factor := domain.FactorDef{FactorType: domain.FactorTypeTimeSeries, Status: domain.FactorStatusEnabled, SourceHash: "hash"}
	unfiltered, err := BuildTask(scope, factor, "/factors")
	require.NoError(t, err)
	scope.FilterSourceSeriesTag = true
	empty, err := BuildTask(scope, factor, "/factors")
	require.NoError(t, err)
	require.True(t, empty.FilterSourceSeriesTag)
	require.Empty(t, empty.SourceSeriesTag)
	require.NotEqual(t, DeterministicTaskID(unfiltered), DeterministicTaskID(empty))
	scope.SourceSeriesTag = "venue:test"
	named, err := BuildTask(scope, factor, "/factors")
	require.NoError(t, err)
	require.Equal(t, "venue:test", named.SourceSeriesTag)
	require.NotEqual(t, DeterministicTaskID(empty), DeterministicTaskID(named))
}
