package executor

import (
	"context"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/reporter"
	"github.com/mooyang-code/moox/modules/collector/pkg/config"
)

func TestExecuteDueTasksReportsStatusSynchronously(t *testing.T) {
	oldGlobalConfig := config.GlobalConfig
	oldReporter := reportTaskStatusSync
	t.Cleanup(func() {
		config.GlobalConfig = oldGlobalConfig
		config.UpdateTaskInstances(nil)
		reportTaskStatusSync = oldReporter
	})

	config.UpdateNodeInfo("scf-node", "test")
	config.UpdateTaskInstances([]*config.CollectorTaskInstanceCache{{
		TaskID:     "task-1",
		NodeID:     "scf-node",
		TaskParams: `{"data_type":"missing_type","data_source":"missing_source","inst_type":"SPOT","symbol":"BTC-USDT","intervals":["1m"]}`,
		Invalid:    0,
	}})

	var reports []struct {
		taskID string
		status int
		result string
	}
	reportTaskStatusSync = func(ctx context.Context, taskID string, status int, result string) {
		reports = append(reports, struct {
			taskID string
			status int
			result string
		}{taskID: taskID, status: status, result: result})
	}

	if err := ExecuteDueTasks(context.Background()); err != nil {
		t.Fatalf("ExecuteDueTasks returned error: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("reports = %+v, want exactly one synchronous report", reports)
	}
	if reports[0].taskID != "task-1" || reports[0].status != reporter.StatusFailed {
		t.Fatalf("report = %+v, want task-1 failed", reports[0])
	}
	if !strings.Contains(reports[0].result, "missing_source") {
		t.Fatalf("report result = %q, want missing collector detail", reports[0].result)
	}
}
