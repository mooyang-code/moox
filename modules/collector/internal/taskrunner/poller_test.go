package taskrunner

import (
	"testing"

	nodeRuntime "github.com/mooyang-code/moox/packages/cloudruntime"
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
