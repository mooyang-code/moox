package controlclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildUploadPackageJobRequest(t *testing.T) {
	body := BuildUploadPackageJobRequest(UploadPackageJobRequest{
		PackageName:    "data-collector",
		Version:        "v1.2.3",
		Runtime:        "CustomRuntime",
		PackageType:    "data_collector",
		BizType:        "data_collector",
		CloudAccountID: "acct-1",
		FileContent:    "base64zip",
	})
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, want := range []string{
		`"task_type":"UPLOAD_FILE_TO_COS"`,
		`"package_name":"data-collector"`,
		`"file_content":"base64zip"`,
		`"cloud_account_id":"acct-1"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("body %s missing %s", got, want)
		}
	}
}

func TestBuildCreateNodeJobRequestIncludesHandler(t *testing.T) {
	body := BuildCreateNodeJobRequest(CreateNodeJobRequest{
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
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, want := range []string{
		`"task_type":"CREATE_NODE"`,
		`"handler":"bootstrap"`,
		`"runtime":"CustomRuntime"`,
		`"package_id":"pkg-1"`,
		`"config":{"REGION":"ap-guangzhou"}`,
		`"environment":{"MOOX_ENV":"prod"}`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("body %s missing %s", got, want)
		}
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
		case "/api/control/asynctask/CreateAsyncJob":
			_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":[{"job_id":"job-1","total_task_cnt":1}]}`))
		case "/api/control/asynctask/QueryAsyncJob":
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

	want := []string{"/api/control/asynctask/CreateAsyncJob", "/api/control/asynctask/QueryAsyncJob"}
	if strings.Join(gotPaths, ",") != strings.Join(want, ",") {
		t.Fatalf("paths = %v, want %v", gotPaths, want)
	}
}
