package engine

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStageSummaryListsStagesInPipelineOrder(t *testing.T) {
	summary := stageSummary(map[string]time.Duration{
		"write": 1500 * time.Millisecond, "load": 2 * time.Second, "compute": 250 * time.Microsecond,
	})

	require.Equal(t, "load=2s compute=0s write=1.5s", summary)
}
