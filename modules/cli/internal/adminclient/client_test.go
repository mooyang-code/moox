package adminclient

import (
	"context"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPostJSONSendsSpaceHeader(t *testing.T) {
	var gotSpaceID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSpaceID = r.Header.Get("X-Space-Id")
		_, _ = w.Write([]byte(`{"ret_info":{"code":0,"msg":"ok"}}`))
	}))
	defer server.Close()

	client := New(server.URL)
	client.SpaceID = "crypto"
	if _, err := client.postJSON(context.Background(), http.MethodPost, "/api/admin/cloudnode/ListCloudAccounts", map[string]string{}); err != nil {
		t.Fatalf("postJSON() error = %v", err)
	}
	if gotSpaceID != "crypto" {
		t.Fatalf("X-Space-Id = %q, want crypto", gotSpaceID)
	}
}

func TestRewriteToServiceRoute(t *testing.T) {
	assert.Equal(t, "/api/service/CloudNodeMgr/ListAccounts", rewriteToServiceRoute("/api/admin/CloudNodeMgr/ListAccounts"))
	assert.Equal(t, "/ListAccounts", rewriteToServiceRoute("/ListAccounts"))
}

func TestServiceAuthRejectsRemotePlainHTTP(t *testing.T) {
	c := New("http://example.com")
	c.ServiceAuth = &ServiceAuthConfig{Version: "v1", AccessKey: "ak", SecretKey: "sk", ExpireSecs: 60}
	_, err := c.postJSON(context.Background(), http.MethodPost, "/api/admin/cloudnode/ListAccounts", map[string]any{})
	require.ErrorContains(t, err, "non-loopback HTTP")
}

func TestIsRetInfoSuccess(t *testing.T) {
	assert.True(t, isRetInfoSuccess(0))
	assert.False(t, isRetInfoSuccess(1))
}

func TestResolvePackageType(t *testing.T) {
	assert.Equal(t, 1, ResolvePackageType("collector"))
	assert.Equal(t, 2, ResolvePackageType("factor"))
	assert.Equal(t, 1, ResolvePackageType("unknown"))
}

func TestParseBatchChangeResponse(t *testing.T) {
	rsp, err := parseBatchChangeResponse([]byte(`{"ret_info":{"code":0},"batch_id":"b1","processed_count":2}`), "BatchCreateNodes")
	require.NoError(t, err)
	assert.Equal(t, "b1", rsp.BatchID)
	assert.Equal(t, 2, rsp.ProcessedCount)
	_, err = parseBatchChangeResponse([]byte(`{"ret_info":{"code":1,"msg":"fail"}}`), "BatchCreateNodes")
	require.Error(t, err)
}

func TestBuildAuthHeader(t *testing.T) {
	cfg := ServiceAuthConfig{AccessKey: "app", SecretKey: "key"}
	header, err := cfg.BuildAuthHeader("POST", "/api/service/x/Do", []byte(`{"a":1}`), time.Unix(1700000000, 0))
	require.NoError(t, err)
	assert.Contains(t, header, "app")
	assert.Contains(t, header, "1700000000")
}
