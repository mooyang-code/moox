package cloudruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/jetstream"
)

func TestJobItemExecuteAtZeroMeansMissing(t *testing.T) {
	var item JobItem
	if !item.ExecuteAt.IsZero() {
		t.Fatalf("execute_at = %v, want zero value", item.ExecuteAt)
	}
}

func testConfig(target string) Config {
	return Config{
		ServiceGatewayTarget: target, SpaceID: "crypto", NodeID: "node-1",
		Auth: AuthConfig{AccessKey: "key", SecretKey: "secret", TargetNode: "gateway-1"},
	}
}

func TestConfigDoesNotRequireCodePackageIdentity(t *testing.T) {
	cfg := testConfig("http://127.0.0.1:11000")

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func resetRegistryForTest() {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()
	globalRegistry.handlers = map[string]Handler{}
}

func TestExecuteJobItemReportsBeforeAck(t *testing.T) {
	resetRegistryForTest()
	var handledDeliveryCount uint64
	Register("collect.kline", HandlerFunc(func(_ context.Context, item JobItem) (Result, error) {
		handledDeliveryCount = item.DeliveryCount
		return Result{Summary: map[string]any{"rows": 1}}, nil
	}))
	reported := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reported = true
		var body reportRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Status != reportSuccess || body.JobItemID != "item-1" {
			t.Fatalf("body = %+v", body)
		}
		_, _ = w.Write([]byte(`{"ret_info":{"code":0,"msg":"ok"}}`))
	}))
	defer server.Close()
	result := ExecuteJobItem(context.Background(), testConfig(server.URL), JobItem{
		SpaceID: "crypto", JobItemID: "item-1", JobType: "collect.kline",
	}, 2, 3)
	if !reported || result.Decision != jetstream.ACK || handledDeliveryCount != 2 {
		t.Fatalf("reported=%v delivery_count=%d result=%+v", reported, handledDeliveryCount, result)
	}
}

func TestClassifyTaskInstanceReportErrorCodeForLifecycleLog(t *testing.T) {
	kind, code := classifyError(Retryable(errors.New("gateway unavailable"), "TASK_INSTANCE_REPORT_FAILED"))
	if kind != errorRetryable || code != "TASK_INSTANCE_REPORT_FAILED" {
		t.Fatalf("classifyError() = kind=%d code=%q", kind, code)
	}
}

func TestExecuteJobItemRetryableFailureOnlyReportsOnLastDelivery(t *testing.T) {
	resetRegistryForTest()
	Register("collect.kline", HandlerFunc(func(context.Context, JobItem) (Result, error) {
		return Result{}, Retryable(errors.New("temporary"), "TEMP")
	}))
	reports := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reports++
		_, _ = w.Write([]byte(`{"ret_info":{"code":0,"msg":"ok"}}`))
	}))
	defer server.Close()
	item := JobItem{SpaceID: "crypto", JobItemID: "item-1", JobType: "collect.kline"}
	first := ExecuteJobItem(context.Background(), testConfig(server.URL), item, 1, 3)
	if first.Decision != jetstream.RETRY || first.Delay != normalRetryDelay || normalRetryDelay != time.Second || reports != 0 {
		t.Fatalf("first=%+v reports=%d", first, reports)
	}
	last := ExecuteJobItem(context.Background(), testConfig(server.URL), item, 3, 3)
	if last.Decision != jetstream.TERM || reports != 1 {
		t.Fatalf("last=%+v reports=%d", last, reports)
	}
}

func TestExecuteJobItemReportFailureRetriesDelivery(t *testing.T) {
	resetRegistryForTest()
	Register("collect.kline", HandlerFunc(func(context.Context, JobItem) (Result, error) {
		return Result{}, nil
	}))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	result := ExecuteJobItem(context.Background(), testConfig(server.URL), JobItem{
		SpaceID: "crypto", JobItemID: "item-1", JobType: "collect.kline",
	}, 1, 3)
	if result.Decision != jetstream.RETRY || result.Err == nil {
		t.Fatalf("result=%+v", result)
	}
	if result.Delay != normalRetryDelay || normalRetryDelay != time.Second {
		t.Fatalf("retry delay = %v, want %v", result.Delay, normalRetryDelay)
	}
}

func TestCloudJobLifecycleLogFieldsAreStableAndOmitParamsAndSummary(t *testing.T) {
	t.Setenv("MOOX_CODE_PACKAGE_ID", "package-1")
	fields := cloudJobLogFields{
		Event: "collector_job_done",
		Config: Config{
			NodeID: "node-1",
		},
		Item: JobItem{
			SpaceID: "crypto", JobID: "job-1", JobItemID: "item-1", JobType: "collect.kline",
			ExecuteAt: time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC),
			Consumer:  "consumer-1", MessageID: "message-1",
			Params: map[string]any{
				"task_id": "task-1", "dataset_id": "kline", "subject_id": "BTC-USDT",
				"symbol": "BTCUSDT", "interval": "1m", "secret_key": "must-not-log",
			},
		},
		DeliveryCount: 2, Status: "failed", Duration: 1500 * time.Millisecond,
		ErrorCode: "COLLECT_FAILED", Err: errors.New("request failed"),
	}
	got := fields.String()
	want := `event="collector_job_done" space_id="crypto" job_id="job-1" job_item_id="item-1" ` +
		`task_id="task-1" job_type="collect.kline" runtime_code_package_id="package-1" node_id="node-1" ` +
		`consumer="consumer-1" message_id="message-1" delivery_count=2 ` +
		`execute_at="2026-07-26T10:00:00Z" dataset_id="kline" ` +
		`subject_id="BTC-USDT" symbol="BTCUSDT" interval="1m" status="failed" duration_ms=1500 ` +
		`error_code="COLLECT_FAILED" error="request failed"`
	if got != want {
		t.Fatalf("cloud job log:\n got: %s\nwant: %s", got, want)
	}
	for _, forbidden := range []string{"params=", "summary=", "must-not-log", "secret_key"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("cloud job log contains %q: %s", forbidden, got)
		}
	}
}
