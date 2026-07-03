package taskrunner

import (
	"testing"

	nodeRuntime "github.com/mooyang-code/moox/packages/cloudruntime"
)

func TestTaskEventFromWorkItemAllowsSymbolWithoutSymbolOrInterval(t *testing.T) {
	got, err := taskEventFromWorkItem(nodeRuntime.WorkItemLease{
		OwnerRef: "task-symbol",
		Payload:  `{"exchange":"binance","market":"spot","data_type":"symbol"}`,
	})
	if err != nil {
		t.Fatalf("taskEventFromWorkItem() error = %v", err)
	}
	if got.TaskID != "task-symbol" || got.DataType != "symbol" || got.Market != "spot" || got.InstType != "SPOT" {
		t.Fatalf("unexpected event: %+v", got)
	}
	if len(got.Intervals) != 1 || got.Intervals[0] != "" {
		t.Fatalf("Intervals = %#v, want one empty interval marker", got.Intervals)
	}
}
