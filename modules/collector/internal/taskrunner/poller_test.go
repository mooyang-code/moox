package taskrunner

import (
	"context"
	nodeRuntime "github.com/mooyang-code/moox/packages/cloudruntime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestTaskEventFromJobItemUsesStableTaskIDFromParams(t *testing.T) {
	event, err := taskEventFromJobItem(nodeRuntime.JobItem{
		JobItemID: "task-1:2026-07-04T09:07:00Z",
		Params: map[string]any{
			"space_id":   "crypto",
			"task_id":    "task-1",
			"exchange":   "binance",
			"data_type":  "kline",
			"market":     "spot",
			"symbol":     "BTCUSDT",
			"subject_id": "BTCUSDT",
			"interval":   "1m",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if event.TaskID != "task-1" {
		t.Fatalf("TaskID = %q, want stable task_id from params", event.TaskID)
	}
	if event.Symbol != "BTCUSDT" || len(event.Intervals) != 1 || event.Intervals[0] != "1m" {
		t.Fatalf("unexpected task event: %#v", event)
	}
}

func TestTaskEventFromJobItemAllowsSymbolWithoutSymbolOrInterval(t *testing.T) {
	got, err := taskEventFromJobItem(nodeRuntime.JobItem{
		JobItemID: "task-symbol:2026-07-04T10:30:00Z",
		Params: map[string]any{
			"task_id":   "task-symbol",
			"exchange":  "binance",
			"market":    "spot",
			"data_type": "symbol",
		},
	})
	if err != nil {
		t.Fatalf("taskEventFromJobItem() error = %v", err)
	}
	if got.TaskID != "task-symbol" || got.DataType != "symbol" || got.Market != "spot" || got.InstType != "SPOT" {
		t.Fatalf("unexpected event: %+v", got)
	}
	if len(got.Intervals) != 1 || got.Intervals[0] != "" {
		t.Fatalf("Intervals = %#v, want one empty interval marker", got.Intervals)
	}
}

func TestTaskEventFromJobItemUsesEmptyIntervalForTick(t *testing.T) {
	event, err := taskEventFromJobItem(nodeRuntime.JobItem{
		JobItemID: "task-tick",
		Params: map[string]any{
			"task_id": "task-tick", "exchange": "binance", "market": "spot", "data_type": "tick", "symbol": "BTCUSDT",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "tick", event.DataType)
	assert.Equal(t, []string{""}, event.Intervals)
}

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
