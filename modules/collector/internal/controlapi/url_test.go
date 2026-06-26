package controlapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestURLUsesAPIServicePath(t *testing.T) {
	got := URL("127.0.0.1", 20103, "cloudnode", "ReportHeartbeatInner")
	want := "http://127.0.0.1:20103/api/service/cloudnode/ReportHeartbeatInner"
	if got != want {
		t.Fatalf("URL() = %q, want %q", got, want)
	}
}

func TestGenerateAuthHeaderSignsBody(t *testing.T) {
	header := GenerateAuthHeader(AuthConfig{
		Version:   "moox-auth-v1",
		AccessKey: "collector",
		SecretKey: "collector-secret",
		NowUnix:   1710000000,
		ExpireSec: 1800,
	}, `{"node_id":"scf-event"}`)

	const prefix = "moox-auth-v1/collector/1710000000/1800/"
	if !strings.HasPrefix(header, prefix) {
		t.Fatalf("auth header = %q, want prefix %q", header, prefix)
	}
	if len(strings.TrimPrefix(header, prefix)) != 64 {
		t.Fatalf("signature length = %d, want 64", len(strings.TrimPrefix(header, prefix)))
	}
}

func TestNewSignedRequestSetsAuthHeader(t *testing.T) {
	body := []byte(`{"id":"task-1"}`)
	req, err := NewSignedRequest(
		"POST",
		"http://127.0.0.1:20103/api/service/collectmgr/ReportTaskStatus",
		body,
		AuthConfig{Version: "moox-auth-v1", AccessKey: "collector", SecretKey: "collector-secret", NowUnix: 1710000000, ExpireSec: 1800},
	)
	if err != nil {
		t.Fatalf("NewSignedRequest returned error: %v", err)
	}
	if req.Method != http.MethodPost {
		t.Fatalf("method = %q, want POST", req.Method)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got := req.Header.Get("Auth"); !strings.HasPrefix(got, "moox-auth-v1/collector/1710000000/1800/") {
		t.Fatalf("Auth header = %q", got)
	}
}
