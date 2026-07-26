package reporter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendRequestReturnsMalformedResponseError(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_NODE_ID", "gateway-gz-122")
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "test-ak")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "test-sk")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Moox-Target-Node"); got != "gateway-gz-122" {
			t.Fatalf("target node = %q", got)
		}
		_, _ = w.Write([]byte("not-json"))
	}))
	defer server.Close()

	err := sendRequest(context.Background(), server.URL, []byte(`{}`), server.Client())
	if err == nil || !strings.Contains(err.Error(), "解析服务端响应失败") {
		t.Fatalf("sendRequest() error = %v, want malformed response error", err)
	}
}

func TestSendRequestReturnsRetInfoError(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_NODE_ID", "gateway-gz-122")
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "test-ak")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "test-sk")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Moox-Target-Node"); got != "gateway-gz-122" {
			t.Fatalf("target node = %q", got)
		}
		_, _ = w.Write([]byte(`{"ret_info":{"code":1,"msg":"bad"}}`))
	}))
	defer server.Close()

	err := sendRequest(context.Background(), server.URL, []byte(`{}`), server.Client())
	if err == nil || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("sendRequest() error = %v, want ret_info error", err)
	}
}

func TestExecuteTaskStatusReportCarriesJobItemID(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_NODE_ID", "gateway-gz-122")
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "test-ak")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "test-sk")
	var got ReportTaskStatusRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"ret_info":{"code":0,"msg":"ok"}}`))
	}))
	defer server.Close()

	err := executeTaskStatusReport(
		context.Background(), "crypto", "task-1", "item-123", "node-1", StatusSuccess, `{"rows":1}`, server.URL,
	)
	if err != nil {
		t.Fatalf("executeTaskStatusReport() error = %v", err)
	}
	if got.JobItemID != "item-123" {
		t.Fatalf("job_item_id = %q, want item-123", got.JobItemID)
	}
}

func TestReportTaskStatusRequiresJobItemIDBeforeSkippingUnconfiguredGateway(t *testing.T) {
	err := ReportTaskStatus(context.Background(), "crypto", "task-1", "", StatusSuccess, `{}`)
	if err == nil || !strings.Contains(err.Error(), "job_item_id is required") {
		t.Fatalf("ReportTaskStatus() error = %v, want required job_item_id", err)
	}
}

func TestTaskStatusLogFieldsAreStableAndOmitResult(t *testing.T) {
	t.Setenv("MOOX_CODE_PACKAGE_ID", "package-1")
	got := taskStatusLogLine("crypto", "task-1", "item-1", "node-1", StatusSuccess, "success", nil)
	want := `event="collector_job_instance_reported" space_id="crypto" job_item_id="item-1" ` +
		`task_id="task-1" runtime_code_package_id="package-1" node_id="node-1" ` +
		`task_status=3 status="success" error_code="" error=""`
	if got != want {
		t.Fatalf("task status log:\n got: %s\nwant: %s", got, want)
	}
	for _, sensitive := range []string{"result=", "params=", "secret_key", "access_key"} {
		if strings.Contains(strings.ToLower(got), sensitive) {
			t.Fatalf("task status log contains %q: %s", sensitive, got)
		}
	}
}

func TestTaskStatusLogFailureHasStableErrorCode(t *testing.T) {
	got := taskStatusLogLine(
		"crypto", "task-1", "item-1", "node-1", StatusFailed, "failed",
		errors.New("gateway unavailable"),
	)
	if !strings.Contains(got, `status="failed"`) ||
		!strings.Contains(got, `error_code="TASK_INSTANCE_REPORT_FAILED"`) {
		t.Fatalf("failed task status log = %s", got)
	}
}

func TestTaskStatusLogDistinguishesSkippedReport(t *testing.T) {
	got := taskStatusLogLine("crypto", "task-1", "item-1", "", StatusSuccess, "skipped", nil)
	if !strings.Contains(got, `status="skipped"`) || !strings.Contains(got, `error_code=""`) {
		t.Fatalf("skipped task status log = %s", got)
	}
}
