package command

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestCollectorFunctionEnvironmentEmbedsCAFileMaterial(t *testing.T) {
	setCollectorCLSTestCredentials(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	caFile := filepath.Join(t.TempDir(), "peer.pem")
	require.NoError(t, os.WriteFile(caFile, pemBytes, 0o600))
	t.Setenv("MOOX_GATEWAY_CA_FILE", caFile)
	env, err := collectorFunctionEnvironment(collectorPublishOptions{})
	require.NoError(t, err)
	assert.Equal(t, base64.StdEncoding.EncodeToString(pemBytes), env["MOOX_GATEWAY_CA_PEM_B64"])
	assert.NotContains(t, env, "MOOX_GATEWAY_CA_FILE")
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
	newCollectorCLSAPI = func() (tencent.CLSAPI, error) { return collectorCLSAPI{}, nil }
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
		Config:           []string{"timeout=60"},
		Env:              []string{"MOOX_ENV=prod"},
	}, "moox-collector_dev")

	if item.CloudAccountID != "account-a" || item.Region != "ap-guangzhou" || item.PackageID != "moox-collector_dev" {
		t.Fatalf("routing fields = %#v", item)
	}
	if item.Config["timeout"] != "60" {
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
	if len(workloads) != 2 || workloads[0] != "collect.kline" || workloads[1] != "collect.symbol" {
		t.Fatalf("supported_workloads = %#v", workloads)
	}
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
		},
	}, "moox-collector_dev")
	require.ErrorContains(t, err, "managed key")
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
