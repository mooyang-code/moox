package executor

import (
	"context"
	"errors"
	"testing"
)

func TestReportImmediateTaskStatusReturnsReporterError(t *testing.T) {
	wantErr := errors.New("report failed")
	oldReportTaskStatus := reportTaskStatus
	reportTaskStatus = func(context.Context, string, string, int, string) error {
		return wantErr
	}
	defer func() { reportTaskStatus = oldReportTaskStatus }()

	if err := reportImmediateTaskStatus(context.Background(), "space-a", "task-1", 3, "ok"); !errors.Is(err, wantErr) {
		t.Fatalf("reportImmediateTaskStatus() error = %v, want %v", err, wantErr)
	}
}
