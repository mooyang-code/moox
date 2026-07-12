package executor

import (
	"context"
	"errors"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

type stubCollector struct{ err error }

func (s *stubCollector) Source() string   { return "stub" }
func (s *stubCollector) DataType() string { return "symbol" }
func (s *stubCollector) Collect(context.Context, *sources.CollectParams) error {
	return s.err
}

func TestNormalizeMarket(t *testing.T) {
	assert.Equal(t, "spot", normalizeMarket(nil))
	assert.Equal(t, "swap", normalizeMarket(&model.TaskExecuteEvent{Market: "swap"}))
	assert.Equal(t, "swap", normalizeMarket(&model.TaskExecuteEvent{InstType: "SWAP"}))
	assert.Equal(t, "spot", normalizeMarket(&model.TaskExecuteEvent{InstType: "SPOT"}))
}

func TestExecuteTaskImmediately_NilEvent(t *testing.T) {
	_, err := ExecuteTaskImmediately(context.Background(), nil)
	assert.Error(t, err)
}

func TestExecuteTaskImmediately_WithStubCollector(t *testing.T) {
	old := reportTaskStatus
	reportTaskStatus = func(context.Context, string, string, int, string) error { return nil }
	t.Cleanup(func() { reportTaskStatus = old })

	require.NoError(t, sources.GetRegistry().Register(&sources.CollectorDescriptor{
		Source: "stubex", Market: "spot", DataType: "symbol", Collector: &stubCollector{},
	}))

	msg, err := ExecuteTaskImmediately(context.Background(), &model.TaskExecuteEvent{
		SpaceID: "crypto", TaskID: "task-1", DataSource: "stubex", DataType: "symbol",
		InstType: "SPOT", Symbol: "BTCUSDT",
	})
	require.NoError(t, err)
	assert.Contains(t, msg, "成功")
}

func TestExecuteCollectTasks_EmptyAndMissingCollector(t *testing.T) {
	assert.False(t, executeCollectTasks(context.Background(), nil, nil).HasError)

	reports := 0
	result := executeCollectTasks(context.Background(), []*collectTask{{
		SpaceID: "crypto", TaskID: "t1", DataSource: "missing", Market: "spot", DataType: "kline",
	}}, func(context.Context, string, string, int, string) { reports++ })
	assert.Equal(t, 1, reports)
	assert.False(t, result.HasError) // reported via callback, no local error flag when reporter set
}
