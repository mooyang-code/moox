package rpcclient

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"

	"github.com/mooyang-code/moox/modules/cli/internal/testfixture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func spaceTestClient(server *httptest.Server, spaceID string) *Client {
	return &Client{Gateway: testfixture.HandlerGateway{Handler: server.Config.Handler}, SpaceID: spaceID}
}

// 云账号走 Collector；provider 在客户端过滤（Collector 只登记腾讯云）。
func TestListCloudAccountsCallsCollectorAndFiltersProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/admin/collector/ListCloudAccounts", r.URL.Path)
		_, _ = w.Write([]byte(`{"ret_info":{"code":0},"accounts":[{"account_id":"a1","provider":"tencent"},{"account_id":"a2","provider":"other"}]}`))
	}))
	defer server.Close()
	accounts, err := collectorTestClient(server).ListCloudAccounts(context.Background(), "tencent")
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	assert.Equal(t, "a1", accounts[0].GetAccountId())
}

func TestCreateCloudAccountSendsTheWholeAccount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/admin/collector/CreateCloudAccount", r.URL.Path)
		var body struct {
			Account map[string]any `json:"account"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "tencent-scf", body.Account["account_id"])
		assert.Equal(t, "ap-singapore", body.Account["cos_region"])
		_, _ = w.Write([]byte(`{"ret_info":{"code":0},"account":{"account_id":"tencent-scf"}}`))
	}))
	defer server.Close()
	account, err := collectorTestClient(server).CreateCloudAccount(context.Background(), &collectorpb.CloudAccount{AccountId: "tencent-scf", CosRegion: "ap-singapore"})
	require.NoError(t, err)
	assert.Equal(t, "tencent-scf", account.GetAccountId())
}

// 返回码非 0 或缺 ret_info 都是错误，消息里带上 Collector 给的原因。
func TestCollectorCloudCallsRejectFailedOrIncompleteResponses(t *testing.T) {
	for response, want := range map[string]string{
		`{"ret_info":{"code":409,"msg":"云账号 a1 已存在"}}`: "已存在",
		`{"accounts":[]}`: "ret_info",
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(response)) }))
		_, err := collectorTestClient(server).ListCloudAccounts(context.Background(), "")
		server.Close()
		require.ErrorContains(t, err, want)
	}
}

// 最新代码包：同空间、名称完全相同、状态可用的第一条（Collector 按上传先后倒序返回）。
func TestLatestPackagePicksTheNewestAvailablePackageWithTheExactName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/admin/collector/ListFunctionPackages", r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "crypto", body["space_id"])
		_, _ = w.Write([]byte(`{"ret_info":{"code":0},"packages":[
			{"package_id":"pending-1","package_name":"moox-collector-crypto-plan","status":"pending"},
			{"package_id":"other-1","package_name":"moox-collector-crypto-plan-experimental","status":"available"},
			{"package_id":"exact-1","package_name":"moox-collector-crypto-plan","status":"available"},
			{"package_id":"exact-0","package_name":"moox-collector-crypto-plan","status":"available"}]}`))
	}))
	defer server.Close()
	client := spaceTestClient(server, "crypto")
	id, found, err := client.LatestPackage(context.Background(), "moox-collector-crypto-plan")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "exact-1", id)
	_, found, err = client.LatestPackage(context.Background(), "moox-collector-stockcn-plan")
	require.NoError(t, err)
	assert.False(t, found)
}

// 上传：Init 拿地址 → PUT 到 COS → Complete 核对大小与 MD5；三步的请求体都带同一个空间。
func TestUploadPackageInitPutsAndCompletesInTheSameSpace(t *testing.T) {
	data := []byte("zip-bytes")
	sum := md5.Sum(data)
	cos := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		raw, _ := io.ReadAll(r.Body)
		assert.Equal(t, data, raw)
	}))
	defer cos.Close()
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.URL.Path)
		assert.Equal(t, "stockcn", r.Header.Get("X-Space-Id"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "stockcn", body["space_id"])
		switch r.URL.Path {
		case "/api/admin/collector/InitPackageUpload":
			assert.Equal(t, "moox-collector-stockcn-plan", body["package_name"])
			assert.Equal(t, "account-1", body["cloud_account_id"])
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"package":{"package_id":"pkg-1","status":"pending"},"upload_url":"` + cos.URL + `/pkg.zip"}`))
		case "/api/admin/collector/CompletePackageUpload":
			assert.Equal(t, "pkg-1", body["package_id"])
			assert.Equal(t, hex.EncodeToString(sum[:]), body["file_md5"])
			// proto JSON 把 int64 写成字符串。
			assert.Equal(t, strconv.Itoa(len(data)), body["file_size"])
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"package":{"package_id":"pkg-1","status":"available"}}`))
		default:
			t.Fatalf("意外的请求 %s", r.URL.Path)
		}
	}))
	defer server.Close()
	pkg, err := spaceTestClient(server, "stockcn").UploadPackage(context.Background(), &collectorpb.InitPackageUploadReq{
		PackageName: "moox-collector-stockcn-plan", Version: "v1", Runtime: "Go1", CloudAccountId: "account-1", Filename: "pkg.zip",
	}, data)
	require.NoError(t, err)
	assert.Equal(t, "pkg-1", pkg.GetPackageId())
	assert.Equal(t, "available", pkg.GetStatus())
	assert.Equal(t, []string{"/api/admin/collector/InitPackageUpload", "/api/admin/collector/CompletePackageUpload"}, methods)
}

func TestUploadPackageRequiresASpace(t *testing.T) {
	_, err := (&Client{}).UploadPackage(context.Background(), &collectorpb.InitPackageUploadReq{}, nil)
	require.ErrorContains(t, err, "空间")
}

func TestPackageUploadClientHasBoundedTimeout(t *testing.T) {
	assert.Equal(t, packageUploadTimeout, newPackageUploadHTTPClient().Timeout)
}

// 删除结果带命名空间：同名函数在两个命名空间里的副本是两条结果。
func TestDeleteCloudFunctionsDecodesTheNamespaceOfEachResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/admin/collector/DeleteCloudFunctions", r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "crypto", body["space_id"])
		assert.Equal(t, []any{"f-000"}, body["names"])
		assert.Equal(t, "legacy", body["namespace"])
		assert.Equal(t, "ap-nanjing", body["region"])
		assert.Equal(t, true, body["dry_run"])
		_, _ = w.Write([]byte(`{"ret_info":{"code":0},"results":[
			{"name":"f-000","region":"ap-nanjing","namespace":"default","done":true},
			{"name":"f-000","region":"ap-nanjing","namespace":"legacy","done":true}]}`))
	}))
	defer server.Close()
	results, err := spaceTestClient(server, "crypto").DeleteCloudFunctions(context.Background(), DeleteCloudFunctionsRequest{Names: []string{"f-000"}, Namespace: "legacy", Region: "ap-nanjing", DryRun: true})
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, "default", results[0].Namespace)
	assert.Equal(t, "legacy", results[1].Namespace)
}

func TestCloudFunctionDrifted(t *testing.T) {
	good := CloudFunction{Exists: true, TimerMatches: true, PackageLatest: true, EnvironmentMatches: true}
	assert.False(t, good.Drifted())
	for name, change := range map[string]func(*CloudFunction){
		"不存在":    func(f *CloudFunction) { f.Exists = false },
		"没读到":    func(f *CloudFunction) { f.ReadError = "限频" },
		"定时器":    func(f *CloudFunction) { f.TimerMatches = false },
		"代码包":    func(f *CloudFunction) { f.PackageLatest = false },
		"环境":     func(f *CloudFunction) { f.EnvironmentMatches = false },
		"计划外的函数": func(f *CloudFunction) { f.Unplanned = true },
	} {
		function := good
		change(&function)
		assert.True(t, function.Drifted(), name)
	}
}
