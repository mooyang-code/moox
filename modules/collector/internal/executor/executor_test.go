package executor

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReportTaskStatusReturnsReporterError(t *testing.T) {
	wantErr := errors.New("report failed")
	oldReportTaskStatus := sendTaskStatus
	sendTaskStatus = func(context.Context, string, string, string, uint64, int, string) error {
		return wantErr
	}
	defer func() { sendTaskStatus = oldReportTaskStatus }()

	err := reportTaskStatus(context.Background(), "space-a", "task-1", "item-1", 2, 3, "ok")
	if !errors.Is(err, wantErr) {
		t.Fatalf("reportTaskStatus() error = %v, want %v", err, wantErr)
	}
	if !errors.Is(err, ErrTaskInstanceReportFailed) {
		t.Fatalf("reportTaskStatus() error = %v, want task instance report boundary", err)
	}
}

type stubCollector struct {
	err    error
	params *sources.CollectParams
}

func (s *stubCollector) Source() string   { return "stub" }
func (s *stubCollector) DataType() string { return "symbol" }
func (s *stubCollector) Collect(_ context.Context, params *sources.CollectParams) error {
	s.params = params
	return s.err
}

func TestNormalizeMarket(t *testing.T) {
	assert.Equal(t, "spot", normalizeMarket(nil))
	assert.Equal(t, "swap", normalizeMarket(&model.TaskExecuteEvent{Market: "swap"}))
	assert.Equal(t, "swap", normalizeMarket(&model.TaskExecuteEvent{InstType: "SWAP"}))
	assert.Equal(t, "spot", normalizeMarket(&model.TaskExecuteEvent{InstType: "SPOT"}))
}

func TestExecuteTask_NilEvent(t *testing.T) {
	_, err := ExecuteTask(context.Background(), context.Background(), nil)
	assert.Error(t, err)
}

func TestExecuteTask_WithStubCollector(t *testing.T) {
	old := sendTaskStatus
	var reportedJobItemID string
	var reportedDeliveryCount uint64
	sendTaskStatus = func(_ context.Context, _, _, jobItemID string, deliveryCount uint64, _ int, _ string) error {
		reportedJobItemID = jobItemID
		reportedDeliveryCount = deliveryCount
		return nil
	}
	t.Cleanup(func() { sendTaskStatus = old })

	collector := &stubCollector{}
	require.NoError(t, sources.GetRegistry().Register(&sources.CollectorDescriptor{
		Source: "stubex", Market: "spot", DataType: "symbol", Collector: collector,
	}))

	msg, err := ExecuteTask(context.Background(), context.Background(), &model.TaskExecuteEvent{
		SpaceID: "crypto", DatasetID: "symbols-custom", TaskID: "task-1", JobItemID: "item-1",
		DeliveryCount: 3, DataSource: "stubex", DataType: "symbol", InstType: "SPOT", Symbol: "BTCUSDT",
	})
	require.NoError(t, err)
	assert.Contains(t, msg, "成功")
	assert.Equal(t, "item-1", reportedJobItemID)
	assert.Equal(t, uint64(3), reportedDeliveryCount)
	require.NotNil(t, collector.params)
	assert.Equal(t, "symbols-custom", collector.params.DatasetID)
}

func TestExecuteTaskReportsFailureOnlyOnFinalDeliveryWithReservedContext(t *testing.T) {
	old := sendTaskStatus
	reports := 0
	sendTaskStatus = func(ctx context.Context, _, _, _ string, _ uint64, _ int, _ string) error {
		reports++
		if err := ctx.Err(); err != nil {
			t.Fatalf("report context already expired: %v", err)
		}
		return nil
	}
	t.Cleanup(func() { sendTaskStatus = old })

	collector := &stubCollector{err: context.DeadlineExceeded}
	require.NoError(t, sources.GetRegistry().Register(&sources.CollectorDescriptor{
		Source: "stubfail", Market: "spot", DataType: "symbol", Collector: collector,
	}))
	event := &model.TaskExecuteEvent{
		SpaceID: "crypto", DatasetID: "symbols-custom", TaskID: "task-fail", JobItemID: "item-fail",
		DeliveryCount: 1, MaxDeliver: 4, DataSource: "stubfail", DataType: "symbol", Market: "spot",
	}
	workloadCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ExecuteTask(workloadCtx, context.Background(), event)
	require.Error(t, err)
	assert.Equal(t, 0, reports)

	event.DeliveryCount = 4
	_, err = ExecuteTask(workloadCtx, context.Background(), event)
	require.Error(t, err)
	assert.Equal(t, 1, reports)
}

func TestExecuteCollectTasks_EmptyAndMissingCollector(t *testing.T) {
	assert.False(t, executeCollectTasks(context.Background(), nil, nil).HasError)

	reports := 0
	result := executeCollectTasks(context.Background(), []*collectTask{{
		SpaceID: "crypto", TaskID: "t1", JobItemID: "item-1",
		DataSource: "missing", Market: "spot", DataType: "kline",
	}}, func(context.Context, string, string, string, uint64, int, string) { reports++ })
	assert.Equal(t, 1, reports)
	assert.False(t, result.HasError) // reported via callback, no local error flag when reporter set
}
