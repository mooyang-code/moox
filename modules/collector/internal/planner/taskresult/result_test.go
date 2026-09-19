package taskresult

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResultIDsWithFrequencyAreTaskExclusiveAndFrequencyQualified(t *testing.T) {
	ids := ResultIDsWithFrequency("crypto", "task-5m", "5m")
	require.Equal(t, "dataset_collector_"+hashForTest("crypto", "task-5m")+"_5m", ids.DatasetID)
	require.Equal(t, "view_collector_"+hashForTest("crypto", "task-5m")+"_5m", ids.ViewID)
	require.NotEqual(t, ids, ResultIDsWithFrequency("crypto", "task-1h", "1h"))
}

func hashForTest(spaceID, taskID string) string {
	return resultIDs(spaceID, taskID).DatasetID[len("dataset_collector_"):]
}
