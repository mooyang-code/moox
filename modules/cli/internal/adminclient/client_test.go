package adminclient

import (
	"context"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
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
	c.ServiceAuth = &ServiceAuthConfig{AccessKey: "ak", SecretKey: "sk", TargetNode: "gateway-gz-122", ExpireSecs: 60}
	_, err := c.postJSON(context.Background(), http.MethodPost, "/api/admin/cloudnode/ListAccounts", map[string]any{})
	require.ErrorContains(t, err, "non-loopback HTTP")
}

func TestServiceAuthCannotBypassSafeTransportWithInjectedClient(t *testing.T) {
	c := New("http://example.com")
	c.ServiceAuth = &ServiceAuthConfig{AccessKey: "ak", SecretKey: "sk", TargetNode: "gateway-gz-122"}
	c.HTTPClient = &http.Client{}
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
	cfg := ServiceAuthConfig{AccessKey: "app", SecretKey: "key", TargetNode: "gateway-gz-122"}
	now := time.Unix(1700000000, 0)
	headers := map[string]string{"X-Space-Id": "space-1"}
	header, err := cfg.BuildAuthHeader("POST", "/api/service/x/Do", []byte(`{"a":1}`), headers, now)
	require.NoError(t, err)
	assert.Equal(t, "gateway-gz-122", header.Get("X-Moox-Target-Node"))
	_, err = gatewayauth.Verify(gatewayauth.Credentials{KeyID: "app", Secret: "key"}, gatewayauth.Request{Method: "POST", Path: "/api/service/x/Do", Body: []byte(`{"a":1}`), TargetNode: "gateway-gz-122"}, header, now)
	require.NoError(t, err)
}
