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

func TestCreateCloudAccount_RegistersRegionLocalBucket(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/admin/cloudnode/CreateCloudAccount", r.URL.Path)
		var body struct {
			Account CloudAccountInput `json:"account"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "tencent-scf-singapore", body.Account.AccountID)
		assert.Equal(t, "ap-singapore", body.Account.COSRegion)
		assert.Equal(t, "moox-scf-singapore-1255382561", body.Account.COSBucket)
		_, _ = w.Write([]byte(`{"ret_info":{"code":0,"msg":"ok"},"account":{"account_id":"tencent-scf-singapore","cos_region":"ap-singapore","cos_bucket":"moox-scf-singapore-1255382561"}}`))
	}))
	defer server.Close()

	account, err := New(server.URL).CreateCloudAccount(context.Background(), CloudAccountInput{
		AccountID: "tencent-scf-singapore", AccountName: "Tencent SCF Singapore", Provider: "tencent",
		CredentialSecretID: "tencent-default", AppID: "1255382561", COSRegion: "ap-singapore", COSBucket: "moox-scf-singapore-1255382561",
	})
	require.NoError(t, err)
	assert.Equal(t, "tencent-scf-singapore", account.AccountID)
}

func TestEnableTaskRulePreservesCanonicalDefinition(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		switch r.URL.Path {
		case "/api/admin/collectmgr/GetTaskRuleDetail":
			assert.Equal(t, "stock_cn", body["space_id"])
			assert.Equal(t, "builtin-stock-cn-kline-1m", body["rule_id"])
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"rule":{"space_id":"stock_cn","rule_id":"builtin-stock-cn-kline-1m","data_type":"kline","provider":"stock_cn_multi","market_type":"equity","enabled":false,"collect_params":{"frequency":"1m","target_dataset_id":"stock_cn_kline"}}}`))
		case "/api/admin/collectmgr/UpdateTaskRule":
			rule, ok := body["rule"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, true, rule["enabled"])
			assert.Equal(t, "stock_cn_kline", rule["collect_params"].(map[string]any)["target_dataset_id"])
			_, _ = w.Write([]byte(`{"ret_info":{"code":0}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	require.NoError(t, New(server.URL).EnableTaskRule(context.Background(), "stock_cn", "builtin-stock-cn-kline-1m"))
}

func TestCreateTaskRuleStartsDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/admin/collectmgr/CreateTaskRule", r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		rule, ok := body["rule"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "stock_cn", rule["space_id"])
		assert.Equal(t, "builtin-stock-cn-kline-1m", rule["rule_id"])
		assert.Equal(t, false, rule["enabled"])
		assert.Equal(t, "stock_cn_kline", rule["collect_params"].(map[string]any)["target_dataset_id"])
		_, _ = w.Write([]byte(`{"ret_info":{"code":0,"msg":"ok"},"rule_id":"builtin-stock-cn-kline-1m"}`))
	}))
	defer server.Close()

	require.NoError(t, New(server.URL).CreateTaskRule(context.Background(), "stock_cn", "builtin-stock-cn-kline-1m", "kline", "stock_cn_multi", "equity", "moox-cli", map[string]any{
		"provider":          "stock_cn_multi",
		"market_type":       "equity",
		"symbol_source":     "dataset",
		"symbol_dataset_id": "stock_cn_instruments",
		"target_dataset_id": "stock_cn_kline",
		"frequency":         "1m",
	}))
}

func TestPackageUploadClientHasBoundedTimeout(t *testing.T) {
	assert.Equal(t, packageUploadTimeout, newPackageUploadHTTPClient().Timeout)
}

func TestResolvePackageType_MapsKnownAliases(t *testing.T) {
	assert.Equal(t, 1, ResolvePackageType("collector"))
	assert.Equal(t, 2, ResolvePackageType("factor"))
	assert.Equal(t, 3, ResolvePackageType("custom"))
	assert.Equal(t, 1, ResolvePackageType("unknown"))
}
