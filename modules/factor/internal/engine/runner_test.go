package engine

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/stretchr/testify/require"
)

func TestStageSummaryListsStagesInPipelineOrder(t *testing.T) {
	summary := stageSummary(map[string]time.Duration{
		"write": 1500 * time.Millisecond, "load": 2 * time.Second, "compute": 250 * time.Microsecond,
	})

	require.Equal(t, "load=2s compute=0s write=1.5s", summary)
}

func TestWithWarmupMarksUnseenSetsWarming(t *testing.T) {
	lanes := withWarmup(
		[]domain.LaneStatus{{SetID: "a", Queued: 2, Active: true}},
		[]string{"a", "b"},
		[]pipeline.WarmupStatus{{SetID: "a", State: pipeline.WarmupReady, WarmSubjects: 5, ExpectedSubjects: 5}},
	)
	require.Equal(t, []domain.LaneStatus{
		{SetID: "a", Queued: 2, Active: true, WarmupState: pipeline.WarmupReady, WarmSubjects: 5, ExpectedSubjects: 5},
		{SetID: "b", WarmupState: pipeline.WarmupWarming},
	}, lanes)
}
