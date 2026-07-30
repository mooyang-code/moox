package test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	collectsvc "github.com/mooyang-code/moox/modules/collector/internal/rpc"
	"github.com/mooyang-code/moox/modules/collector/internal/serverless"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	collectorschema "github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/mooyang-code/moox/packages/msgbox"
	"github.com/mooyang-code/moox/packages/observabilitypb"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestCollectorStatusToMetricSnapshotPreservesDatasetTruth(t *testing.T) {
	ctx := context.Background()
	mgr, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.ApplySchema(collectorschema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	registry := prometheus.NewRegistry()
	datasetMetrics, err := report.NewDatasetMetrics(registry, "collector")
	if err != nil {
		t.Fatal(err)
	}
	key := report.DatasetKey{SpaceID: "crypto", DatasetID: "market_kline", Freq: "1m"}
	if err := datasetMetrics.ReplaceExpected([]report.DatasetExpectation{{Key: key, Interval: time.Minute}}); err != nil {
		t.Fatal(err)
	}
	service := collectsvc.New(mgr, collectsvc.Dependencies{DatasetMetrics: datasetMetrics})
	t1005 := time.Date(2026, 7, 28, 10, 5, 0, 0, time.UTC)

	reportResult := func(taskID, jobItemID string, status collectorpb.TaskInstanceStatus, result map[string]any) {
		t.Helper()
		if err := mgr.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{
			SpaceID: "crypto", TaskID: taskID, CloudJobItemID: jobItemID,
			DataType: "kline", DatasetID: "market_kline", Interval: "1m",
			LastExecStatus: domain.InstanceStatusPending,
		}}); err != nil {
			t.Fatal(err)
		}
		payload, err := structpb.NewStruct(result)
		if err != nil {
			t.Fatal(err)
		}
		response, err := service.ReportTaskStatus(ctx, &collectorpb.ReportInstanceStatusReq{
			SpaceId: "crypto", TaskId: taskID, JobItemId: jobItemID,
			Status: status, Result: payload,
		})
		if err != nil || response.GetRetInfo().GetCode() != collectorpb.ErrorCode_SUCCESS {
			t.Fatalf("ReportTaskStatus response=%v err=%v", response, err)
		}
	}
	summary := func(rows int, watermark time.Time) map[string]any {
		task := map[string]any{
			"data_type": "kline", "dataset_id": "market_kline", "freq": "1m",
			"rows_written": rows,
		}
		if !watermark.IsZero() {
			task["output_watermark"] = watermark.Format(time.RFC3339Nano)
		}
		return map[string]any{"tasks": []any{task}}
	}
	reportResult("success-new", "job-success-new", collectorpb.TaskInstanceStatus_TASK_INSTANCE_STATUS_SUCCESS, summary(2, t1005))
	reportResult("empty", "job-empty", collectorpb.TaskInstanceStatus_TASK_INSTANCE_STATUS_SUCCESS, summary(0, time.Time{}))
	reportResult("error", "job-error", collectorpb.TaskInstanceStatus_TASK_INSTANCE_STATUS_FAILED, map[string]any{
		"error_code": "COLLECTION_FAILED", "error_summary": "upstream timeout",
	})
	reportResult("success-replay", "job-success-replay", collectorpb.TaskInstanceStatus_TASK_INSTANCE_STATUS_SUCCESS, summary(1, t1005.Add(-2*time.Minute)))

	if err := mgr.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{
		SpaceID: "crypto", TaskID: "stale", CloudJobItemID: "job-current",
		DataType: "kline", DatasetID: "market_kline", Interval: "1m",
		LastExecStatus: domain.InstanceStatusPending,
	}}); err != nil {
		t.Fatal(err)
	}
	stalePayload, err := structpb.NewStruct(summary(100, t1005.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReportTaskStatus(ctx, &collectorpb.ReportInstanceStatusReq{
		SpaceId: "crypto", TaskId: "stale", JobItemId: "job-stale",
		Status: collectorpb.TaskInstanceStatus_TASK_INSTANCE_STATUS_SUCCESS, Result: stalePayload,
	}); err != nil {
		t.Fatal(err)
	}

	if got := collectorMetricValue(t, registry, "moox_collector_dataset_output_watermark_timestamp_seconds", nil); got != float64(t1005.Unix()) {
		t.Fatalf("output watermark=%v want=%v", got, t1005.Unix())
	}
	for result, want := range map[string]float64{"success": 2, "empty": 1, "error": 1} {
		if got := collectorMetricValue(t, registry, "moox_collector_dataset_runs_total", map[string]string{"result": result}); got != want {
			t.Fatalf("runs result=%s got=%v want=%v", result, got, want)
		}
	}

	handler, err := report.NewHandlerWithRegistry(report.Config{
		Module: "collector", ServiceName: "collector", InstanceID: "collector@node-a",
		NodeID: "node-a", BootID: "boot-a", IncludeRegex: `^moox_collector_dataset_`,
	}, registry)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := handler.BuildSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.GetMetricFamilyCount() == 0 || snapshot.GetSampleCount() == 0 {
		t.Fatalf("empty metric snapshot: %+v", snapshot)
	}
}

type sentinelEventReporter struct {
	err error
}

func (r sentinelEventReporter) ReportHealth(context.Context, *observabilitypb.HealthCheckReport, string) error {
	return r.err
}

type sentinelSender struct {
	messages []msgbox.Message
}

func (s *sentinelSender) Send(_ context.Context, message msgbox.Message) error {
	s.messages = append(s.messages, message)
	return nil
}

func TestSCFSentinelUsesDirectNotificationOnlyForCentralPathFailure(t *testing.T) {
	for _, test := range []struct {
		name        string
		monitorUp   bool
		eventBusErr error
		wantError   bool
	}{
		{name: "monitor unavailable", monitorUp: false},
		{name: "eventbus unavailable", monitorUp: true, eventBusErr: errors.New("eventbus unavailable"), wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			sender := &sentinelSender{}
			handler, err := serverless.NewWatchdogHandler(serverless.WatchdogOptions{
				Enabled: true, ObserverID: "scf-sentinel", NodeID: "scf-node-a", SpaceID: "crypto",
				Ready: func() bool { return true },
				Checks: []serverless.WatchdogCheck{func(context.Context) serverless.CheckResult {
					return serverless.CheckResult{
						CheckID: "monitor_ready", Kind: "http", Success: test.monitorUp,
						ErrorCode: "unreachable", Error: "central path unavailable",
					}
				}},
				Events: sentinelEventReporter{err: test.eventBusErr}, DirectSender: sender,
			})
			if err != nil {
				t.Fatal(err)
			}
			runErr := handler.Handle(context.Background())
			if test.wantError && runErr == nil {
				t.Fatal("watchdog succeeded while EventBus was unavailable")
			}
			if !test.wantError && runErr != nil {
				t.Fatal(runErr)
			}
			if len(sender.messages) != 1 || sender.messages[0].Severity != msgbox.SeverityCritical {
				t.Fatalf("direct messages=%+v", sender.messages)
			}
		})
	}
}

func collectorMetricValue(t *testing.T, registry *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			if collectorLabelsMatch(metric, labels) {
				if family.GetType() == dto.MetricType_COUNTER {
					return metric.GetCounter().GetValue()
				}
				return metric.GetGauge().GetValue()
			}
		}
	}
	t.Fatalf("metric %s labels=%v not found", name, labels)
	return 0
}

func collectorLabelsMatch(metric *dto.Metric, expected map[string]string) bool {
	for name, value := range expected {
		found := false
		for _, label := range metric.GetLabel() {
			if label.GetName() == name && label.GetValue() == value {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
