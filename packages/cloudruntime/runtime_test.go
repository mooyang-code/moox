package cloudruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

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

	host, port := testServerAddress(t, server.URL)
	if err := Run(context.Background(), Config{
		ServerIP:          host,
		ServerPort:        port,
		SpaceID:           "crypto",
		NodeID:            "node-a",
		SupportedJobTypes: []string{"collect.kline"},
		Auth: AuthConfig{
			AccessKey: "test-ak",
			SecretKey: "test-sk",
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

	host, port := testServerAddress(t, server.URL)
	if err := Run(context.Background(), Config{
		ServerIP:          host,
		ServerPort:        port,
		SpaceID:           "crypto",
		NodeID:            "node-a",
		SupportedJobTypes: []string{"collect.unknown"},
		Auth: AuthConfig{
			AccessKey: "test-ak",
			SecretKey: "test-sk",
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

	host, port := testServerAddress(t, server.URL)
	if err := Run(context.Background(), Config{
		ServerIP:          host,
		ServerPort:        port,
		SpaceID:           "crypto",
		NodeID:            "node-a",
		SupportedJobTypes: []string{"collect.kline"},
		Auth: AuthConfig{
			AccessKey: "test-ak",
			SecretKey: "test-sk",
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

func testServerAddress(t *testing.T, rawURL string) (string, int) {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("parse test server port: %v", err)
	}
	return parsed.Hostname(), port
}
