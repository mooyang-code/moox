package adminclient

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRewriteToServiceRoute(t *testing.T) {
	assert.Equal(t, "/api/service/CloudNodeMgr/ListAccounts", rewriteToServiceRoute("/api/admin/CloudNodeMgr/ListAccounts"))
	assert.Equal(t, "/ListAccounts", rewriteToServiceRoute("/ListAccounts"))
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
	header, err := cfg.BuildAuthHeader([]byte(`{"a":1}`), time.Unix(1700000000, 0))
	require.NoError(t, err)
	assert.Contains(t, header, "app")
	assert.Contains(t, header, "1700000000")
}

func TestHmacSha256Hex(t *testing.T) {
	got := hmacSha256Hex("secret", "payload")
	assert.Len(t, got, 64)
	assert.Equal(t, got, hmacSha256Hex("secret", "payload"))
}
