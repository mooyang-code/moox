package adminclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListCloudNodesPaginatesAndParsesMetadata(t *testing.T) {
	var pages []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/admin/cloudnode/GetNodeList", r.URL.Path)
		var body struct {
			CloudAccountID string `json:"cloud_account_id"`
			Region         string `json:"region"`
			NodeType       string `json:"node_type"`
			Page           struct {
				Page int `json:"page"`
				Size int `json:"size"`
			} `json:"page"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "account-a", body.CloudAccountID)
		assert.Equal(t, "ap-guangzhou", body.Region)
		assert.Equal(t, "scf-event", body.NodeType)
		pages = append(pages, body.Page.Page)
		index := body.Page.Page - 1
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ret_info": map[string]any{"code": 0, "msg": "ok"},
			"items": []map[string]any{{
				"node_id":    fmt.Sprintf("fleet-%d", index),
				"package_id": "pkg-old",
				"metadata":   map[string]any{"function_name_prefix": "fleet", "index": index},
			}},
			"page": map[string]any{
				"page":     body.Page.Page,
				"size":     1,
				"total":    2,
				"has_more": body.Page.Page == 1,
			},
		})
	}))
	defer server.Close()

	client := New(server.URL)
	nodes, err := client.ListCloudNodes(context.Background(), CloudNodeListFilter{
		CloudAccountID: "account-a",
		Region:         "ap-guangzhou",
		NodeType:       "scf-event",
	})
	require.NoError(t, err)
	require.Len(t, nodes, 2)
	assert.Equal(t, []int{1, 2}, pages)
	assert.Equal(t, "fleet-1", nodes[1].NodeID)
	assert.Equal(t, float64(1), nodes[1].Metadata["index"])
}

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
