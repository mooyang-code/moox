package command

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/mooyang-code/moox/modules/cli/internal/clsprepare"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustBuildCollectorCreateNodeItem(t *testing.T, opts collectorPublishOptions, packageID string) adminclient.NodeCreateItem {
	t.Helper()
	setCollectorCLSTestCredentials(t)
	item, err := buildCollectorCreateNodeItem(opts, packageID)
	require.NoError(t, err)
	return item
}

func setCollectorCLSTestCredentials(t *testing.T) {
	t.Helper()
	if os.Getenv("MOOX_CLS_SECRET_ID") == "" {
		t.Setenv("MOOX_CLS_SECRET_ID", "test-cls-id")
	}
	if os.Getenv("MOOX_CLS_SECRET_KEY") == "" {
		t.Setenv("MOOX_CLS_SECRET_KEY", "test-cls-key")
	}
}

func setCollectorFleetRuntimeTestEnvironment(t *testing.T) string {
	t.Helper()
	setCollectorCLSTestCredentials(t)
	t.Setenv("MOOX_CLS_HOST", "ap-guangzhou.cls.tencentyun.com")
	t.Setenv("MOOX_GATEWAY_NODE_ID", "gateway-e2e")
	t.Setenv("MOOX_COLLECTOR_GATEWAY_SERVICE_KEY_ID", "collector")
	t.Setenv("MOOX_COLLECTOR_GATEWAY_SERVICE_SECRET_KEY", "collector-secret")

	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(server.Close)
	gatewayCA := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	dir := t.TempDir()
	gatewayCAFile := filepath.Join(dir, "gateway-ca.pem")
	require.NoError(t, os.WriteFile(gatewayCAFile, gatewayCA, 0o600))
	t.Setenv("MOOX_GATEWAY_CA_FILE", gatewayCAFile)
	t.Setenv("MOOX_SERVICE_GATEWAY_CA_FILE", gatewayCAFile)

	eventBusCAFile := filepath.Join(dir, "eventbus-ca.pem")
	require.NoError(t, os.WriteFile(eventBusCAFile, []byte("eventbus-ca"), 0o600))
	credentialFile := filepath.Join(dir, "eventbus.yaml")
	require.NoError(t, os.WriteFile(credentialFile, []byte(
		"version: 1\nurls: [tls://203.0.113.10:4222]\nusername: worker\npassword: worker-secret\nca_file: eventbus-ca.pem\n",
	), 0o600))
	return credentialFile
}

func TestCollectorFunctionEnvironmentEmbedsCAFileMaterial(t *testing.T) {
	setCollectorCLSTestCredentials(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	caFile := filepath.Join(t.TempDir(), "peer.pem")
	require.NoError(t, os.WriteFile(caFile, pemBytes, 0o600))
	t.Setenv("MOOX_GATEWAY_CA_FILE", caFile)
	t.Setenv("MOOX_SERVICE_GATEWAY_CA_FILE", caFile)
	env, err := collectorFunctionEnvironment(collectorPublishOptions{})
	require.NoError(t, err)
	assert.Equal(t, base64.StdEncoding.EncodeToString(pemBytes), env["MOOX_GATEWAY_CA_PEM_B64"])
	assert.Equal(t, base64.StdEncoding.EncodeToString(pemBytes), env["MOOX_SERVICE_GATEWAY_CA_PEM_B64"])
	assert.NotContains(t, env, "MOOX_GATEWAY_CA_FILE")
	assert.NotContains(t, env, "MOOX_SERVICE_GATEWAY_CA_FILE")
}

func TestLoadCollectorSCFFetcherConfigSelectsOnlyRequestedSpace(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "custom.toml")
	content := `[admin]
username = "admin"
password = "password"

[tencent_cloud]
secret_id = "secret-id"
secret_key = "secret-key"
region = "ap-guangzhou"

[eventbus]
public_address = "eventbus.example.test"
port = 4222
tls_enabled = true

[control_host]
name = "control"
address = "192.0.2.10"
port = 22
username = "ubuntu"
password = "password"

[scf_fetcher]
enabled = true

[[scf_fetcher.spaces]]
space_id = "crypto"
package_config_dir = "scf/crypto"
package_name = "moox-collector-crypto"
function_prefix = "moox-fetcher-crypto"
memory_size = 64
timeout_seconds = 15
realtime_batch_size = 10
max_inflight_requests = 5
request_timeout_ms = 1500
http_max_attempts = 4
storage_max_attempts = 1
storage_timeout_ms = 5000

[[scf_fetcher.spaces.regions]]
region = "ap-singapore"
enabled = true
function_count = 1
cloud_account_id = "tencent-scf-singapore"

[[scf_fetcher.spaces]]
space_id = "stock_cn"
package_config_dir = "scf/stock_cn"
package_name = "moox-collector-stock_cn"
function_prefix = "moox-fetcher-stock_cn"
memory_size = 64
timeout_seconds = 15
realtime_batch_size = 10
max_inflight_requests = 5
request_timeout_ms = 1500
http_max_attempts = 4
storage_max_attempts = 1
storage_timeout_ms = 5000

[[scf_fetcher.spaces.regions]]
region = "ap-guangzhou"
enabled = true
function_count = 1
cloud_account_id = "tencent-scf-guangzhou"
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	crypto, err := loadCollectorSCFFetcherConfig(path, "crypto")
	require.NoError(t, err)
	require.NotNil(t, crypto)
	assert.Equal(t, "crypto", crypto.SpaceID)
	assert.Equal(t, "scf/crypto", crypto.PackageConfigDir)
	assert.Equal(t, "ap-singapore", crypto.Regions[0].Region)

	_, err = loadCollectorSCFFetcherConfig(path, "stock_us")
	require.ErrorContains(t, err, `no configuration for space "stock_us"`)
}

func TestEnsureCollectorSpaceCloudAccountsRegistersOnlyMissingAccount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/admin/cloudnode/CreateCloudAccount", r.URL.Path)
		_, _ = w.Write([]byte(`{"ret_info":{"code":0},"account":{"account_id":"tencent-scf-singapore","cos_region":"ap-singapore","cos_bucket":"moox-scf-singapore-1255382561"}}`))
	}))
	defer server.Close()

	accounts := map[string]adminclient.CloudAccount{}
	err := ensureCollectorSpaceCloudAccounts(context.Background(), adminclient.New(server.URL), &setupconfig.SCFFetcherSpace{
		SpaceID: "crypto",
		Regions: []setupconfig.SCFFetcherRegion{{
			Region: "ap-singapore", Enabled: true, CloudAccountID: "tencent-scf-singapore",
			CloudAccountName: "Tencent SCF Singapore", CredentialSecretID: "tencent-default",
			AppID: "1255382561", COSBucket: "moox-scf-singapore-1255382561",
		}},
	}, accounts)
	require.NoError(t, err)
	assert.Equal(t, "ap-singapore", accounts["tencent-scf-singapore"].COSRegion)
}

func TestEgressProbeResponseDataAcceptsStructuredAndRawResponses(t *testing.T) {
	structured, ok := egressProbeResponseData(map[string]any{
		"data": map[string]any{"details": map[string]any{"public_ip": "198.51.100.1"}},
	})
	require.True(t, ok)
	assert.Equal(t, "198.51.100.1", structured["details"].(map[string]any)["public_ip"])

	raw, ok := egressProbeResponseData(map[string]any{
		"raw": `{"success":true,"data":{"details":{"public_ip":"198.51.100.2"}}}`,
	})
	require.True(t, ok)
	assert.Equal(t, "198.51.100.2", raw["details"].(map[string]any)["public_ip"])

	_, ok = egressProbeResponseData(map[string]any{"raw": "not-json"})
	assert.False(t, ok)
}

func TestCollectorFunctionEnvironmentRejectsInvalidOrConflictingCA(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		setCollectorCLSTestCredentials(t)
		t.Setenv("MOOX_GATEWAY_CA_FILE", filepath.Join(t.TempDir(), "missing.pem"))
		_, err := collectorFunctionEnvironment(collectorPublishOptions{})
		require.Error(t, err)
	})
	t.Run("conflict", func(t *testing.T) {
		setCollectorCLSTestCredentials(t)
		t.Setenv("MOOX_GATEWAY_CA_FILE", "one")
		t.Setenv("MOOX_GATEWAY_CA_PEM_B64", "two")
		_, err := collectorFunctionEnvironment(collectorPublishOptions{})
		require.ErrorContains(t, err, "mutually exclusive")
	})
	t.Run("invalid material", func(t *testing.T) {
		setCollectorCLSTestCredentials(t)
		t.Setenv("MOOX_GATEWAY_CA_PEM_B64", "not-base64")
		_, err := collectorFunctionEnvironment(collectorPublishOptions{})
		require.Error(t, err)
	})
	t.Run("explicit serverless path", func(t *testing.T) {
		setCollectorCLSTestCredentials(t)
		_, err := collectorFunctionEnvironment(collectorPublishOptions{Env: []string{"MOOX_GATEWAY_CA_FILE=/host/peer.pem"}})
		require.ErrorContains(t, err, "must not contain")
	})
	t.Run("invalid service gateway material", func(t *testing.T) {
		setCollectorCLSTestCredentials(t)
		t.Setenv("MOOX_SERVICE_GATEWAY_CA_PEM_B64", "not-base64")
		_, err := collectorFunctionEnvironment(collectorPublishOptions{})
		require.ErrorContains(t, err, "invalid service gateway CA material")
	})
}

func TestCollectorFunctionEnvironmentDoesNotRequireOrInjectCLSCredentials(t *testing.T) {
	t.Setenv("MOOX_CLS_SECRET_ID", "")
	t.Setenv("MOOX_CLS_SECRET_KEY", "")
	t.Setenv("TENCENTCLOUD_SECRET_ID", "")
	t.Setenv("TENCENTCLOUD_SECRET_KEY", "")
	t.Setenv("TENCENT_SECRET_ID", "")
	t.Setenv("TENCENT_SECRET_KEY", "")
	env, err := collectorFunctionEnvironment(collectorPublishOptions{})
	require.NoError(t, err)
	assert.NotContains(t, env, "MOOX_CLS_SECRET_ID")
	assert.NotContains(t, env, "MOOX_CLS_SECRET_KEY")
}

func TestCollectorFunctionEnvironmentRejectsCloudCredentialOverrides(t *testing.T) {
	_, err := collectorFunctionEnvironment(collectorPublishOptions{Env: []string{"TENCENTCLOUD_SECRET_ID=should-not-reach-scf"}})
	require.ErrorContains(t, err, "managed key TENCENTCLOUD_SECRET_ID")
	_, err = collectorFunctionEnvironment(collectorPublishOptions{Env: []string{"MOOX_CLS_SECRET_KEY=should-not-reach-scf"}})
	require.ErrorContains(t, err, "managed key MOOX_CLS_SECRET_KEY")
}

func TestCollectorFunctionEnvironmentInjectsManagedEventBusCredential(t *testing.T) {
	dir := t.TempDir()
	ca := []byte("test-ca-pem")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ca.pem"), ca, 0o600))
	credentialPath := filepath.Join(dir, "cloudnode-worker.yaml")
	require.NoError(t, os.WriteFile(credentialPath, []byte(
		"version: 1\nurls: [tls://203.0.113.10:4222]\nusername: cloudnode-worker\ntoken: worker-token\nca_file: ca.pem\n",
	), 0o600))
	env, err := collectorFunctionEnvironment(collectorPublishOptions{
		EventBusCredentialFile: credentialPath,
		CLSSecretID:            "cls-id",
		CLSSecretKey:           "cls-key",
	}, "pkg-1")
	require.NoError(t, err)
	assert.Equal(t, "tls://203.0.113.10:4222", env["MOOX_EVENTBUS_NATS_URL"])
	assert.Equal(t, "cloudnode-worker", env["MOOX_EVENTBUS_NATS_USERNAME"])
	assert.Equal(t, "worker-token", env["MOOX_EVENTBUS_NATS_PASSWORD"])
	assert.Equal(t, base64.StdEncoding.EncodeToString(ca), env["MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64"])
	assert.Equal(t, "pkg-1", env["MOOX_CODE_PACKAGE_ID"])
	assert.NotContains(t, env, "MOOX_EVENTBUS_NATS_TLS_CA_FILE")

	_, err = collectorFunctionEnvironment(collectorPublishOptions{
		EventBusCredentialFile: credentialPath,
		CLSSecretID:            "cls-id",
		CLSSecretKey:           "cls-key",
		Env:                    []string{"MOOX_EVENTBUS_NATS_PASSWORD=override"},
	}, "pkg-1")
	require.ErrorContains(t, err, "managed key")
}

func TestCollectorFunctionEnvironmentUsesRuntimeCollectorIdentity(t *testing.T) {
	setCollectorCLSTestCredentials(t)
	t.Setenv("MOOX_GATEWAY_NODE_ID", "")
	t.Setenv("MOOX_GATEWAY_TARGET_NODE", "node-a")
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "moox-cli")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "cli-secret")
	t.Setenv("MOOX_COLLECTOR_GATEWAY_SERVICE_KEY_ID", "collector")
	t.Setenv("MOOX_COLLECTOR_GATEWAY_SERVICE_SECRET_KEY", "collector-secret")

	env, err := collectorFunctionEnvironment(collectorPublishOptions{})
	require.NoError(t, err)
	assert.Equal(t, "node-a", env["MOOX_GATEWAY_NODE_ID"])
	assert.Equal(t, "node-a", env["MOOX_GATEWAY_TARGET_NODE"])
	assert.Equal(t, "collector", env["MOOX_GATEWAY_SERVICE_KEY_ID"])
	assert.Equal(t, "collector-secret", env["MOOX_GATEWAY_SERVICE_SECRET_KEY"])
	assert.Equal(t, "collector", env["MOOX_GATEWAY_CALLER"])
}

type collectorCLSAPI struct{}

func (collectorCLSAPI) GetService(context.Context) (bool, error)    { return true, nil }
func (collectorCLSAPI) OpenService(context.Context) (string, error) { return "", nil }
func (collectorCLSAPI) FindLogset(context.Context, string) (tencent.CLSLogset, bool, error) {
	return tencent.CLSLogset{ID: "logset-from-api", Name: "moox"}, true, nil
}
func (collectorCLSAPI) CreateLogset(context.Context, string) (tencent.CLSLogset, string, error) {
	panic("unexpected CreateLogset")
}
func (collectorCLSAPI) FindTopic(context.Context, string, string) (tencent.CLSTopic, bool, error) {
	return tencent.CLSTopic{ID: "topic-from-api", LogsetID: "logset-from-api", Name: "moox-application", IndexEnabled: true}, true, nil
}
func (collectorCLSAPI) CreateTopic(context.Context, tencent.CLSCreateTopicOptions) (tencent.CLSTopic, string, error) {
	panic("unexpected CreateTopic")
}
func (collectorCLSAPI) CreateIndex(context.Context, string) (string, error) {
	panic("unexpected CreateIndex")
}

func TestBuildCollectorCreateNodeItemIncludesCollectorWorkloads(t *testing.T) {
	t.Setenv("MOOX_CLS_HOST", "ap-guangzhou.cls.tencentyun.com")
	t.Setenv("MOOX_CLS_SECRET_ID", "cls-id")
	t.Setenv("MOOX_CLS_SECRET_KEY", "cls-key")
	t.Setenv("MOOX_COLLECTOR_GATEWAY_SERVICE_KEY_ID", "collector")
	t.Setenv("MOOX_COLLECTOR_GATEWAY_SERVICE_SECRET_KEY", "collector-secret")
	item := mustBuildCollectorCreateNodeItem(t, collectorPublishOptions{
		collectorPackageOptions: collectorPackageOptions{
			CLSLogsetID: "logset-unified",
			CLSTopicID:  "topic-unified",
		},
		CloudAccountID:   "account-a",
		SpaceID:          "crypto",
		ServiceAccessKey: "svc-ak",
		ServiceSecretKey: "svc-sk",
		Runtime:          "CustomRuntime",
		Handler:          "main",
		Region:           "ap-guangzhou",
		PackageName:      "moox-collector",
		BizType:          "market_fetcher",
		NodeType:         "scf-event",
		Env:              []string{"MOOX_ENV=prod"},
	}, "moox-collector_dev")

	if item.CloudAccountID != "account-a" || item.Region != "ap-guangzhou" || item.PackageID != "moox-collector_dev" {
		t.Fatalf("routing fields = %#v", item)
	}
	if item.Config["timeout"] != "15" || item.Config["memory_size"] != "64" {
		t.Fatalf("config = %#v", item.Config)
	}
	assert.NotContains(t, item.Config, "cls_logset_id")
	assert.NotContains(t, item.Config, "cls_topic_id")
	if item.Environment["MOOX_ENV"] != "prod" {
		t.Fatalf("env = %#v", item.Environment)
	}
	if item.Environment["MOOX_SPACE_ID"] != "crypto" {
		t.Fatalf("space env = %#v", item.Environment)
	}
	if item.Environment["MOOX_GATEWAY_SERVICE_KEY_ID"] != "collector" || item.Environment["MOOX_GATEWAY_SERVICE_SECRET_KEY"] != "collector-secret" {
		t.Fatalf("service auth env = %#v", item.Environment)
	}
	assert.Equal(t, "ap-guangzhou.cls.tencentyun.com", item.Environment["MOOX_CLS_HOST"])
	assert.Equal(t, "cls-id", item.Environment["MOOX_CLS_SECRET_ID"])
	assert.Equal(t, "cls-key", item.Environment["MOOX_CLS_SECRET_KEY"])
	if item.Metadata["function_name_prefix"] != "moox-collector" {
		t.Fatalf("function_name_prefix = %#v", item.Metadata["function_name_prefix"])
	}
	if item.Metadata["biz_type"] != "market_fetcher" {
		t.Fatalf("biz_type = %#v", item.Metadata["biz_type"])
	}
	workloads, ok := item.Metadata["supported_workloads"].([]string)
	if !ok {
		t.Fatalf("supported_workloads type = %T", item.Metadata["supported_workloads"])
	}
	if len(workloads) != 0 {
		t.Fatalf("supported_workloads = %#v", workloads)
	}
	assert.NotContains(t, item.Environment, "MOOX_COLLECTOR_JOB_TYPES")
}

func TestBuildCollectorFleetCreateItemsAreUniqueAndDeepCloned(t *testing.T) {
	credentialFile := setCollectorFleetRuntimeTestEnvironment(t)
	items, err := buildCollectorFleetCreateItems(collectorPublishOptions{
		CloudAccountID:         "account-a",
		SpaceID:                "crypto",
		Region:                 "ap-guangzhou",
		FunctionNamePrefix:     "e2e-collector",
		NodeCount:              50,
		EventBusCredentialFile: credentialFile,
	}, "pkg-new")
	require.NoError(t, err)
	require.Len(t, items, 50)

	seen := make(map[any]struct{}, len(items))
	for index, item := range items {
		assert.Equal(t, "e2e-collector", item.Metadata["function_name_prefix"])
		assert.Equal(t, index, item.Metadata["index"])
		assert.Equal(t, "pkg-new", item.PackageID)
		_, duplicate := seen[item.Metadata["index"]]
		assert.False(t, duplicate)
		seen[item.Metadata["index"]] = struct{}{}
	}

	items[0].Metadata["sentinel"] = true
	items[0].Environment["MOOX_TEST_ONLY"] = "changed"
	items[0].Config["timeout"] = "999"
	assert.NotContains(t, items[1].Metadata, "sentinel")
	assert.NotContains(t, items[1].Environment, "MOOX_TEST_ONLY")
	assert.Equal(t, defaultCollectorSCFTimeout, items[1].Config["timeout"])
}

func TestBuildCollectorFleetCreateItemsRequiresCompleteRuntimeEnvironment(t *testing.T) {
	setCollectorCLSTestCredentials(t)
	t.Setenv("MOOX_CLS_HOST", "ap-guangzhou.cls.tencentyun.com")
	_, err := buildCollectorFleetCreateItems(collectorPublishOptions{
		SpaceID:   "crypto",
		NodeCount: 50,
	}, "pkg-new")
	require.ErrorContains(t, err, "collector fleet runtime environment requires")
}

func TestSelectCollectorFleetNodesKeepsEmptySlotsForFleetExpansion(t *testing.T) {
	nodes := []adminclient.CloudNode{
		{NodeID: "fleet-0", BizType: "data_collector", Metadata: map[string]any{"function_name_prefix": "fleet", "index": float64(0)}},
		{NodeID: "other-0", BizType: "factor_calculator", Metadata: map[string]any{"function_name_prefix": "other", "index": float64(0)}},
	}
	selected, err := selectCollectorFleetNodes(nodes, "fleet", "data_collector", 50)
	require.NoError(t, err)
	require.Len(t, selected, 50)
	assert.Equal(t, "fleet-0", selected[0].NodeID)
	assert.Empty(t, selected[1].NodeID)
}

func TestSelectCollectorFleetNodesRequiresUniqueCompleteIndexes(t *testing.T) {
	nodes := make([]adminclient.CloudNode, 50)
	for index := range nodes {
		nodes[index] = adminclient.CloudNode{
			NodeID:   fmt.Sprintf("fleet-%d", index),
			BizType:  "data_collector",
			Metadata: map[string]any{"function_name_prefix": "fleet", "index": float64(index)},
		}
	}
	selected, err := selectCollectorFleetNodes(nodes, "fleet", "data_collector", 50)
	require.NoError(t, err)
	require.Len(t, selected, 50)

	nodes[49].Metadata["index"] = float64(48)
	_, err = selectCollectorFleetNodes(nodes, "fleet", "data_collector", 50)
	require.ErrorContains(t, err, "duplicate fleet index")
}

func TestSelectCollectorFleetNodesSurvivesHeartbeatMetadataReplacement(t *testing.T) {
	nodes := make([]adminclient.CloudNode, 50)
	for index := range nodes {
		nodes[index] = adminclient.CloudNode{
			NodeID:       fmt.Sprintf("fleet-ap-guangzhou-%d", index),
			FunctionName: fmt.Sprintf("fleet-ap-guangzhou-%d", index),
			Region:       "ap-guangzhou",
			BizType:      "data_collector",
			Metadata:     map[string]any{"arch": "amd64", "version": "runtime"},
		}
	}
	selected, err := selectCollectorFleetNodes(nodes, "fleet", "data_collector", 50)
	require.NoError(t, err)
	require.Len(t, selected, 50)
	assert.Equal(t, "fleet-ap-guangzhou-49", selected[49].NodeID)
}

func TestSelectCollectorFleetNodesRejectsCrossBizFleet(t *testing.T) {
	nodes := []adminclient.CloudNode{{
		NodeID:  "fleet-0",
		BizType: "factor_calculator",
		Metadata: map[string]any{
			"function_name_prefix": "fleet",
			"index":                float64(0),
		},
	}}
	_, err := selectCollectorFleetNodes(nodes, "fleet", "data_collector", 1)
	require.ErrorContains(t, err, `has biz_type "factor_calculator"; expected "data_collector"`)
}

type fakeCollectorFleetAPI struct {
	nodes       []adminclient.CloudNode
	createCalls [][]adminclient.NodeCreateItem
	deployCalls [][]adminclient.NodeDeployItem
}

func (f *fakeCollectorFleetAPI) ListCloudNodes(context.Context, adminclient.CloudNodeListFilter) ([]adminclient.CloudNode, error) {
	return append([]adminclient.CloudNode(nil), f.nodes...), nil
}

func (f *fakeCollectorFleetAPI) SubmitCreateNodes(_ context.Context, items []adminclient.NodeCreateItem) (*adminclient.SubmitNodeBatchResponse, error) {
	f.createCalls = append(f.createCalls, append([]adminclient.NodeCreateItem(nil), items...))
	return &adminclient.SubmitNodeBatchResponse{
		JobID:      fmt.Sprintf("create-%d", len(f.createCalls)),
		Operation:  "NODE_BATCH_OPERATION_CREATE_NODES",
		TotalCount: len(items),
	}, nil
}

func (f *fakeCollectorFleetAPI) SubmitDeployNodes(_ context.Context, items []adminclient.NodeDeployItem) (*adminclient.SubmitNodeBatchResponse, error) {
	f.deployCalls = append(f.deployCalls, append([]adminclient.NodeDeployItem(nil), items...))
	return &adminclient.SubmitNodeBatchResponse{
		JobID:      fmt.Sprintf("deploy-%d", len(f.deployCalls)),
		Operation:  "NODE_BATCH_OPERATION_DEPLOY_NODES",
		TotalCount: len(items),
	}, nil
}

func TestCollectorPublishSubmitCommandExists(t *testing.T) {
	cmd, args, err := collectorFunctionPublishCmd.Find([]string{"submit"})
	require.NoError(t, err)
	require.Empty(t, args)
	assert.Same(t, collectorFunctionPublishSubmitCmd, cmd)
}

func TestCollectorPublishStatusCommandExists(t *testing.T) {
	cmd, args, err := collectorFunctionPublishCmd.Find([]string{"status"})
	require.NoError(t, err)
	require.Empty(t, args)
	assert.Same(t, collectorFunctionPublishStatusCmd, cmd)
}

func TestPublishSubmitReturnsAfterJobSubmission(t *testing.T) {
	api := &fakeCollectorFleetAPI{}
	summary, err := submitCollectorFleet(context.Background(), api, collectorPublishOptions{
		NodeCount: 1,
	}, "pkg-new", []adminclient.NodeCreateItem{{PackageID: "pkg-new"}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "create-1", summary.JobID)
	assert.Equal(t, "create_nodes", summary.Operation)
	assert.Equal(t, 1, summary.TotalCount)
}

func TestPublishSubmitCreateFleetUsesOneJob(t *testing.T) {
	items := make([]adminclient.NodeCreateItem, 50)
	for index := range items {
		items[index] = adminclient.NodeCreateItem{
			PackageID: "pkg-new",
			Metadata:  map[string]any{"function_name_prefix": "fleet", "index": index},
		}
	}
	api := &fakeCollectorFleetAPI{}
	summary, err := submitCollectorFleet(context.Background(), api, collectorPublishOptions{
		NodeCount: 50,
	}, "pkg-new", items, nil)
	require.NoError(t, err)
	assert.Equal(t, "created", summary.FleetMode)
	assert.Len(t, api.createCalls, 1)
	assert.Len(t, api.createCalls[0], 50)
	assert.Empty(t, api.deployCalls)
	assert.Equal(t, "create-1", summary.JobID)
	assert.Equal(t, 50, summary.TotalCount)
}

func TestPublishSubmitDeployFleetUsesOneJob(t *testing.T) {
	nodes := make([]adminclient.CloudNode, 50)
	for index := range nodes {
		nodes[index] = adminclient.CloudNode{
			NodeID:    fmt.Sprintf("fleet-%d", index),
			PackageID: "pkg-old",
			BizType:   "data_collector",
			Metadata:  map[string]any{"function_name_prefix": "fleet", "index": float64(index)},
		}
	}
	api := &fakeCollectorFleetAPI{nodes: nodes}
	createItems := make([]adminclient.NodeCreateItem, 50)
	for index := range createItems {
		createItems[index] = adminclient.NodeCreateItem{
			Config:      map[string]string{"timeout": "120", "cls_topic_id": "topic-new"},
			Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "pkg-new", "MOOX_SPACE_ID": "crypto"},
		}
	}
	summary, err := submitCollectorFleet(context.Background(), api, collectorPublishOptions{
		NodeCount: 50,
	}, "pkg-new", createItems, nodes)
	require.NoError(t, err)
	assert.Equal(t, "updated", summary.FleetMode)
	assert.Empty(t, api.createCalls)
	assert.Len(t, api.deployCalls, 1)
	assert.Len(t, api.deployCalls[0], 50)
	assert.Equal(t, "deploy-1", summary.JobID)
	assert.Equal(t, "deploy_nodes", summary.Operation)
	for _, item := range api.deployCalls[0] {
		assert.Equal(t, "pkg-new", item.PackageID)
		assert.Equal(t, createItems[0].Config, item.Config)
		assert.Equal(t, createItems[0].Environment, item.Environment)
	}
}

func TestPublishSubmitExpandsPartialFleet(t *testing.T) {
	nodes := make([]adminclient.CloudNode, 4)
	nodes[0] = adminclient.CloudNode{NodeID: "fleet-0", PackageID: "pkg-old", BizType: "data_collector", Metadata: map[string]any{"function_name_prefix": "fleet", "index": float64(0)}}
	items := make([]adminclient.NodeCreateItem, 4)
	for index := range items {
		items[index] = adminclient.NodeCreateItem{PackageID: "pkg-new", Metadata: map[string]any{"function_name_prefix": "fleet", "index": index}}
	}
	api := &fakeCollectorFleetAPI{}
	summary, err := submitCollectorFleet(context.Background(), api, collectorPublishOptions{NodeCount: 4}, "pkg-new", items, nodes)
	require.NoError(t, err)
	assert.Equal(t, "expanded", summary.FleetMode)
	assert.Equal(t, "create_nodes", summary.Operation)
	assert.Equal(t, 3, summary.TotalCount)
	require.Len(t, api.createCalls, 1)
	assert.Empty(t, api.deployCalls)
	require.Len(t, api.createCalls[0], 3)
	assert.Equal(t, 1, api.createCalls[0][0].Metadata["index"])
	assert.Equal(t, 2, api.createCalls[0][1].Metadata["index"])
	assert.Equal(t, 3, api.createCalls[0][2].Metadata["index"])
}

func TestPublishSubmitRejectsOversizedFleetBeforeUpload(t *testing.T) {
	credentialFile := setCollectorFleetRuntimeTestEnvironment(t)
	uploadCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/admin/cloudnode/ListCloudAccounts":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"accounts":[{"account_id":"account-a"}]}`))
		case "/api/admin/cloudnode/GetNodeList":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"items":[{"node_id":"fleet-50","biz_type":"market_fetcher","metadata":{"function_name_prefix":"fleet","index":50}}],"page":{"has_more":false}}`))
		case "/api/admin/cloudnode/InitPackageUpload":
			uploadCalled = true
			t.Fatal("partial fleet must fail before upload")
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	_, err := publishCollectorFunction(context.Background(), collectorPublishOptions{
		ControlURL:             server.URL,
		AccessToken:            "token",
		SpaceID:                "crypto",
		CloudAccountID:         "account-a",
		Region:                 "ap-guangzhou",
		NodeCount:              50,
		FunctionNamePrefix:     "fleet",
		EventBusCredentialFile: credentialFile,
	})
	require.ErrorContains(t, err, `fleet prefix "fleet" has invalid index metadata`)
	assert.False(t, uploadCalled)
}

func TestPublishSubmitRejectsNodeCountAboveBatchLimitBeforeControlPlaneAccess(t *testing.T) {
	controlPlaneCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		controlPlaneCalled = true
		t.Fatalf("node count validation must run before control-plane access: %s", r.URL.Path)
	}))
	defer server.Close()

	_, err := publishCollectorFunction(context.Background(), collectorPublishOptions{
		ControlURL:     server.URL,
		AccessToken:    "token",
		SpaceID:        "crypto",
		CloudAccountID: "account-a",
		Region:         "ap-guangzhou",
		NodeCount:      maxCollectorPublishNodeCount + 1,
	})

	require.ErrorContains(t, err, "--node-count must be between 1 and 100")
	assert.False(t, controlPlaneCalled)
}

func TestPublishStatusPrintsJobAndItems(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/admin/cloudnode/GetNodeBatchChange", r.URL.Path)
		_, _ = w.Write([]byte(`{"ret_info":{"code":0},"job":{"job_id":"node-batch-1","status":"NODE_BATCH_STATUS_PARTIAL","total_count":2},"items":[{"item_id":"item-1","node_id":"node-1","status":"NODE_BATCH_ITEM_STATUS_SUCCESS"},{"item_id":"item-2","node_id":"node-2","status":"NODE_BATCH_ITEM_STATUS_FAILED","error_message":"failed"}]}`))
	}))
	defer server.Close()
	status, err := publishCollectorFunctionStatus(context.Background(), collectorPublishStatusOptions{
		ControlURL: server.URL,
		JobID:      "node-batch-1",
	})
	require.NoError(t, err)
	assert.Equal(t, "node-batch-1", status.Job.JobID)
	assert.Equal(t, "NODE_BATCH_STATUS_PARTIAL", status.Job.Status)
	require.Len(t, status.Items, 2)
}

func TestCollectorCLSCredentialsPreferDedicatedRuntimeIdentity(t *testing.T) {
	t.Setenv("MOOX_CLS_SECRET_ID", "dedicated-id")
	t.Setenv("MOOX_CLS_SECRET_KEY", "dedicated-key")
	t.Setenv("TENCENTCLOUD_SECRET_ID", "control-id")
	t.Setenv("TENCENTCLOUD_SECRET_KEY", "control-key")

	secretID, secretKey := collectorCLSCredentials()
	assert.Equal(t, "dedicated-id", secretID)
	assert.Equal(t, "dedicated-key", secretKey)
}

func TestResolveCollectorCLSSinkUsesSelectedCloudAccountSecret(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/service/secret/GetSecretValue", r.URL.Path)
		require.NotEmpty(t, r.Header.Get("X-Moox-Signature"))
		_, _ = w.Write([]byte(`{"ret_info":{"code":0},"secret":{"secret_id":"secret-shanghai","category":"cloud","provider":"tencent","status":"active","key_id":"shanghai-id","secret_value":"shanghai-key"}}`))
	}))
	defer server.Close()
	client := adminclient.New(server.URL)
	client.ServiceAuth = &adminclient.ServiceAuthConfig{AccessKey: "ak", SecretKey: "sk", Caller: "moox-cli", TargetNode: "gateway", ExpireSecs: 60}
	previous := newCollectorCLSAPI
	defer func() { newCollectorCLSAPI = previous }()
	var gotID, gotKey, gotRegion string
	newCollectorCLSAPI = func(secretID, secretKey, region string) (tencent.CLSAPI, error) {
		gotID, gotKey, gotRegion = secretID, secretKey, region
		return collectorCLSAPI{}, nil
	}
	_, err := resolveCollectorCLSSink(context.Background(), client, adminclient.CloudAccount{AccountID: "tencent-scf-shanghai", CredentialSecretID: "secret-shanghai"})
	require.NoError(t, err)
	assert.Equal(t, "shanghai-id", gotID)
	assert.Equal(t, "shanghai-key", gotKey)
	assert.Equal(t, clsprepare.Region, gotRegion)
}

func TestBuildCollectorCreateNodeItemRejectsLegacyJobItemWorkloads(t *testing.T) {
	setCollectorCLSTestCredentials(t)
	_, err := buildCollectorCreateNodeItem(collectorPublishOptions{
		CloudAccountID: "account-a",
		Region:         "ap-guangzhou",
		JobTypes:       []string{"kline_realtime"},
	}, "moox-collector_dev")
	require.ErrorContains(t, err, "does not consume CloudNode JobItem workloads")
}

func TestBuildCollectorCreateNodeItemDefaultsToGoRuntime(t *testing.T) {
	item := mustBuildCollectorCreateNodeItem(t, collectorPublishOptions{
		CloudAccountID: "account-a",
		Region:         "ap-guangzhou",
	}, "moox-collector_dev")

	if item.Runtime != "Go1" {
		t.Fatalf("runtime = %q, want Go1", item.Runtime)
	}
}

func TestBuildCollectorCreateNodeItemRejectsUnsafeRuntimeOverride(t *testing.T) {
	setCollectorCLSTestCredentials(t)
	_, err := buildCollectorCreateNodeItem(collectorPublishOptions{
		CloudAccountID: "account-a",
		Region:         "ap-guangzhou",
		Config: []string{
			"realtime_batch_size=64",
			"max_inflight_requests=1",
			"request_timeout_ms=7000",
		},
	}, "moox-collector_dev")
	require.ErrorContains(t, err, "request waves")
}

func TestBuildCollectorCreateNodeItemRejectsInvalidInflightOverride(t *testing.T) {
	setCollectorCLSTestCredentials(t)
	_, err := buildCollectorCreateNodeItem(collectorPublishOptions{
		CloudAccountID: "account-a",
		Region:         "ap-guangzhou",
		Config:         []string{"max_inflight_requests=65"},
	}, "moox-collector_dev")
	require.ErrorContains(t, err, "max_inflight_requests must be between 1 and 64")
}

func TestBuildCollectorCreateNodeItemRejectsMalformedRuntimeOverride(t *testing.T) {
	setCollectorCLSTestCredentials(t)
	_, err := buildCollectorCreateNodeItem(collectorPublishOptions{
		CloudAccountID: "account-a",
		Region:         "ap-guangzhou",
		Config:         []string{"max_inflight_requests=not-a-number"},
	}, "moox-collector_dev")
	require.ErrorContains(t, err, "max_inflight_requests must be an integer")
}

func TestCollectorFunctionEnvironmentRejectsManagedGatewayOverride(t *testing.T) {
	setCollectorCLSTestCredentials(t)
	_, err := buildCollectorCreateNodeItem(collectorPublishOptions{
		CloudAccountID:   "account-a",
		SpaceID:          "crypto",
		ServiceAccessKey: "svc-ak",
		ServiceSecretKey: "svc-sk",
		Region:           "ap-guangzhou",
		Env: []string{
			"MOOX_SPACE_ID=override-space",
			"MOOX_GATEWAY_SERVICE_KEY_ID=override-ak",
			"MOOX_GATEWAY_SERVICE_SECRET_KEY=override-sk",
			"MOOX_GATEWAY_SERVICE_EXPIRE_SECONDS=60",
		},
	}, "moox-collector_dev")
	require.ErrorContains(t, err, "managed key")
}

func TestCollectorFunctionEnvironmentRejectsManagedSpaceOverride(t *testing.T) {
	setCollectorCLSTestCredentials(t)
	_, err := collectorFunctionEnvironment(collectorPublishOptions{
		SpaceID: "crypto",
		Env:     []string{"MOOX_SPACE_ID=stocks"},
	})
	require.ErrorContains(t, err, "managed key MOOX_SPACE_ID")
}

func TestCollectorFunctionEnvironmentRejectsGatewayCallerOverride(t *testing.T) {
	setCollectorCLSTestCredentials(t)
	_, err := collectorFunctionEnvironment(collectorPublishOptions{
		Env: []string{"MOOX_GATEWAY_CALLER=moox-cli"},
	})
	require.ErrorContains(t, err, "managed key MOOX_GATEWAY_CALLER")
}

func TestDeployCollectorFunctionWithExistingZip(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "collector.zip")
	require.NoError(t, os.WriteFile(zipPath, []byte("fake-zip"), 0o600))

	uploadBase := ""
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/admin/cloudnode/InitPackageUpload":
			uploadBase = server.URL + "/cos-upload"
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ret_info":   map[string]any{"code": 0, "msg": "ok"},
				"package_id": "pkg-1",
				"upload_url": uploadBase,
			})
		case "/cos-upload":
			w.WriteHeader(http.StatusOK)
		case "/api/admin/cloudnode/CompletePackageUpload":
			_ = json.NewEncoder(w).Encode(map[string]any{"ret_info": map[string]any{"code": 0, "msg": "ok"}})
		case "/api/admin/cloudnode/SubmitDeployNodes":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ret_info":    map[string]any{"code": 0, "msg": "ok"},
				"job_id":      "node-batch-1",
				"operation":   "NODE_BATCH_OPERATION_DEPLOY_NODES",
				"total_count": 1,
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	summary, err := deployCollectorFunction(context.Background(), collectorDeployOptions{
		ControlURL:     server.URL,
		CloudAccountID: "account-a",
		NodeID:         "node-1",
		ZipPath:        zipPath,
	})
	require.NoError(t, err)
	assert.Equal(t, zipPath, summary.ZipPath)
	assert.Equal(t, "pkg-1", summary.PackageID)
	assert.Equal(t, "node-batch-1", summary.JobID)
	assert.Equal(t, "deploy_nodes", summary.Operation)
	assert.Equal(t, 1, summary.TotalCount)
}

func TestResolveCollectorRootMissing(t *testing.T) {
	_, err := resolveCollectorRoot("/nonexistent/path")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collector root not found")
}

func TestValidateCollectorPublishAuth(t *testing.T) {
	t.Setenv("MOOX_ACCESS_TOKEN", "")
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "")

	require.ErrorContains(t, validateCollectorPublishAuth(collectorPublishOptions{}), "control authentication")
	require.NoError(t, validateCollectorPublishAuth(collectorPublishOptions{AccessToken: "token"}))
	require.Error(t, validateCollectorPublishAuth(collectorPublishOptions{ServiceAccessKey: "key"}))
	require.NoError(t, validateCollectorPublishAuth(collectorPublishOptions{
		ServiceAccessKey: "key",
		ServiceSecretKey: "secret",
	}))
}
