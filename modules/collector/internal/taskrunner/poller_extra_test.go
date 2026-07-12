package taskrunner

import (
	"context"
	"testing"

	nodeRuntime "github.com/mooyang-code/moox/packages/cloudruntime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntimeSpaceID_FromEnv(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "crypto")
	assert.Equal(t, "crypto", runtimeSpaceID())
}

func TestTaskEventFromJobItem_RequiresTaskID(t *testing.T) {
	_, err := taskEventFromJobItem(nodeRuntime.JobItem{Params: map[string]any{"data_type": "kline"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "task_id")
}

func TestTaskEventFromJobItem_RequiresSymbolForKline(t *testing.T) {
	_, err := taskEventFromJobItem(nodeRuntime.JobItem{
		JobItemID: "task-1",
		Params:    map[string]any{"task_id": "task-1", "data_type": "kline"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symbol")
}

func TestTaskEventFromJobItem_DefaultsInterval(t *testing.T) {
	event, err := taskEventFromJobItem(nodeRuntime.JobItem{
		JobItemID: "task-1",
		Params: map[string]any{
			"task_id": "task-1", "data_type": "kline", "symbol": "BTCUSDT",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"1m"}, event.Intervals)
	assert.Equal(t, "binance", event.DataSource)
}

func TestPollAndExecuteJobItems_SkipsWithoutGateway(t *testing.T) {
	assert.NoError(t, PollAndExecuteJobItems(context.Background()))
}

func TestStringHelpers(t *testing.T) {
	assert.Equal(t, "a", firstString("", "a", "b"))
	assert.Equal(t, "v", stringValue(map[string]any{"k": "v"}, "k"))
	assert.Equal(t, "", stringValue(map[string]any{"k": 1}, "k"))
}
