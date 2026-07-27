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
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
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
	if os.Getenv("MOOX_CLS_SECRET_ID") == "" && os.Getenv("TENCENTCLOUD_SECRET_ID") == "" {
		t.Setenv("TENCENTCLOUD_SECRET_ID", "test-cls-id")
	}
	if os.Getenv("MOOX_CLS_SECRET_KEY") == "" && os.Getenv("TENCENTCLOUD_SECRET_KEY") == "" {
		t.Setenv("TENCENTCLOUD_SECRET_KEY", "test-cls-key")
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

func TestCollectorFunctionEnvironmentRequiresRuntimeCLSCredentials(t *testing.T) {
	t.Setenv("MOOX_CLS_SECRET_ID", "")
	t.Setenv("MOOX_CLS_SECRET_KEY", "")
	t.Setenv("TENCENTCLOUD_SECRET_ID", "")
	t.Setenv("TENCENTCLOUD_SECRET_KEY", "")
	t.Setenv("TENCENT_SECRET_ID", "")
	t.Setenv("TENCENT_SECRET_KEY", "")
	_, err := collectorFunctionEnvironment(collectorPublishOptions{})
	require.ErrorContains(t, err, "CLS runtime host and credentials are required")
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

func TestResolveCollectorCLSResourcesUsesTencentCloudAPI(t *testing.T) {
	original := newCollectorCLSAPI
	newCollectorCLSAPI = func(string, string) (tencent.CLSAPI, error) { return collectorCLSAPI{}, nil }
	t.Cleanup(func() { newCollectorCLSAPI = original })

	resources, err := resolveCollectorCLSResources(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "logset-from-api", resources.LogsetID)
	assert.Equal(t, "topic-from-api", resources.TopicID)
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
		BizType:          "data_collector",
		NodeType:         "scf-event",
		Env:              []string{"MOOX_ENV=prod"},
	}, "moox-collector_dev")

	if item.CloudAccountID != "account-a" || item.Region != "ap-guangzhou" || item.PackageID != "moox-collector_dev" {
		t.Fatalf("routing fields = %#v", item)
	}
	if item.Config["timeout"] != "120" {
		t.Fatalf("config = %#v", item.Config)
	}
	if item.Config["cls_logset_id"] != "logset-unified" || item.Config["cls_topic_id"] != "topic-unified" {
		t.Fatalf("CLS function config = %#v", item.Config)
	}
	if item.Environment["MOOX_ENV"] != "prod" {
		t.Fatalf("env = %#v", item.Environment)
	}
	if item.Environment["MOOX_SPACE_ID"] != "crypto" {
		t.Fatalf("space env = %#v", item.Environment)
	}
	if item.Environment["MOOX_GATEWAY_SERVICE_KEY_ID"] != "collector" || item.Environment["MOOX_GATEWAY_SERVICE_SECRET_KEY"] != "collector-secret" {
		t.Fatalf("service auth env = %#v", item.Environment)
	}
	if item.Environment["MOOX_CLS_HOST"] != "ap-guangzhou.cls.tencentyun.com" || item.Environment["MOOX_CLS_SECRET_ID"] != "cls-id" || item.Environment["MOOX_CLS_SECRET_KEY"] != "cls-key" {
		t.Fatalf("CLS env = %#v", item.Environment)
	}
	if item.Metadata["function_name_prefix"] != "moox-collector" {
		t.Fatalf("function_name_prefix = %#v", item.Metadata["function_name_prefix"])
	}
	if item.Metadata["biz_type"] != "data_collector" {
		t.Fatalf("biz_type = %#v", item.Metadata["biz_type"])
	}
	workloads, ok := item.Metadata["supported_workloads"].([]string)
	if !ok {
		t.Fatalf("supported_workloads type = %T", item.Metadata["supported_workloads"])
	}
	if len(workloads) != 2 || workloads[0] != "collect.binance.kline" || workloads[1] != "collect.binance.symbol" {
		t.Fatalf("supported_workloads = %#v", workloads)
	}
	assert.Equal(t, "collect.binance.kline,collect.binance.symbol", item.Environment["MOOX_COLLECTOR_JOB_TYPES"])
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

func TestSelectCollectorFleetNodesRejectsPartialFleet(t *testing.T) {
	nodes := []adminclient.CloudNode{
		{NodeID: "fleet-0", BizType: "data_collector", Metadata: map[string]any{"function_name_prefix": "fleet", "index": float64(0)}},
		{NodeID: "other-0", BizType: "factor_calculator", Metadata: map[string]any{"function_name_prefix": "other", "index": float64(0)}},
	}
	_, err := selectCollectorFleetNodes(nodes, "fleet", "data_collector", 50)
	require.ErrorContains(t, err, `fleet prefix "fleet" has 1 nodes; expected either 0 or 50`)
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

func (f *fakeCollectorFleetAPI) BatchCreateNodes(_ context.Context, items []adminclient.NodeCreateItem) (*adminclient.BatchChangeResponse, error) {
	f.createCalls = append(f.createCalls, append([]adminclient.NodeCreateItem(nil), items...))
	return &adminclient.BatchChangeResponse{
		BatchID:        fmt.Sprintf("create-%d", len(f.createCalls)),
		ProcessedCount: len(items),
	}, nil
}

func (f *fakeCollectorFleetAPI) BatchDeployNodes(_ context.Context, items []adminclient.NodeDeployItem) (*adminclient.BatchChangeResponse, error) {
	f.deployCalls = append(f.deployCalls, append([]adminclient.NodeDeployItem(nil), items...))
	return &adminclient.BatchChangeResponse{
		BatchID:        fmt.Sprintf("deploy-%d", len(f.deployCalls)),
		ProcessedCount: len(items),
	}, nil
}

func TestApplyCollectorFleetCreatesInSerialBatches(t *testing.T) {
	items := make([]adminclient.NodeCreateItem, 50)
	for index := range items {
		items[index] = adminclient.NodeCreateItem{
			PackageID: "pkg-new",
			Metadata:  map[string]any{"function_name_prefix": "fleet", "index": index},
		}
	}
	api := &fakeCollectorFleetAPI{}
	summary, err := applyCollectorFleet(context.Background(), api, collectorPublishOptions{
		CloudAccountID:     "account-a",
		Region:             "ap-guangzhou",
		NodeType:           "scf-event",
		BizType:            "data_collector",
		FunctionNamePrefix: "fleet",
		NodeCount:          50,
		CreateBatchSize:    5,
		DeployBatchSize:    1,
	}, "pkg-new", items)
	require.NoError(t, err)
	assert.Equal(t, "created", summary.FleetMode)
	assert.Len(t, api.createCalls, 10)
	assert.Empty(t, api.deployCalls)
	assert.Len(t, summary.CreateBatchIDs, 10)
	assert.Equal(t, 50, summary.CreateProcessedCount)
}

func TestApplyCollectorFleetDeploysNewPackageToExistingFleet(t *testing.T) {
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
	summary, err := applyCollectorFleet(context.Background(), api, collectorPublishOptions{
		CloudAccountID:     "account-a",
		Region:             "ap-guangzhou",
		NodeType:           "scf-event",
		BizType:            "data_collector",
		FunctionNamePrefix: "fleet",
		NodeCount:          50,
		CreateBatchSize:    5,
		DeployBatchSize:    1,
	}, "pkg-new", createItems)
	require.NoError(t, err)
	assert.Equal(t, "updated", summary.FleetMode)
	assert.Empty(t, api.createCalls)
	assert.Len(t, api.deployCalls, 50)
	assert.Len(t, summary.DeployBatchIDs, 50)
	assert.Equal(t, 50, summary.DeployProcessedCount)
	assert.Zero(t, summary.DeploySkippedCount)
	assert.Equal(t, 1, summary.DeployBatchSize)
	for _, batch := range api.deployCalls {
		for _, item := range batch {
			assert.Equal(t, "pkg-new", item.PackageID)
			assert.Equal(t, createItems[0].Config, item.Config)
			assert.Equal(t, createItems[0].Environment, item.Environment)
		}
	}
}

func TestApplyCollectorFleetSkipsNodesAlreadyOnRequestedPackage(t *testing.T) {
	nodes := make([]adminclient.CloudNode, 50)
	items := make([]adminclient.NodeCreateItem, 50)
	for index := range nodes {
		nodes[index] = adminclient.CloudNode{
			NodeID:    fmt.Sprintf("fleet-%d", index),
			PackageID: "pkg-new",
			BizType:   "data_collector",
			Metadata:  map[string]any{"function_name_prefix": "fleet", "index": float64(index)},
		}
	}
	api := &fakeCollectorFleetAPI{nodes: nodes}
	summary, err := applyCollectorFleet(context.Background(), api, collectorPublishOptions{
		FunctionNamePrefix: "fleet",
		NodeCount:          50,
		CreateBatchSize:    5,
		DeployBatchSize:    1,
	}, "pkg-new", items)
	require.NoError(t, err)
	assert.Equal(t, "updated", summary.FleetMode)
	assert.Empty(t, api.deployCalls)
	assert.Zero(t, summary.DeployProcessedCount)
	assert.Equal(t, 50, summary.DeploySkippedCount)
	assert.Equal(t, 1, summary.DeployBatchSize)
}

func TestApplyCollectorFleetRejectsPartialWithoutMutation(t *testing.T) {
	api := &fakeCollectorFleetAPI{nodes: []adminclient.CloudNode{{
		NodeID:   "fleet-0",
		BizType:  "data_collector",
		Metadata: map[string]any{"function_name_prefix": "fleet", "index": float64(0)},
	}}}
	_, err := applyCollectorFleet(context.Background(), api, collectorPublishOptions{
		FunctionNamePrefix: "fleet",
		NodeCount:          50,
		CreateBatchSize:    5,
		DeployBatchSize:    1,
	}, "pkg-new", make([]adminclient.NodeCreateItem, 50))
	require.ErrorContains(t, err, `fleet prefix "fleet" has 1 nodes`)
	assert.Empty(t, api.createCalls)
	assert.Empty(t, api.deployCalls)
}

func TestApplyCollectorFleetRejectsShortDesiredItemsForExistingFleet(t *testing.T) {
	api := &fakeCollectorFleetAPI{}
	_, err := applyCollectorFleet(context.Background(), api, collectorPublishOptions{
		FunctionNamePrefix: "fleet",
		NodeCount:          50,
		CreateBatchSize:    5,
		DeployBatchSize:    1,
	}, "pkg-new", make([]adminclient.NodeCreateItem, 49))
	require.ErrorContains(t, err, "create item count=49")
	assert.Empty(t, api.createCalls)
	assert.Empty(t, api.deployCalls)
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

func TestBuildCollectorCreateNodeItemNormalizesJobTypesAndKeepsMetadataEnvironmentInSync(t *testing.T) {
	item := mustBuildCollectorCreateNodeItem(t, collectorPublishOptions{
		CloudAccountID: "account-a",
		Region:         "ap-guangzhou",
		JobTypes: []string{
			" collect.binance.symbol ",
			"collect.binance.kline",
			"collect.binance.symbol",
		},
	}, "moox-collector_dev")

	workloads, ok := item.Metadata["supported_workloads"].([]string)
	require.True(t, ok)
	assert.Equal(t, []string{"collect.binance.symbol", "collect.binance.kline"}, workloads)
	assert.Equal(t, strings.Join(workloads, ","), item.Environment["MOOX_COLLECTOR_JOB_TYPES"])
}

func TestBuildCollectorCreateNodeItemRejectsInvalidJobTypes(t *testing.T) {
	tests := []struct {
		name     string
		jobTypes []string
		want     string
	}{
		{name: "empty", jobTypes: []string{""}, want: "must not be empty"},
		{name: "whitespace", jobTypes: []string{"  "}, want: "must not be empty"},
		{name: "unknown", jobTypes: []string{"collect.tushare.kline"}, want: "unsupported collector job type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setCollectorCLSTestCredentials(t)
			_, err := buildCollectorCreateNodeItem(collectorPublishOptions{
				CloudAccountID: "account-a",
				Region:         "ap-guangzhou",
				JobTypes:       tt.jobTypes,
			}, "moox-collector_dev")
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestCollectorFunctionPublishJobTypesFlagDefaultsToBinance(t *testing.T) {
	got, err := collectorFunctionPublishCmd.Flags().GetStringSlice("job-types")
	require.NoError(t, err)
	assert.Equal(t, []string{"collect.binance.kline", "collect.binance.symbol"}, got)
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
			"MOOX_COLLECTOR_JOB_TYPES=collect.binance.symbol",
		},
	}, "moox-collector_dev")
	require.ErrorContains(t, err, "managed key")
}

func TestCollectorFunctionEnvironmentRejectsManagedJobTypesOverride(t *testing.T) {
	setCollectorCLSTestCredentials(t)
	_, err := collectorFunctionEnvironment(collectorPublishOptions{
		Env: []string{"MOOX_COLLECTOR_JOB_TYPES=collect.binance.symbol"},
	})
	require.ErrorContains(t, err, "managed key MOOX_COLLECTOR_JOB_TYPES")
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
		case "/api/admin/cloudnode/BatchDeployNodes":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ret_info":        map[string]any{"code": 0, "msg": "ok"},
				"batch_id":        "batch-1",
				"processed_count": 1,
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
	assert.Equal(t, "batch-1", summary.DeployBatchID)
	assert.Equal(t, 1, summary.DeployProcessedCount)
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
