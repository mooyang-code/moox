package cloudruntime

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPostServiceUsesCAFileFromEnvironment(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Moox-Target-Node"); got != "gateway-gz-122" {
			t.Fatalf("target node = %q", got)
		}
		_, _ = w.Write([]byte(`{"ret_info":{"code":0}}`))
	}))
	defer server.Close()
	caFile := filepath.Join(t.TempDir(), "peer-ca.pem")
	requireNoError(t, os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600))
	t.Setenv("MOOX_GATEWAY_NODE_ID", "gateway-gz-122")
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "test-ak")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "test-sk")
	t.Setenv("MOOX_GATEWAY_CA_FILE", caFile)
	var out map[string]any
	requireNoError(t, postService(context.Background(), Config{ServiceGatewayTarget: server.URL, HTTPTimeout: time.Second}, "cloudnode", "PollJobItems", map[string]any{}, &out))
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunPollsJobItemsAndDispatchesRegisteredHandler(t *testing.T) {
	resetRegistryForTest()
	defer resetRegistryForTest()

	var pollReq pollJobItemsRequest
	var reportReq reportJobItemStatusRequest
	Register("collect.kline", HandlerFunc(func(ctx context.Context, item JobItem) (Result, error) {
		if item.JobItemID != "ji-1" {
			t.Fatalf("job_item_id = %q, want ji-1", item.JobItemID)
		}
		return Result{Summary: map[string]any{"rows_written": 12}}, nil
	}))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Moox-Target-Node"); got != "gateway-gz-122" {
			t.Fatalf("X-Moox-Target-Node = %q, want gateway-gz-122", got)
		}
		switch r.URL.Path {
		case "/api/service/cloudnode/PollJobItems":
			if err := json.NewDecoder(r.Body).Decode(&pollReq); err != nil {
				t.Fatalf("decode poll request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(pollJobItemsResponse{
				RetInfo: &retInfo{Code: 0, Msg: "ok"},
				Items: []polledJobItem{{
					SpaceID:       "crypto",
					JobID:         "job-1",
					JobItemID:     "ji-1",
					JobType:       "collect.kline",
					CodePackageID: "collector-scf",
					Params:        map[string]any{"symbol": "BTCUSDT"},
					AttemptNo:     2,
				}},
			})
		case "/api/service/cloudnode/ReportJobItemStatus":
			if err := json.NewDecoder(r.Body).Decode(&reportReq); err != nil {
				t.Fatalf("decode report request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(struct {
				RetInfo *retInfo `json:"ret_info"`
			}{RetInfo: &retInfo{Code: 0, Msg: "ok"}})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	if err := Run(context.Background(), Config{
		ServiceGatewayTarget: server.URL,
		SpaceID:              "crypto",
		NodeID:               "node-a",
		SupportedJobTypes:    []string{"collect.kline"},
		Auth: AuthConfig{
			AccessKey:  "test-ak",
			SecretKey:  "test-sk",
			TargetNode: "gateway-gz-122",
		},
	}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if pollReq.SpaceID != "crypto" {
		t.Fatalf("poll space_id = %q, want crypto", pollReq.SpaceID)
	}
	if len(pollReq.SupportedJobTypes) != 1 || pollReq.SupportedJobTypes[0] != "collect.kline" {
		t.Fatalf("supported_job_types = %#v, want collect.kline", pollReq.SupportedJobTypes)
	}
	if pollReq.ProtocolVersion != defaultProtocolVersion {
		t.Fatalf("protocol_version = %q, want %q", pollReq.ProtocolVersion, defaultProtocolVersion)
	}
	if reportReq.JobItemID != "ji-1" {
		t.Fatalf("report job_item_id = %q, want ji-1", reportReq.JobItemID)
	}
	if reportReq.Status != jobItemReportStatusSuccess {
		t.Fatalf("report status = %d, want %d", reportReq.Status, jobItemReportStatusSuccess)
	}
	if reportReq.AttemptNo != 2 {
		t.Fatalf("attempt_no = %d, want 2", reportReq.AttemptNo)
	}
	if got := reportReq.ResultSummary["rows_written"]; got != float64(12) {
		t.Fatalf("rows_written = %#v, want 12", got)
	}
}

func TestRunReportsPermanentFailureWhenHandlerMissing(t *testing.T) {
	resetRegistryForTest()
	defer resetRegistryForTest()

	var reportReq reportJobItemStatusRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/service/cloudnode/PollJobItems":
			_ = json.NewEncoder(w).Encode(pollJobItemsResponse{
				RetInfo: &retInfo{Code: 0, Msg: "ok"},
				Items: []polledJobItem{{
					SpaceID:       "crypto",
					JobID:         "job-1",
					JobItemID:     "ji-missing",
					JobType:       "collect.unknown",
					CodePackageID: "collector-scf",
					AttemptNo:     1,
				}},
			})
		case "/api/service/cloudnode/ReportJobItemStatus":
			if err := json.NewDecoder(r.Body).Decode(&reportReq); err != nil {
				t.Fatalf("decode report request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(struct {
				RetInfo *retInfo `json:"ret_info"`
			}{RetInfo: &retInfo{Code: 0, Msg: "ok"}})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	if err := Run(context.Background(), Config{
		ServiceGatewayTarget: server.URL,
		SpaceID:              "crypto",
		NodeID:               "node-a",
		SupportedJobTypes:    []string{"collect.unknown"},
		Auth: AuthConfig{
			AccessKey: "test-ak", SecretKey: "test-sk", TargetNode: "gateway-gz-122",
		},
	}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if reportReq.Status != jobItemReportStatusFailed {
		t.Fatalf("status = %d, want failed", reportReq.Status)
	}
	if reportReq.ErrorKind != jobItemErrorKindPermanent {
		t.Fatalf("error_kind = %d, want permanent", reportReq.ErrorKind)
	}
	if reportReq.ErrorCode != "HANDLER_NOT_FOUND" {
		t.Fatalf("error_code = %q, want HANDLER_NOT_FOUND", reportReq.ErrorCode)
	}
}

func TestRunReportsRetryableErrorKind(t *testing.T) {
	resetRegistryForTest()
	defer resetRegistryForTest()

	var reportReq reportJobItemStatusRequest
	Register("collect.kline", HandlerFunc(func(context.Context, JobItem) (Result, error) {
		return Result{}, Retryable(errors.New("temporary upstream failure"), "UPSTREAM_TEMPORARY")
	}))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/service/cloudnode/PollJobItems":
			_ = json.NewEncoder(w).Encode(pollJobItemsResponse{
				RetInfo: &retInfo{Code: 0, Msg: "ok"},
				Items:   []polledJobItem{{JobItemID: "ji-retry", JobType: "collect.kline", AttemptNo: 3}},
			})
		case "/api/service/cloudnode/ReportJobItemStatus":
			if err := json.NewDecoder(r.Body).Decode(&reportReq); err != nil {
				t.Fatalf("decode report request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(struct {
				RetInfo *retInfo `json:"ret_info"`
			}{RetInfo: &retInfo{Code: 0, Msg: "ok"}})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	if err := Run(context.Background(), Config{
		ServiceGatewayTarget: server.URL,
		SpaceID:              "crypto",
		NodeID:               "node-a",
		SupportedJobTypes:    []string{"collect.kline"},
		Auth: AuthConfig{
			AccessKey: "test-ak", SecretKey: "test-sk", TargetNode: "gateway-gz-122",
		},
	}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if reportReq.ErrorKind != jobItemErrorKindRetryable {
		t.Fatalf("error_kind = %d, want retryable", reportReq.ErrorKind)
	}
	if reportReq.ErrorCode != "UPSTREAM_TEMPORARY" {
		t.Fatalf("error_code = %q, want UPSTREAM_TEMPORARY", reportReq.ErrorCode)
	}
	if reportReq.AttemptNo != 3 {
		t.Fatalf("attempt_no = %d, want 3", reportReq.AttemptNo)
	}
}

func TestRunReportsPermanentFailureAndCompletionLogWhenHandlerPanics(t *testing.T) {
	resetRegistryForTest()
	defer resetRegistryForTest()

	var completionLines []string
	oldCompletionLogger := logCompletion
	logCompletion = func(ctx context.Context, line string) {
		completionLines = append(completionLines, line)
	}
	defer func() { logCompletion = oldCompletionLogger }()

	var reportReq reportJobItemStatusRequest
	Register("collect.kline", HandlerFunc(func(context.Context, JobItem) (Result, error) {
		panic("collector exploded")
	}))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/service/cloudnode/PollJobItems":
			_ = json.NewEncoder(w).Encode(pollJobItemsResponse{
				RetInfo: &retInfo{Code: 0, Msg: "ok"},
				Items: []polledJobItem{{
					SpaceID:   "crypto",
					JobItemID: "task-1:2026-07-07T10:01:00Z",
					JobType:   "collect.kline",
					Params: map[string]any{
						"task_id":  "task-1",
						"symbol":   "BTCUSDT",
						"interval": "1m",
					},
					AttemptNo: 1,
				}},
			})
		case "/api/service/cloudnode/ReportJobItemStatus":
			if err := json.NewDecoder(r.Body).Decode(&reportReq); err != nil {
				t.Fatalf("decode report request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(struct {
				RetInfo *retInfo `json:"ret_info"`
			}{RetInfo: &retInfo{Code: 0, Msg: "ok"}})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	if err := Run(context.Background(), Config{
		ServiceGatewayTarget: server.URL,
		SpaceID:              "crypto",
		NodeID:               "node-a",
		SupportedJobTypes:    []string{"collect.kline"},
		Auth: AuthConfig{
			AccessKey: "test-ak", SecretKey: "test-sk", TargetNode: "gateway-gz-122",
		},
	}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if reportReq.Status != jobItemReportStatusFailed {
		t.Fatalf("status = %d, want failed", reportReq.Status)
	}
	if reportReq.ErrorKind != jobItemErrorKindPermanent {
		t.Fatalf("error_kind = %d, want permanent", reportReq.ErrorKind)
	}
	if reportReq.ErrorCode != "HANDLER_PANIC" {
		t.Fatalf("error_code = %q, want HANDLER_PANIC", reportReq.ErrorCode)
	}
	if len(completionLines) != 1 {
		t.Fatalf("completion log count = %d, want 1: %#v", len(completionLines), completionLines)
	}
	for _, want := range []string{
		"collector_job_done",
		"space_id=crypto",
		"task_id=task-1",
		"job_item_id=task-1:2026-07-07T10:01:00Z",
		"node_id=node-a",
		"symbol=BTCUSDT",
		"interval=1m",
		"status=failed",
		"error=\"panic: collector exploded\"",
	} {
		if !strings.Contains(completionLines[0], want) {
			t.Fatalf("completion log line missing %q: %s", want, completionLines[0])
		}
	}
}

func TestRegisterRejectsDuplicateJobType(t *testing.T) {
	resetRegistryForTest()
	defer resetRegistryForTest()

	Register("collect.kline", HandlerFunc(func(context.Context, JobItem) (Result, error) {
		return Result{}, nil
	}))
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate Register should panic")
		}
	}()
	Register("collect.kline", HandlerFunc(func(context.Context, JobItem) (Result, error) {
		return Result{}, nil
	}))
}

func TestJobCompletionLogLineIncludesCollectorLookupFields(t *testing.T) {
	line := jobCompletionLogLine(Config{
		SpaceID: "crypto",
		NodeID:  "node-a",
	}, JobItem{
		SpaceID:   "crypto",
		JobItemID: "task-1:2026-07-07T10:01:00Z",
		JobType:   "collect.kline",
		AttemptNo: 2,
		Params: map[string]any{
			"task_id":  "task-1",
			"symbol":   "BTCUSDT",
			"interval": "1m",
		},
	}, jobItemReportStatusFailed, 1532*time.Millisecond, errors.New("upstream unavailable"))

	for _, want := range []string{
		"collector_job_done",
		"space_id=crypto",
		"task_id=task-1",
		"job_item_id=task-1:2026-07-07T10:01:00Z",
		"node_id=node-a",
		"job_type=collect.kline",
		"attempt_no=2",
		"symbol=BTCUSDT",
		"interval=1m",
		"status=failed",
		"duration_ms=1532",
		"error=\"upstream unavailable\"",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("completion log line missing %q: %s", want, line)
		}
	}
}
