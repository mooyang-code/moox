package adminclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildUploadPackageJobRequest(t *testing.T) {
	body, err := BuildUploadPackageJobRequest(UploadPackageJobRequest{
		PackageName:    "data-collector",
		Version:        "v1.2.3",
		Runtime:        "CustomRuntime",
		PackageType:    "data_collector",
		BizType:        "data_collector",
		CloudAccountID: "acct-1",
		FileContent:    "base64zip",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, want := range []string{
		`"task_type":"UPLOAD_FILE_TO_COS"`,
		`\"package_name\":\"data-collector\"`,
		`\"file_content\":\"base64zip\"`,
		`\"cloud_account_id\":\"acct-1\"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("body %s missing %s", got, want)
		}
	}
}

func TestBuildCreateNodeJobRequestIncludesHandler(t *testing.T) {
	body, err := BuildCreateNodeJobRequest(CreateNodeJobRequest{
		CloudAccountID: "acct-1",
		Runtime:        "CustomRuntime",
		Handler:        "bootstrap",
		Region:         "ap-guangzhou",
		PackageID:      "pkg-1",
		BizType:        "data_collector",
		NodeType:       "scf-event",
		Config: map[string]string{
			"REGION": "ap-guangzhou",
		},
		Environment: map[string]string{
			"MOOX_ENV": "prod",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, want := range []string{
		`"task_type":"CREATE_NODE"`,
		`\"handler\":\"bootstrap\"`,
		`\"runtime\":\"CustomRuntime\"`,
		`\"package_id\":\"pkg-1\"`,
		`\"config\":{\"REGION\":\"ap-guangzhou\"}`,
		`\"environment\":{\"MOOX_ENV\":\"prod\"}`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("body %s missing %s", got, want)
		}
	}
}

func TestParseCreateJobResponseDirectPB(t *testing.T) {
	resp, err := parseCreateJobResponse([]byte(`{"ret_info":{"code":0,"msg":"ok"},"job_id":"job-direct","total_task_cnt":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.JobID != "job-direct" {
		t.Fatalf("job id = %q, want job-direct", resp.JobID)
	}
}

func TestParseQueryJobResponseDirectPB(t *testing.T) {
	resp, err := parseQueryJobResponse([]byte(`{"ret_info":{"code":0},"job_id":"job-1","job_status":2,"progress":100,"success_task_cnt":1,"failed_task_cnt":0,"tasks":[{"task_id":"t1","task_type":"UPLOAD_FILE_TO_COS","task_status":2,"result_data":"{\"package_id\":\"pkg-1\"}"}]}`), "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if resp.JobStatus != 2 {
		t.Fatalf("job status = %d, want 2", resp.JobStatus)
	}
	if len(resp.Tasks) != 1 || resp.Tasks[0].ResultData == "" {
		t.Fatalf("tasks = %+v", resp.Tasks)
	}
}

func TestClientSendsAccessTokenHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Access-Token"); got != "token-1" {
			t.Fatalf("X-Access-Token = %q, want token-1", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":[{"job_id":"job-1","total_task_cnt":1}]}`))
	}))
	defer server.Close()

	client := New(server.URL)
	client.AccessToken = "token-1"
	resp, err := client.CreateNodeJob(context.Background(), CreateNodeJobRequest{
		CloudAccountID: "acct-1",
		NodeType:       "scf-event",
		Runtime:        "CustomRuntime",
		Handler:        "main",
		Region:         "ap-guangzhou",
		PackageID:      "pkg-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.JobID != "job-1" {
		t.Fatalf("job id = %q, want job-1", resp.JobID)
	}
}

func TestClientUsesGatewayAsyncTaskRoutes(t *testing.T) {
	var gotPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/admin/asynctask/CreateAsyncJob":
			_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":[{"job_id":"job-1","total_task_cnt":1}]}`))
		case "/api/admin/asynctask/QueryAsyncJob":
			_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":[{"job_id":"job-1","job_status":2,"progress":100}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := New(server.URL)
	if _, err := client.CreateNodeJob(context.Background(), CreateNodeJobRequest{
		CloudAccountID: "acct-1",
		NodeType:       "scf-event",
		Runtime:        "CustomRuntime",
		Handler:        "main",
		Region:         "ap-guangzhou",
		PackageID:      "pkg-1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.QueryJob(context.Background(), "job-1"); err != nil {
		t.Fatal(err)
	}

	want := []string{"/api/admin/asynctask/CreateAsyncJob", "/api/admin/asynctask/QueryAsyncJob"}
	if strings.Join(gotPaths, ",") != strings.Join(want, ",") {
		t.Fatalf("paths = %v, want %v", gotPaths, want)
	}
}

// TestClientServiceAuthRewriteAndHeader 验证配置 ServiceAuth 后：
//  1. 请求路径从 /api/admin/asynctask/* 改写为 /api/service/asynctask/*
//  2. 携带 Auth 头，格式为 5 段 "/" 分隔
//  3. 签名与 control 端 validateServiceAuthHeader 算法对称（此处用镜像算法校验）
func TestClientServiceAuthRewriteAndHeader(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Auth")
		buf := make([]byte, 0)
		chunk := make([]byte, 4096)
		for {
			n, err := r.Body.Read(chunk)
			if n > 0 {
				buf = append(buf, chunk[:n]...)
			}
			if err != nil {
				break
			}
		}
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ret_info":{"code":0,"msg":"ok"},"job_id":"job-svc","total_task_cnt":1}`))
	}))
	defer server.Close()

	cfg := &ServiceAuthConfig{
		Version:    "moox-auth-v1",
		AccessKey:  "moox-service",
		SecretKey:  "moox-service-secret-change-me",
		ExpireSecs: 1800,
	}
	client := New(server.URL)
	client.ServiceAuth = cfg
	resp, err := client.CreateNodeJob(context.Background(), CreateNodeJobRequest{
		CloudAccountID: "acct-1",
		NodeType:       "scf-event",
		Runtime:        "CustomRuntime",
		Handler:        "main",
		Region:         "ap-guangzhou",
		PackageID:      "pkg-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.JobID != "job-svc" {
		t.Fatalf("job id = %q, want job-svc", resp.JobID)
	}
	if gotPath != "/api/service/asynctask/CreateAsyncJob" {
		t.Fatalf("path = %q, want /api/service/asynctask/CreateAsyncJob", gotPath)
	}
	if gotAuth == "" {
		t.Fatal("missing Auth header")
	}
	parts := strings.Split(gotAuth, "/")
	if len(parts) != 5 {
		t.Fatalf("Auth header has %d parts, want 5: %q", len(parts), gotAuth)
	}
	// 用相同算法重新生成期望签名并比对：用解析出的 prefix 重算 signature。
	prefix := strings.Join(parts[:4], "/")
	wantSig := hmacSha256Hex(hmacSha256Hex(cfg.SecretKey, prefix), gotBody)
	if parts[4] != wantSig {
		t.Fatalf("signature mismatch: got %q, want %q", parts[4], wantSig)
	}
}
