package adminclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListCloudAccounts_ParsesSuccessResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/admin/cloudnode/ListCloudAccounts", r.URL.Path)
		_, _ = w.Write([]byte(`{"ret_info":{"code":0,"msg":"ok"},"accounts":[{"account_id":"a1","provider":"tencent"}]}`))
	}))
	defer server.Close()

	client := New(server.URL)
	accounts, err := client.ListCloudAccounts(context.Background(), "tencent")
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	assert.Equal(t, "a1", accounts[0].AccountID)
}

func TestResolvePackageType_MapsKnownAliases(t *testing.T) {
	assert.Equal(t, 1, ResolvePackageType("collector"))
	assert.Equal(t, 2, ResolvePackageType("factor"))
	assert.Equal(t, 3, ResolvePackageType("custom"))
	assert.Equal(t, 1, ResolvePackageType("unknown"))
}
