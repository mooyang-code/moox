package command

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"debug/elf"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"maps"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/mooyang-code/moox/modules/cli/internal/privatenet"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/testfixture"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"trpc.group/trpc-go/trpc-go/errs"
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

func TestValidateSCFPublishOverrideCapacityIncludesOtherSpaces(t *testing.T) {
	snapshot := &setupconfig.Snapshot{Manifest: setupconfig.Manifest{SCFFetcher: setupconfig.SCFFetcher{
		TencentLimits: setupconfig.TencentSCFLimits{
			MaxNamespacesPerRegion:       5,
			MaxFunctionsPerNamespace:     50,
			MaxBurstConcurrencyPerMinute: 500,
		},
		Spaces: []setupconfig.SCFFetcherSpace{
			{SpaceID: "crypto", Namespace: "default", Regions: []setupconfig.SCFFetcherRegion{{Region: "ap-guangzhou", Enabled: true, FunctionCount: 24}}},
			{SpaceID: "other", Namespace: "default", Regions: []setupconfig.SCFFetcherRegion{{Region: "ap-guangzhou", Enabled: true, FunctionCount: 24}}},
		},
	}}}
	require.NoError(t, validateSCFPublishOverrideCapacity(snapshot, "crypto", "ap-guangzhou", 24))
	require.ErrorContains(t, validateSCFPublishOverrideCapacity(snapshot, "crypto", "ap-guangzhou", 26), "above max_functions_per_namespace")
}

func TestValidateSCFPublishOverrideCapacityAllowsOverflowNamespaces(t *testing.T) {
	snapshot := &setupconfig.Snapshot{Manifest: setupconfig.Manifest{SCFFetcher: setupconfig.SCFFetcher{
		TencentLimits: setupconfig.TencentSCFLimits{MaxNamespacesPerRegion: 5, MaxFunctionsPerNamespace: 50},
		Spaces: []setupconfig.SCFFetcherSpace{
			{SpaceID: "crypto", Namespace: "moox-crypto", Regions: []setupconfig.SCFFetcherRegion{{Region: "ap-nanjing", Enabled: true, FunctionCount: 49}}},
		},
	}}}
	require.NoError(t, validateSCFPublishOverrideCapacity(snapshot, "crypto", "ap-nanjing", 54))
	require.ErrorContains(t, validateSCFPublishOverrideCapacity(snapshot, "crypto", "ap-nanjing", 251), "above max_namespaces_per_region")
}

func TestValidateSCFPublishOverrideCanRepairOriginalQuotaOverflow(t *testing.T) {
	snapshot := &setupconfig.Snapshot{Manifest: setupconfig.Manifest{SCFFetcher: setupconfig.SCFFetcher{
		TencentLimits: setupconfig.TencentSCFLimits{MaxNamespacesPerRegion: 5, MaxFunctionsPerNamespace: 50},
		Spaces: []setupconfig.SCFFetcherSpace{
			{SpaceID: "crypto", Namespace: "default", Regions: []setupconfig.SCFFetcherRegion{{Region: "ap-singapore", Enabled: true, FunctionCount: 25}}},
			{SpaceID: "other", Namespace: "default", Regions: []setupconfig.SCFFetcherRegion{{Region: "ap-singapore", Enabled: true, FunctionCount: 24}}},
		},
	}}}
	// The declared plan is 51 including two Invoke canaries, but an explicit
	// single-region rollout can shrink crypto to 24 and fit the quota.
	require.NoError(t, validateSCFPublishOverrideCapacity(snapshot, "crypto", "ap-singapore", 24))
}

func TestSCFCLSIngestHostUsesPublicEndpoint(t *testing.T) {
	assert.Equal(t, "ap-guangzhou.cls.tencentcs.com", scfCLSIngestHost("ap-guangzhou.cls.tencentyun.com"))
	assert.Equal(t, "custom.example.test", scfCLSIngestHost("custom.example.test"))
}

func setCollectorFleetRuntimeTestEnvironment(t *testing.T) string {
	t.Helper()
	setCollectorCLSTestCredentials(t)
	t.Setenv("MOOX_CLS_ENDPOINT", "ap-guangzhou.cls.tencentyun.com")
	t.Setenv("MOOX_CLS_TOPIC_ID", "topic-test")
	t.Setenv("MOOX_GATEWAY_NODE_ID", "gateway-e2e")
	t.Setenv("MOOX_COLLECTOR_GATEWAY_SERVICE_KEY_ID", "collector")
	t.Setenv("MOOX_COLLECTOR_GATEWAY_SERVICE_SECRET_KEY", "collector-secret")
	t.Setenv("MOOX_STORAGE_RPC_GATEWAY_TARGET", "ip://106.53.107.122:11003")
	t.Setenv("MOOX_COLLECTOR_RPC_GATEWAY_TARGET", "ip://203.0.113.10:11003")
	t.Setenv("MOOX_COLLECTOR_GATEWAY_TARGET_NODE", "collector-node")

	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(server.Close)
	gatewayCA := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	dir := t.TempDir()
	gatewayCAFile := filepath.Join(dir, "gateway-ca.pem")
	require.NoError(t, os.WriteFile(gatewayCAFile, gatewayCA, 0o600))
	t.Setenv("MOOX_GATEWAY_CA_FILE", gatewayCAFile)
	t.Setenv("MOOX_SERVICE_GATEWAY_CA_FILE", gatewayCAFile)

	eventBusCAFile := filepath.Join(dir, "eventbus-ca.pem")
	require.NoError(t, os.WriteFile(eventBusCAFile, mustTestEventBusCAPEM(t), 0o600))
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "test-primary-secret")
	credentialFile := filepath.Join(dir, "eventbus.yaml")
	require.NoError(t, os.WriteFile(credentialFile, []byte(
		"version: 1\nurls: [tls://203.0.113.10:4222]\nusername: worker\npassword: worker-secret\nca_file: eventbus-ca.pem\n",
	), 0o600))
	return credentialFile
}

const collectorTestStorageAppKeysJSON = `{"moox-collector":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`

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

func TestCollectorFunctionEnvironmentUsesResolvedControlTrustMaterial(t *testing.T) {
	setCollectorCLSTestCredentials(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	controlCA := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	staleCA := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("stale")})
	t.Setenv("MOOX_GATEWAY_CA_PEM_B64", base64.StdEncoding.EncodeToString(staleCA))
	t.Setenv("MOOX_SERVICE_GATEWAY_CA_PEM_B64", base64.StdEncoding.EncodeToString(staleCA))

	env, err := collectorFunctionEnvironment(collectorPublishOptions{
		EventBusCredential: &jetstream.CredentialFile{
			URLs:     []string{"tls://203.0.113.10:4222"},
			Username: "publisher",
			Password: "publisher-secret",
			CAFile:   "ca.pem",
		},
		collectorPackageOptions: collectorPackageOptions{EventBusCAPEM: mustTestEventBusCAPEM(t)},
		GatewayCAPEM:            controlCA,
		ServiceGatewayCAPEM:     controlCA,
	}, "package-control")
	require.NoError(t, err)
	assert.Equal(t, "certs/eventbus-ca.pem", env["MOOX_EVENTBUS_NATS_TLS_CA_FILE"])
	assert.NotContains(t, env, "MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64")
	assert.Equal(t, base64.StdEncoding.EncodeToString(controlCA), env["MOOX_GATEWAY_CA_PEM_B64"])
	assert.Equal(t, base64.StdEncoding.EncodeToString(controlCA), env["MOOX_SERVICE_GATEWAY_CA_PEM_B64"])
}

func TestPreflightCollectorSCFEventBusCredentialUsesManifestPublicEndpoint(t *testing.T) {
	ca := mustTestEventBusCAPEM(t)
	credential, err := preflightCollectorSCFEventBusCredential(
		jetstream.CredentialFile{
			URLs:     []string{"tls://127.0.0.1:4222"},
			Username: "publisher",
			Password: "publisher-secret",
			CAFile:   "ca.pem",
		},
		ca,
		setupconfig.EventBus{PublicAddress: "eventbus.example.test", Port: 4333, TLSEnabled: true},
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"tls://eventbus.example.test:4333"}, credential.URLs)
	assert.Equal(t, "publisher", credential.Username)
	assert.Equal(t, "publisher-secret", credential.Password)
	assert.Equal(t, "ca.pem", credential.CAFile)
}

func TestPreflightCollectorSCFEventBusCredentialRejectsUnsafeManifest(t *testing.T) {
	baseCredential := jetstream.CredentialFile{
		URLs:     []string{"tls://127.0.0.1:4222"},
		Username: "publisher",
		Password: "publisher-secret",
	}
	validCA := mustTestEventBusCAPEM(t)
	tests := []struct {
		name     string
		eventBus setupconfig.EventBus
		ca       []byte
		want     string
	}{
		{name: "loopback public address", eventBus: setupconfig.EventBus{PublicAddress: "127.0.0.1", Port: 4222, TLSEnabled: true}, ca: validCA, want: "non-loopback"},
		{name: "unspecified public address", eventBus: setupconfig.EventBus{PublicAddress: "0.0.0.0", Port: 4222, TLSEnabled: true}, ca: validCA, want: "non-loopback"},
		{name: "tls disabled", eventBus: setupconfig.EventBus{PublicAddress: "eventbus.example.test", Port: 4222}, ca: validCA, want: "tls_enabled"},
		{name: "missing ca", eventBus: setupconfig.EventBus{PublicAddress: "eventbus.example.test", Port: 4222, TLSEnabled: true}, want: "CA material"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := preflightCollectorSCFEventBusCredential(baseCredential, tt.ca, tt.eventBus)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func mustTestEventBusCAPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "moox-test-eventbus-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestCollectorStoragePrimaryAuthSecretUsesNormalizedControlFile(t *testing.T) {
	secret, err := collectorStoragePrimaryAuthSecret([]byte("MOOX_STORAGE_PRIMARY_AUTH_SECRET=primary-secret\nMOOX_STORAGE_VIEW_AUTH_SECRET=view-secret\n"))
	require.NoError(t, err)
	assert.Equal(t, "primary-secret", secret)
	_, err = collectorStoragePrimaryAuthSecret([]byte("MOOX_STORAGE_PRIMARY_AUTH_SECRET=old\nMOOX_STORAGE_PRIMARY_AUTH_SECRET=new\nMOOX_STORAGE_VIEW_AUTH_SECRET=view\n"))
	assert.Error(t, err)
}

func TestCollectorPublicationDerivesOnlyAllowedBindingAppIDs(t *testing.T) {
	raw := []byte("storage:\n  bindings:\n    spot:\n      auth_info: {app_id: moox-collector, app_key: ''}\n    swap:\n      auth_info: {app_id: moox-collector, app_key: ''}\n")
	encoded, err := collectorStorageBindingAppKeys(raw, "test-master-secret")
	require.NoError(t, err)
	var keys map[string]string
	require.NoError(t, json.Unmarshal([]byte(encoded), &keys))
	require.Len(t, keys, 1)
	require.Len(t, keys["moox-collector"], 64)
	require.NotContains(t, encoded, "test-master-secret")
	_, err = collectorStorageBindingAppKeys(raw, "")
	require.ErrorContains(t, err, "Primary auth secret")
	_, err = collectorStorageBindingAppKeys([]byte("storage:\n  bindings:\n    x:\n      auth_info: {app_id: administrator, app_key: ''}\n"), "test-master-secret")
	require.ErrorContains(t, err, "allowed")
}

func TestCollectorZipUploadRejectsCredentialsBeforeControlAccess(t *testing.T) {
	setCollectorCLSTestCredentials(t)
	t.Setenv("MOOX_CLS_TOPIC_ID", "test-topic")
	t.Setenv("MOOX_GATEWAY_NODE_ID", "storage-node")
	t.Setenv("MOOX_COLLECTOR_GATEWAY_SERVICE_KEY_ID", "collector")
	t.Setenv("MOOX_COLLECTOR_GATEWAY_SERVICE_SECRET_KEY", "test-service-secret")
	zipPath := filepath.Join(t.TempDir(), "external.zip")
	writeMinimalSCFZip(t, zipPath, map[string]string{"sources/market/extra.yaml": "password: leaked-test-password\n", "main": "binary", "certs/eventbus-ca.pem": string(mustTestEventBusCAPEM(t))})
	called := 0
	server := newControlFixtureServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called++ }))
	defer server.Close()
	_, err := publishCollectorFunction(context.Background(), collectorPublishOptions{ControlURL: server.URL, SpaceID: "stockcn", StorageRPCGatewayTarget: "ip://storage.example:11003", CloudAccountID: "account", Region: "ap-singapore", NodeCount: 1, AccessToken: "test-token", ZipPath: zipPath})
	require.ErrorContains(t, err, "password")
	require.NotContains(t, err.Error(), "leaked-test-password")
	require.Zero(t, called)
	_, err = deployCollectorFunction(context.Background(), collectorDeployOptions{ControlURL: server.URL, CloudAccountID: "account", NodeID: "node", ZipPath: zipPath})
	require.Error(t, err)
	require.Zero(t, called)
}

func TestCollectorTimerEnvironmentOmitsUnrelatedHTTPSCAs(t *testing.T) {
	t.Setenv("MOOX_CLS_SECRET_ID", "cls-id")
	t.Setenv("MOOX_CLS_SECRET_KEY", "cls-key")
	t.Setenv("MOOX_GATEWAY_CA_PEM_B64", "")
	t.Setenv("MOOX_SERVICE_GATEWAY_CA_PEM_B64", "")
	env, err := collectorFunctionEnvironment(collectorPublishOptions{TriggerType: "timer", StorageRPCGatewayTarget: "ip://storage:11003"}, "pkg-timer")
	require.NoError(t, err)
	assert.Equal(t, "ip://storage:11003", env["MOOX_STORAGE_RPC_GATEWAY_TARGET"])
	assert.NotContains(t, env, "MOOX_GATEWAY_CA_PEM_B64")
	assert.NotContains(t, env, "MOOX_SERVICE_GATEWAY_CA_PEM_B64")
}

func TestCollectorTimerEnvironmentIncludesDurableCredentialsAndCAPath(t *testing.T) {
	setCollectorCLSTestCredentials(t)
	ca := mustTestEventBusCAPEM(t)
	opts := collectorPublishOptions{
		TriggerType: "timer", BizType: "market_fetcher", SpaceID: "stockcn",
		CollectorRPCGatewayTarget: "ip://collector.example:11003", CollectorGatewayTargetNode: "collector-node",
		StorageRPCGatewayTarget: "ip://storage:11003", StorageAppKeysJSON: `{"moox-collector":"` + strings.Repeat("a", 64) + `"}`,
		EventBusCredential:      &jetstream.CredentialFile{URLs: []string{"tls://eventbus.example:4222"}, Username: "publisher", Password: "test-password"},
		collectorPackageOptions: collectorPackageOptions{EventBusCAPEM: ca},
	}
	env, err := collectorFunctionEnvironment(opts, "pkg-timer")
	require.NoError(t, err)
	require.Equal(t, "test-password", env["MOOX_EVENTBUS_NATS_PASSWORD"])
	require.Equal(t, "certs/eventbus-ca.pem", env["MOOX_EVENTBUS_NATS_TLS_CA_FILE"])
	require.Equal(t, opts.StorageAppKeysJSON, env["MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON"])
	require.NotContains(t, env, "MOOX_STORAGE_PRIMARY_AUTH_SECRET")
	require.NotContains(t, env, "MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64")
	require.Equal(t, "60", env["MOOX_FETCH_TIMEOUT_SECONDS"])
	require.Equal(t, "ip://collector.example:11003", env["MOOX_COLLECTOR_RPC_GATEWAY_TARGET"])
	require.Equal(t, "collector-node", env["MOOX_COLLECTOR_GATEWAY_TARGET_NODE"])
	require.LessOrEqual(t, tencent.SCFEnvironmentBytes(env), 4096)
	for _, key := range []string{"MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON", "MOOX_STORAGE_PRIMARY_AUTH_SECRET", "MOOX_EVENTBUS_NATS_TLS_CA_FILE", "MOOX_COLLECTOR_RPC_GATEWAY_TARGET", "MOOX_COLLECTOR_GATEWAY_TARGET_NODE"} {
		opts.Env = []string{key + "=forbidden-value"}
		_, err := collectorFunctionEnvironment(opts, "pkg-timer")
		require.ErrorContains(t, err, "managed key "+key)
	}
	opts.Env = []string{"PROVIDER_EXTRA=" + strings.Repeat("private-value", 400)}
	_, err = collectorFunctionEnvironment(opts, "pkg-timer")
	require.ErrorContains(t, err, "4096")
	require.NotContains(t, err.Error(), "private-value")
}

func TestCollectorEnvironmentUsesPublicStorageTargetForAllSCF(t *testing.T) {
	t.Setenv("MOOX_CLS_SECRET_ID", "cls-id")
	t.Setenv("MOOX_CLS_SECRET_KEY", "cls-key")
	opts := collectorPublishOptions{
		TriggerType:                    "timer",
		StorageRPCGatewayTarget:        "ip://146.56.196.204:11003",
		StoragePrivateRPCGatewayTarget: "ip://10.206.0.5:11003",
	}
	for _, region := range []string{"ap-guangzhou", "ap-shanghai", "ap-hongkong", "ap-singapore"} {
		opts.Region = region
		env, err := collectorFunctionEnvironment(opts, "pkg-timer")
		require.NoError(t, err)
		assert.Equal(t, "ip://146.56.196.204:11003", env["MOOX_STORAGE_RPC_GATEWAY_TARGET"], region)
	}
}

func TestCollectorInvokeMarketFetcherKeepsEventBusButOmitsHTTPSCAs(t *testing.T) {
	setCollectorCLSTestCredentials(t)
	env, err := collectorFunctionEnvironment(collectorPublishOptions{
		TriggerType: "invoke", BizType: "market_fetcher", StorageRPCGatewayTarget: "ip://storage:11003",
		EventBusCredential:      &jetstream.CredentialFile{URLs: []string{"tls://eventbus.example:4222"}, Username: "worker", Password: "secret"},
		collectorPackageOptions: collectorPackageOptions{EventBusCAPEM: mustTestEventBusCAPEM(t)}, GatewayCAPEM: []byte("gateway-ca"), ServiceGatewayCAPEM: []byte("service-ca"),
	}, "pkg-invoke")
	require.NoError(t, err)
	assert.Equal(t, "tls://eventbus.example:4222", env["MOOX_EVENTBUS_NATS_URL"])
	assert.NotContains(t, env, "MOOX_GATEWAY_CA_PEM_B64")
	assert.NotContains(t, env, "MOOX_SERVICE_GATEWAY_CA_PEM_B64")
}

func TestLoadCollectorSCFFetcherConfigSelectsOnlyRequestedSpace(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "moox.toml")
	content := `[admin]
username = "admin"
password = "password"

[tencent_cloud]
secret_id = "secret-id"
secret_key = "secret-key"
region = "ap-guangzhou"

[eventbus]
host = "192.0.2.10"
port = 4222
tls_enabled = true

[hosts."192.0.2.10"]
port = 22
username = "ubuntu"
password = "password"

[hosts."106.53.107.122"]
port = 22
username = "ubuntu"
password = "password"

[control_host]
name = "control"
host = "192.0.2.10"

[scf_fetcher]
enabled = true

[scf_fetcher.cloud_account]
account_id = "tencent-scf"
account_name = "Tencent SCF"
credential_secret_id = "tencent-default"
app_id = "1255382561"
cos_region = "ap-guangzhou"
cos_bucket = "moox-scf-guangzhou-1255382561"

[[scf_fetcher.spaces]]
space_id = "crypto"
storage_gateway_host = "106.53.107.122"
entrypoint = "crypto"
package_config_dir = "scf/crypto"
package_name = "moox-collector-crypto-market"
function_prefix = "moox-fetcher-crypto-market"
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

[[scf_fetcher.spaces]]
space_id = "stockcn"
timer_function_count = 1
measured_safe_group_size = 1
storage_gateway_host = "106.53.107.122"
package_config_dir = "scf/stockcn"
package_name = "moox-collector-stockcn"
function_prefix = "moox-fetcher-stockcn"
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
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	cryptoMarket, err := loadCollectorSCFFetcherConfig(path, "crypto")
	require.NoError(t, err)
	require.NotNil(t, cryptoMarket)
	assert.Equal(t, "crypto", cryptoMarket.SpaceID)
	assert.Equal(t, "crypto", cryptoMarket.Entrypoint)
	assert.Equal(t, "scf/crypto", cryptoMarket.PackageConfigDir)
	assert.Equal(t, "ap-singapore", cryptoMarket.Regions[0].Region)

	_, err = loadCollectorSCFFetcherConfig(path, "stockus")
	require.ErrorContains(t, err, `no configuration for space "stockus"`)
}

func TestEnsureCollectorSpaceCloudAccountsRegistersOnlyMissingAccount(t *testing.T) {
	server := newControlFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/admin/cloudnode/CreateCloudAccount", r.URL.Path)
		_, _ = w.Write([]byte(`{"ret_info":{"code":0},"account":{"account_id":"tencent-scf-singapore","cos_region":"ap-singapore","cos_bucket":"moox-scf-singapore-1255382561"}}`))
	}))
	defer server.Close()

	accounts := map[string]adminclient.CloudAccount{}
	err := ensureCollectorSpaceCloudAccounts(context.Background(), collectorTestClient(server), &setupconfig.SCFFetcherSpace{
		SpaceID: "crypto",
		Regions: []setupconfig.SCFFetcherRegion{{
			Region: "ap-singapore", Enabled: true, CloudAccountID: "tencent-scf-singapore",
			CloudAccountName: "Tencent SCF Singapore", CredentialSecretID: "tencent-default",
			AppID: "1255382561", COSRegion: "ap-guangzhou", COSBucket: "moox-scf-guangzhou-1255382561",
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

func TestResolveCollectorProbeExpectedCountUsesStockCNFleetConfig(t *testing.T) {
	configured := &setupconfig.SCFFetcherSpace{
		SpaceID:            "stockcn",
		TimerFunctionCount: 200,
		Regions: []setupconfig.SCFFetcherRegion{
			{Region: "ap-guangzhou", Enabled: true, FunctionCount: 50},
			{Region: "ap-shanghai", Enabled: true, FunctionCount: 50},
			{Region: "ap-beijing", Enabled: true, FunctionCount: 50},
			{Region: "ap-chengdu", Enabled: true, FunctionCount: 50},
		},
	}

	count, err := resolveCollectorProbeExpectedCount("stockcn", "", configured)
	require.NoError(t, err)
	assert.Equal(t, 200, count)

	count, err = resolveCollectorProbeExpectedCount("stockcn", "ap-shanghai", configured)
	require.NoError(t, err)
	assert.Equal(t, 50, count)

	count, err = resolveCollectorProbeExpectedCount("stockcn", "", nil)
	require.NoError(t, err)
	assert.Equal(t, setupconfig.DefaultStockCNMarketTimerFunctionCount, count)

	count, err = resolveCollectorProbeExpectedCount("crypto", "", nil)
	require.NoError(t, err)
	assert.Zero(t, count)
}

func TestSelectCollectorProbeNodesKeepsCryptoSemanticsAndStockTimersOnly(t *testing.T) {
	nodes := []adminclient.CloudNode{
		{NodeID: "timer-a", PackageID: "pkg", TriggerType: "timer", Metadata: map[string]any{"deployment_ready": true}},
		{NodeID: "invoke-a", PackageID: "pkg", TriggerType: "invoke", Metadata: map[string]any{"deployment_ready": true}},
	}

	assert.Len(t, selectCollectorProbeNodes(nodes, false), 2)
	stockNodes := selectCollectorProbeNodes(nodes, true)
	require.Len(t, stockNodes, 1)
	assert.Equal(t, "timer-a", stockNodes[0].NodeID)
}

func TestCollectorEgressProbeReportKeepsOnlyDiagnosticCounts(t *testing.T) {
	report := &collectorProbeReport{
		Results: []collectorProbeResult{
			{NodeID: "node-a", OutboundIP: "198.51.100.1"},
			{NodeID: "node-b", OutboundIP: "198.51.100.1"},
		},
		ExpectedCount: 170,
		EligibleCount: 2,
		DistinctCount: 1,
	}
	assert.Equal(t, 170, report.ExpectedCount)
	assert.Equal(t, 2, report.EligibleCount)
	assert.Equal(t, 1, report.DistinctCount)
}

func TestDefaultCryptoCollectorTasksSelectOnlyKlineFields(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "config", "setup", "collection-tasks.yaml"))
	require.NoError(t, err)
	var bundle struct {
		Tasks []struct {
			SpaceID       string `yaml:"space_id"`
			TaskName      string `yaml:"task_name"`
			DataType      string `yaml:"data_type"`
			CollectParams struct {
				Frequency    string   `yaml:"frequency"`
				OutputFields []string `yaml:"output_fields"`
			} `yaml:"collect_params"`
		} `yaml:"tasks"`
	}
	require.NoError(t, yaml.Unmarshal(content, &bundle))
	wantFields := []string{"open", "high", "low", "close", "volume", "quote_volume", "trade_num"}
	wantTasks := map[string]bool{
		"Binance 现货 K 线 1m": false,
		"Binance 合约 K 线 1m": false,
		"Binance 现货 K 线 1h": false,
		"Binance 合约 K 线 1h": false,
	}
	for _, task := range bundle.Tasks {
		if task.SpaceID != "crypto" || task.DataType != "kline" {
			continue
		}
		if _, ok := wantTasks[task.TaskName]; !ok {
			continue
		}
		wantTasks[task.TaskName] = true
		assert.Equal(t, wantFields, task.CollectParams.OutputFields, task.TaskName)
	}
	for name, found := range wantTasks {
		assert.True(t, found, "missing default task %s", name)
	}
}

func TestDefaultStockCNCollectorTasksRequireExplicitActivation(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "config", "setup", "collection-tasks.yaml"))
	require.NoError(t, err)
	var bundle struct {
		Tasks []struct {
			SpaceID       string         `yaml:"space_id"`
			DataType      string         `yaml:"data_type"`
			TagIDs        []string       `yaml:"tag_ids"`
			Enabled       bool           `yaml:"enabled"`
			CollectParams map[string]any `yaml:"collect_params"`
		} `yaml:"tasks"`
	}
	require.NoError(t, yaml.Unmarshal(content, &bundle))
	found := false
	for _, task := range bundle.Tasks {
		if task.SpaceID != "stockcn" || task.DataType != "kline" || !slices.Contains(task.TagIDs, "cn_a_share") {
			continue
		}
		if fmt.Sprint(task.CollectParams["frequency"]) != "1m" {
			continue
		}
		found = true
		assert.False(t, task.Enabled)
	}
	assert.True(t, found, "default stockcn 1m task should exist and remain disabled until explicit activation")
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
	ca := mustTestEventBusCAPEM(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ca.pem"), ca, 0o600))
	credentialPath := filepath.Join(dir, "market-fetch-publisher.yaml")
	require.NoError(t, os.WriteFile(credentialPath, []byte(
		"version: 1\nurls: [tls://203.0.113.10:4222]\nusername: market-fetch-publisher\ntoken: worker-token\nca_file: ca.pem\n",
	), 0o600))
	env, err := collectorFunctionEnvironment(collectorPublishOptions{
		EventBusCredentialFile: credentialPath,
		CLSSecretID:            "cls-id",
		CLSSecretKey:           "cls-key",
	}, "pkg-1")
	require.NoError(t, err)
	assert.Equal(t, "tls://203.0.113.10:4222", env["MOOX_EVENTBUS_NATS_URL"])
	assert.Equal(t, "market-fetch-publisher", env["MOOX_EVENTBUS_NATS_USERNAME"])
	assert.Equal(t, "worker-token", env["MOOX_EVENTBUS_NATS_PASSWORD"])
	assert.Equal(t, "certs/eventbus-ca.pem", env["MOOX_EVENTBUS_NATS_TLS_CA_FILE"])
	assert.Equal(t, "pkg-1", env["MOOX_CODE_PACKAGE_ID"])
	assert.NotContains(t, env, "MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64")

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
	assert.Equal(t, "10", env["MOOX_FETCH_MAX_INFLIGHT_REQUESTS"])
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

func TestBuildCollectorCreateNodeItemUsesManagedShortLivedConfiguration(t *testing.T) {
	t.Setenv("MOOX_CLS_ENDPOINT", "ap-guangzhou.cls.tencentyun.com")
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
	if item.Config["timeout"] != "60" || item.Config["memory_size"] != "64" {
		t.Fatalf("config = %#v", item.Config)
	}
	assert.NotContains(t, item.Config, "cls_logset_id")
	assert.NotContains(t, item.Config, "cls_topic_id")
	assert.Equal(t, "ENABLE", item.Config["public_net_status"])
	if item.Environment["MOOX_ENV"] != "prod" {
		t.Fatalf("env = %#v", item.Environment)
	}
	if item.Environment["MOOX_SPACE_ID"] != "crypto" {
		t.Fatalf("space env = %#v", item.Environment)
	}
	if item.Environment["MOOX_GATEWAY_SERVICE_KEY_ID"] != "collector" || item.Environment["MOOX_GATEWAY_SERVICE_SECRET_KEY"] != "collector-secret" {
		t.Fatalf("service auth env = %#v", item.Environment)
	}
	assert.Equal(t, "ap-guangzhou.cls.tencentyun.com", item.Environment["MOOX_CLS_ENDPOINT"])
	assert.Equal(t, "logset-unified", item.Environment["MOOX_CLS_LOGSET_ID"])
	assert.Equal(t, "topic-unified", item.Environment["MOOX_CLS_TOPIC_ID"])
	assert.Equal(t, "cls-id", item.Environment["MOOX_CLS_SECRET_ID"])
	assert.Equal(t, "cls-key", item.Environment["MOOX_CLS_SECRET_KEY"])
	if item.Metadata["function_name_prefix"] != "moox-collector" {
		t.Fatalf("function_name_prefix = %#v", item.Metadata["function_name_prefix"])
	}
	if item.Metadata["biz_type"] != "market_fetcher" {
		t.Fatalf("biz_type = %#v", item.Metadata["biz_type"])
	}
	assert.NotContains(t, item.Metadata, "supported_workloads")
	assert.NotContains(t, item.Environment, "MOOX_COLLECTOR_JOB_TYPES")
}

func TestApplyCollectorStorageRouteUsesPrivateVPCForSameRegion(t *testing.T) {
	route := privatenet.SCFStorageRoute{
		Region: "ap-nanjing", SameRegion: true, Network: "vpc",
		Target: "ip://10.206.0.5:11003", VpcID: "vpc-storage", SubnetID: "subnet-storage",
	}
	opts := applyCollectorStorageRoute(collectorPublishOptions{Region: "ap-nanjing"}, map[string]privatenet.SCFStorageRoute{"ap-nanjing": route}, &setupconfig.SCFFetcherSpace{StorageRPCGatewayTarget: "ip://146.56.196.204:11003"}, false)
	assert.Equal(t, "ip://10.206.0.5:11003", opts.StorageRPCGatewayTarget)
	assert.Equal(t, "vpc-storage", opts.StorageVPCID)
	assert.Equal(t, "subnet-storage", opts.StorageSubnetID)
	assert.True(t, opts.StorageRouteResolved)
	item := mustBuildCollectorCreateNodeItem(t, opts, "pkg")
	assert.Equal(t, "vpc-storage", item.Config["vpc_id"])
	assert.Equal(t, "subnet-storage", item.Config["subnet_id"])
	assert.Equal(t, "ip://10.206.0.5:11003", item.Environment["MOOX_STORAGE_RPC_GATEWAY_TARGET"])
}

func TestApplyCollectorStorageRouteUsesPublicForCrossRegion(t *testing.T) {
	route := privatenet.SCFStorageRoute{Region: "ap-hongkong", Network: "public", Target: "ip://146.56.196.204:11003"}
	opts := applyCollectorStorageRoute(collectorPublishOptions{Region: "ap-hongkong"}, map[string]privatenet.SCFStorageRoute{"ap-hongkong": route}, &setupconfig.SCFFetcherSpace{StorageRPCGatewayTarget: "ip://146.56.196.204:11003"}, false)
	assert.Equal(t, "ip://146.56.196.204:11003", opts.StorageRPCGatewayTarget)
	assert.True(t, opts.StorageRouteResolved)
	item := mustBuildCollectorCreateNodeItem(t, opts, "pkg")
	assert.NotContains(t, item.Config, "vpc_id")
	assert.NotContains(t, item.Config, "subnet_id")
	assert.Equal(t, "true", item.Config["clear_vpc"])
}

func TestOrderCollectorPublishRegionsStorageFirst(t *testing.T) {
	input := []setupconfig.SCFFetcherRegion{
		{Region: "ap-hongkong"}, {Region: "ap-nanjing"}, {Region: "ap-singapore"},
	}
	ordered := orderCollectorPublishRegions(input, "ap-nanjing", true)
	require.Equal(t, []string{"ap-nanjing", "ap-hongkong", "ap-singapore"}, []string{ordered[0].Region, ordered[1].Region, ordered[2].Region})
	// Ordering must not mutate the manifest slice used for subsequent commands.
	assert.Equal(t, "ap-hongkong", input[0].Region)
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
		StorageAppKeysJSON:     collectorTestStorageAppKeysJSON,
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

func TestBuildCollectorFleetCreateItemsContinuesOverflowIndexes(t *testing.T) {
	credentialFile := setCollectorFleetRuntimeTestEnvironment(t)
	items, err := buildCollectorFleetCreateItems(collectorPublishOptions{
		CloudAccountID:         "account-a",
		SpaceID:                "crypto",
		Region:                 "ap-nanjing",
		FunctionNamePrefix:     "moox-fetcher-crypto-binance",
		NodeCount:              11,
		IndexOffset:            49,
		EventBusCredentialFile: credentialFile,
		StorageAppKeysJSON:     collectorTestStorageAppKeysJSON,
	}, "pkg-new")
	require.NoError(t, err)
	require.Len(t, items, 11)
	assert.Equal(t, 49, items[0].Metadata["index"])
	assert.Equal(t, 59, items[10].Metadata["index"])
}

func TestCollectorShardIndexOffsetsKeepIndexesUniqueAcrossNamespaces(t *testing.T) {
	shards := []setupconfig.SCFNamespaceShard{
		{Namespace: "moox-crypto", Invokes: 50},
		{Namespace: "moox-crypto-ns2", Invokes: 30},
		{Namespace: "timer-a", Timers: 49, Invokes: 1},
		{Namespace: "timer-b", Timers: 5},
	}

	offsets := collectorShardIndexOffsets(shards)
	require.Equal(t, []collectorShardIndexOffset{
		{Timer: 0, Invoke: 0},
		{Timer: 0, Invoke: 50},
		{Timer: 0, Invoke: 80},
		{Timer: 49, Invoke: 81},
	}, offsets)
}

func TestSelectCollectorFleetNodesMapsOverflowIndexes(t *testing.T) {
	nodes := []adminclient.CloudNode{
		{NodeID: "fleet-49", Namespace: "moox-crypto-ns2", TriggerType: "timer", BizType: "data_collector", Metadata: map[string]any{"function_name_prefix": "fleet", "index": float64(49)}},
		{NodeID: "fleet-50", Namespace: "moox-crypto-ns2", TriggerType: "timer", BizType: "data_collector", Metadata: map[string]any{"function_name_prefix": "fleet", "index": float64(50)}},
	}
	selected, err := selectCollectorFleetNodesForTrigger(nodes, "fleet", "data_collector", 11, "timer", 49, "moox-crypto-ns2")
	require.NoError(t, err)
	require.Len(t, selected, 11)
	assert.Equal(t, "fleet-49", selected[0].NodeID)
	assert.Equal(t, "fleet-50", selected[1].NodeID)
	assert.Empty(t, selected[2].NodeID)
}

func TestBuildCollectorFleetCreateItemsRequiresCompleteRuntimeEnvironment(t *testing.T) {
	setCollectorCLSTestCredentials(t)
	t.Setenv("MOOX_CLS_ENDPOINT", "ap-guangzhou.cls.tencentyun.com")
	t.Setenv("MOOX_CLS_TOPIC_ID", "topic-test")
	_, err := buildCollectorFleetCreateItems(collectorPublishOptions{
		SpaceID:   "crypto",
		NodeCount: 50,
	}, "pkg-new")
	require.ErrorContains(t, err, "collector fleet runtime environment requires")
}

func TestCollectorTimerRequiresUsableClaimRoute(t *testing.T) {
	credentialFile := setCollectorFleetRuntimeTestEnvironment(t)
	opts := collectorPublishOptions{SpaceID: "stockcn", NodeCount: 1, EventBusCredentialFile: credentialFile, StorageAppKeysJSON: collectorTestStorageAppKeysJSON}
	for _, tc := range []struct{ name, target, node string }{
		{"missing target", "", "collector"},
		{"missing node", "ip://collector.example:11003", ""},
		{"invalid node", "ip://collector.example:11003", "bad node"},
		{"loopback", "ip://127.0.0.1:11003", "collector"},
		{"http", "https://collector.example:11003", "collector"},
		{"invalid port", "ip://collector.example:70000", "collector"},
		{"path", "ip://collector.example:11003/path", "collector"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MOOX_COLLECTOR_RPC_GATEWAY_TARGET", tc.target)
			t.Setenv("MOOX_COLLECTOR_GATEWAY_TARGET_NODE", tc.node)
			_, err := buildCollectorFleetCreateItems(opts, "pkg")
			require.ErrorContains(t, err, "MOOX_COLLECTOR_")
		})
	}
	t.Setenv("MOOX_COLLECTOR_RPC_GATEWAY_TARGET", "ip://collector.example:11003")
	t.Setenv("MOOX_COLLECTOR_GATEWAY_TARGET_NODE", "collector")
	_, err := buildCollectorFleetCreateItems(opts, "pkg")
	require.NoError(t, err)
}

func TestCollectorTimerRequiresEventBusEnvironment(t *testing.T) {
	credentialFile := setCollectorFleetRuntimeTestEnvironment(t)
	environment, err := collectorFunctionEnvironment(collectorPublishOptions{SpaceID: "stockcn", TriggerType: "timer", EventBusCredentialFile: credentialFile, StorageAppKeysJSON: collectorTestStorageAppKeysJSON}, "pkg")
	require.NoError(t, err)
	for _, key := range []string{"MOOX_EVENTBUS_NATS_URL", "MOOX_EVENTBUS_NATS_USERNAME", "MOOX_EVENTBUS_NATS_PASSWORD", "MOOX_EVENTBUS_NATS_TLS_CA_FILE"} {
		t.Run(key, func(t *testing.T) {
			partial := make(map[string]string, len(environment))
			for name, value := range environment {
				partial[name] = value
			}
			delete(partial, key)
			require.ErrorContains(t, validateCollectorFleetRuntimeEnvironment(partial, true), key)
		})
	}
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

func TestSelectCollectorFleetNodesDoesNotReuseOtherNamespace(t *testing.T) {
	nodes := []adminclient.CloudNode{
		{NodeID: "old-0", Namespace: "default", TriggerType: "timer", BizType: "data_collector", Metadata: map[string]any{"function_name_prefix": "fleet", "index": float64(0)}},
	}
	selected, err := selectCollectorFleetNodesForTrigger(nodes, "fleet", "data_collector", 1, "timer", 0, "crypto")
	require.NoError(t, err)
	assert.Empty(t, selected)
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
	createErr   error
}

func (f *fakeCollectorFleetAPI) ListCloudNodes(context.Context, adminclient.CloudNodeListFilter) ([]adminclient.CloudNode, error) {
	return append([]adminclient.CloudNode(nil), f.nodes...), nil
}

func (f *fakeCollectorFleetAPI) SubmitCreateNodes(_ context.Context, items []adminclient.NodeCreateItem) (*adminclient.SubmitNodeBatchResponse, error) {
	f.createCalls = append(f.createCalls, append([]adminclient.NodeCreateItem(nil), items...))
	if f.createErr != nil {
		return nil, f.createErr
	}
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

func TestDeleteCollectorFunctionsFiltersNamespace(t *testing.T) {
	server := newControlFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/admin/cloudnode/GetNodeList", r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ret_info": map[string]any{"code": 0},
			"items": []adminclient.CloudNode{
				{NodeID: "legacy-default", Namespace: "default", NodeType: "scf-event", BizType: "market_fetcher"},
				{NodeID: "new-crypto", Namespace: "moox-crypto", NodeType: "scf-event", BizType: "market_fetcher"},
				{NodeID: "deleted-default", Namespace: "default", IsDeleted: true, NodeType: "scf-event", BizType: "market_fetcher"},
			},
		})
	}))
	defer server.Close()

	summary, err := deleteCollectorFunctions(context.Background(), collectorDeleteOptions{
		ControlURL: server.URL,
		SpaceID:    "crypto",
		Namespace:  "default",
		DryRun:     true,
	})
	require.NoError(t, err)
	assert.Equal(t, "default", summary.Namespace)
	assert.Equal(t, 1, summary.TotalCount)
	assert.Equal(t, []string{"legacy-default"}, summary.NodeIDs)
}

func TestDeleteCollectorFunctionsRequiresWaitForFencedAsyncBatch(t *testing.T) {
	var calls []string
	server := newControlFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/admin/cloudnode/GetNodeList":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"items":[{"node_id":"fetcher-1","node_type":"scf-event","biz_type":"market_fetcher","namespace":"default"}],"page":{"has_more":false}}`))
		case "/api/admin/publishlease/AcquireCollectorPublishLease":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"space_id":"crypto","lease_id":"lease-1","fencing_token":"1","expires_at":"2026-10-03T12:02:00Z"}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	_, err := deleteCollectorFunctions(context.Background(), collectorDeleteOptions{
		ControlURL: server.URL, SpaceID: "crypto", Confirm: true, Wait: false,
	})
	require.ErrorContains(t, err, "requires --wait")
	assert.Empty(t, calls, "the command must reject non-waiting deletion before reading targets or acquiring a lease")
}

func TestDeleteCollectorFunctionsAcquiresLeaseBeforeTargetSnapshot(t *testing.T) {
	var calls []string
	leaseHeld := false
	server := newControlFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/admin/publishlease/AcquireCollectorPublishLease":
			leaseHeld = true
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"space_id":"crypto","lease_id":"lease-1","fencing_token":"17","expires_at":"2026-10-03T20:02:00Z"}`))
		case "/api/admin/cloudnode/GetNodeList":
			assert.True(t, leaseHeld, "target snapshot must be read inside the publish lease")
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"items":[],"page":{"has_more":false}}`))
		case "/api/admin/publishlease/ReleaseCollectorPublishLease":
			leaseHeld = false
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"released":true}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	summary, err := deleteCollectorFunctions(context.Background(), collectorDeleteOptions{
		ControlURL: server.URL, SpaceID: "crypto", Confirm: true, Wait: true,
	})
	require.NoError(t, err)
	require.Equal(t, "nothing_to_delete", summary.Status)
	assert.Equal(t, []string{
		"/api/admin/publishlease/AcquireCollectorPublishLease",
		"/api/admin/cloudnode/GetNodeList",
		"/api/admin/publishlease/ReleaseCollectorPublishLease",
	}, calls, "even an empty snapshot is protected and released under the lease")
}

func TestCleanupUnknownCanarySubmissionDoesNotTreatEmptyInventoryAsComplete(t *testing.T) {
	var calls []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := newControlFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/admin/cloudnode/GetNodeList":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"items":[],"page":{"has_more":false}}`))
			cancel()
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	client := collectorTestClient(server)

	err := cleanupCollectorSCFReleaseCanaryFleet(ctx, client, collectorPublishOptions{
		SpaceID: "crypto", CloudAccountID: "account-1", Region: "ap-singapore", Namespace: "canary",
		NodeType: "scf-event", BizType: "market_fetcher", TriggerType: "invoke", NodeCount: 1,
		FunctionNamePrefix: "r0123456789abcdef0123456789abcdef",
	}, "", false, true)
	require.ErrorIs(t, err, errCollectorBatchOutcomeUnknown)
	assert.Equal(t, []string{"/api/admin/cloudnode/GetNodeList"}, calls, "an empty inventory is not proof that an ambiguous create job is settled; the prefix remains unsettled until caller cancellation")
}

func TestDeleteCollectorFunctionsHoldsLeaseUntilNodeBatchCompletes(t *testing.T) {
	statusReads := 0
	var sawFence bool
	var released bool
	server := newControlFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/admin/cloudnode/GetNodeList":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"items":[{"node_id":"fetcher-1","node_type":"scf-event","biz_type":"market_fetcher","namespace":"default"}],"page":{"has_more":false}}`))
		case "/api/admin/publishlease/AcquireCollectorPublishLease":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"space_id":"crypto","lease_id":"lease-1","fencing_token":"17","expires_at":"2026-10-03T20:02:00Z"}`))
		case "/api/admin/cloudnode/SubmitDeleteNodes":
			var request struct {
				LeaseID string `json:"collector_publish_lease_id"`
				Token   int64  `json:"collector_publish_fencing_token"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			sawFence = request.LeaseID == "lease-1" && request.Token == 17
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"job_id":"delete-1","operation":"NODE_BATCH_OPERATION_DELETE_NODES","total_count":1}`))
		case "/api/admin/cloudnode/GetNodeBatchChange":
			statusReads++
			status := "NODE_BATCH_STATUS_RUNNING"
			if statusReads > 1 {
				status = "NODE_BATCH_STATUS_SUCCESS"
			}
			_, _ = fmt.Fprintf(w, `{"ret_info":{"code":0},"job":{"job_id":"delete-1","operation":"NODE_BATCH_OPERATION_DELETE_NODES","status":%q,"success_count":1,"total_count":1},"items":[]}`, status)
		case "/api/admin/publishlease/ReleaseCollectorPublishLease":
			released = true
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"released":true}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	summary, err := deleteCollectorFunctions(context.Background(), collectorDeleteOptions{
		ControlURL: server.URL, SpaceID: "crypto", Confirm: true, Wait: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "NODE_BATCH_STATUS_SUCCESS", summary.Status)
	assert.True(t, sawFence)
	assert.True(t, released)
	assert.GreaterOrEqual(t, statusReads, 2)
}

func TestDeleteCollectorFunctionsChunksFleetAndWaitsEveryAcceptedBatch(t *testing.T) {
	nodes := make([]adminclient.CloudNode, 170)
	for index := range nodes {
		nodes[index] = adminclient.CloudNode{NodeID: fmt.Sprintf("fetcher-%03d", index), Namespace: "default", NodeType: "scf-event", BizType: "market_fetcher"}
	}
	var submittedSizes []int
	var statusReads = map[string]int{}
	var released bool
	server := newControlFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/admin/cloudnode/GetNodeList":
			_ = json.NewEncoder(w).Encode(map[string]any{"ret_info": map[string]any{"code": 0}, "items": nodes, "page": map[string]any{"has_more": false}})
		case "/api/admin/publishlease/AcquireCollectorPublishLease":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"space_id":"stockcn","lease_id":"lease-1","fencing_token":"17","expires_at":"2026-10-03T20:02:00Z"}`))
		case "/api/admin/cloudnode/SubmitDeleteNodes":
			var request struct {
				NodeIDs []string `json:"node_ids"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			submittedSizes = append(submittedSizes, len(request.NodeIDs))
			jobID := fmt.Sprintf("delete-%d", len(submittedSizes))
			_, _ = fmt.Fprintf(w, `{"ret_info":{"code":0},"job_id":%q,"operation":"NODE_BATCH_OPERATION_DELETE_NODES","total_count":%d}`, jobID, len(request.NodeIDs))
		case "/api/admin/cloudnode/GetNodeBatchChange":
			var request struct {
				JobID string `json:"job_id"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			statusReads[request.JobID]++
			total := map[string]int{"delete-1": 100, "delete-2": 70}[request.JobID]
			status := "NODE_BATCH_STATUS_SUCCESS"
			failed, success := 0, total
			if request.JobID == "delete-1" {
				status, failed, success = "NODE_BATCH_STATUS_FAILED", 100, 0
			} else if statusReads[request.JobID] == 1 {
				status = "NODE_BATCH_STATUS_RUNNING"
				success = 0
			}
			_, _ = fmt.Fprintf(w, `{"ret_info":{"code":0},"job":{"job_id":%q,"operation":"NODE_BATCH_OPERATION_DELETE_NODES","status":%q,"success_count":%d,"failed_count":%d,"total_count":%d},"items":[]}`, request.JobID, status, success, failed, total)
		case "/api/admin/publishlease/ReleaseCollectorPublishLease":
			released = true
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"released":true}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	summary, err := deleteCollectorFunctions(context.Background(), collectorDeleteOptions{
		ControlURL: server.URL, SpaceID: "stockcn", Confirm: true, Wait: true,
	})
	require.ErrorContains(t, err, "delete-1 finished with status NODE_BATCH_STATUS_FAILED")
	assert.Equal(t, []int{100, 70}, submittedSizes)
	assert.Equal(t, "delete-1,delete-2", summary.JobID)
	assert.Equal(t, 100, summary.FailedCount)
	assert.Equal(t, 1, statusReads["delete-1"])
	assert.GreaterOrEqual(t, statusReads["delete-2"], 2, "the second job must reach terminal status after the first fails")
	assert.True(t, released, "release only after every accepted batch reaches terminal status")
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

func TestPublishSubmitPartialFleetUpdatesExistingAndCreatesMissingSlots(t *testing.T) {
	nodes := make([]adminclient.CloudNode, 4)
	nodes[0] = adminclient.CloudNode{NodeID: "fleet-0", PackageID: "pkg-old", BizType: "data_collector", Metadata: map[string]any{"function_name_prefix": "fleet", "index": float64(0)}}
	items := make([]adminclient.NodeCreateItem, 4)
	for index := range items {
		items[index] = adminclient.NodeCreateItem{PackageID: "pkg-new", Metadata: map[string]any{"function_name_prefix": "fleet", "index": index}}
	}
	api := &fakeCollectorFleetAPI{}
	summary, err := submitCollectorFleet(context.Background(), api, collectorPublishOptions{NodeCount: 4}, "pkg-new", items, nodes)
	require.NoError(t, err)
	assert.Equal(t, "updated", summary.FleetMode)
	assert.Equal(t, "deploy_nodes,create_nodes", summary.Operation)
	assert.Equal(t, "deploy-1,create-1", summary.JobID)
	assert.Equal(t, 4, summary.TotalCount)
	require.Len(t, api.deployCalls, 1)
	assert.Len(t, api.deployCalls[0], 1)
	assert.Equal(t, "fleet-0", api.deployCalls[0][0].NodeID)
	require.Len(t, api.createCalls, 1)
	assert.Len(t, api.createCalls[0], 3)
	assert.Equal(t, 1, api.createCalls[0][0].Metadata["index"])
}

func TestPublishSubmitRetainsAcceptedJobWhenLaterSubmissionIsAmbiguous(t *testing.T) {
	nodes := []adminclient.CloudNode{{NodeID: "fleet-0", PackageID: "pkg-old"}, {}}
	items := []adminclient.NodeCreateItem{{PackageID: "pkg-new"}, {PackageID: "pkg-new"}}
	api := &fakeCollectorFleetAPI{createErr: io.EOF}

	summary, err := submitCollectorFleet(context.Background(), api, collectorPublishOptions{NodeCount: 2}, "pkg-new", items, nodes)

	require.ErrorIs(t, err, errCollectorBatchOutcomeUnknown)
	assert.Equal(t, "deploy-1", summary.JobID)
	assert.Equal(t, "deploy_nodes,create_nodes", summary.Operation)
	assert.Equal(t, 2, summary.TotalCount)
}

func TestWaitCollectorBatchChecksEveryJobAfterTerminalFailure(t *testing.T) {
	var checked []string
	server := newControlFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			JobID string `json:"job_id"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		checked = append(checked, request.JobID)
		status := "NODE_BATCH_STATUS_SUCCESS"
		if request.JobID == "job-failed" {
			status = "NODE_BATCH_STATUS_FAILED"
		}
		_, _ = fmt.Fprintf(w, `{"ret_info":{"code":0},"job":{"job_id":%q,"status":%q,"failed_count":1}}`, request.JobID, status)
	}))
	defer server.Close()

	err := waitCollectorBatch(context.Background(), collectorTestClient(server), "job-failed,job-success")

	require.Error(t, err)
	assert.NotErrorIs(t, err, errCollectorBatchOutcomeUnknown)
	assert.Equal(t, []string{"job-failed", "job-success"}, checked)
}

func TestWaitCollectorBatchCanceledReturnsUnknownOutcome(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := waitCollectorBatch(ctx, adminclient.New("http://127.0.0.1:1"), "job-pending")
	require.ErrorIs(t, err, errCollectorBatchOutcomeUnknown)
}

func TestCollectorPublishUnknownSubmissionErrorsAreAmbiguous(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "native network loss", err: errs.NewFrameError(errs.RetClientNetErr, "lost response"), want: true},
		{name: "native server timeout", err: errs.NewFrameError(errs.RetServerTimeout, "timed out"), want: true},
		{name: "native authentication", err: errs.NewFrameError(errs.RetServerAuthFail, "denied"), want: false},
		{name: "gateway 503", err: fmt.Errorf("control returned HTTP 503 Service Unavailable"), want: true},
		{name: "service 500", err: fmt.Errorf("SubmitCreateNodes: code 500: internal error"), want: true},
		{name: "missing job id", err: fmt.Errorf("SubmitCreateNodes: empty job_id"), want: true},
		{name: "malformed operation", err: fmt.Errorf("SubmitCreateNodes: operation: unknown enum"), want: true},
		{name: "authorization rejection", err: fmt.Errorf("control returned HTTP 403 Forbidden"), want: false},
		{name: "business rejection", err: fmt.Errorf("SubmitCreateNodes: code 409: lease stale"), want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, isAmbiguousCollectorPublishOutcome(test.err))
		})
	}
}

func TestPublishSubmitRejectsOversizedFleetBeforeUpload(t *testing.T) {
	credentialFile := setCollectorFleetRuntimeTestEnvironment(t)
	uploadCalled := false
	server := newControlFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/admin/cloudnode/ListCloudAccounts":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"accounts":[{"account_id":"account-a"}]}`))
		case "/api/admin/cloudnode/GetNodeList":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"items":[{"node_id":"fleet-50","biz_type":"market_fetcher","trigger_type":"timer","metadata":{"function_name_prefix":"fleet","index":50}}],"page":{"has_more":false}}`))
		case "/api/admin/cloudnode/InitPackageUpload":
			uploadCalled = true
			t.Fatal("partial fleet must fail before upload")
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	_, err := publishCollectorFunction(context.Background(), collectorPublishOptions{
		collectorPackageOptions: collectorPackageOptions{CollectorRoot: filepath.Join("..", "..", "..", "collector")},
		ControlURL:              server.URL,
		AccessToken:             "token",
		SpaceID:                 "crypto",
		CloudAccountID:          "account-a",
		Region:                  "ap-guangzhou",
		NodeCount:               50,
		FunctionNamePrefix:      "fleet",
		EventBusCredentialFile:  credentialFile,
	})
	require.ErrorContains(t, err, `fleet prefix "fleet" has invalid index metadata`)
	assert.False(t, uploadCalled)
}

func TestPublishSubmitRejectsNodeCountAboveBatchLimitBeforeControlPlaneAccess(t *testing.T) {
	controlPlaneCalled := false
	server := newControlFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	server := newControlFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	server := newControlFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/service/secret/GetSecretValue", r.URL.Path)
		require.NotEmpty(t, r.Header.Get("X-Moox-Signature"))
		_, _ = w.Write([]byte(`{"ret_info":{"code":0},"secret":{"secret_id":"secret-shanghai","category":"cloud","provider":"tencent","status":"active","key_id":"shanghai-id","secret_value":"shanghai-key"}}`))
	}))
	defer server.Close()
	client := collectorTestClient(server)
	client.ServiceAuth = &adminclient.ServiceAuthConfig{AccessKey: "ak", SecretKey: "sk", Caller: "moox-cli", TargetNode: "gateway", ExpireSecs: 60}
	previous := newCollectorCLSAPI
	defer func() { newCollectorCLSAPI = previous }()
	var gotID, gotKey, gotRegion string
	newCollectorCLSAPI = func(secretID, secretKey, region string) (tencent.CLSAPI, error) {
		gotID, gotKey, gotRegion = secretID, secretKey, region
		return collectorCLSAPI{}, nil
	}
	sink, err := resolveCollectorCLSSink(context.Background(), client, adminclient.CloudAccount{AccountID: "tencent-scf-shanghai", CredentialSecretID: "secret-shanghai"}, "ap-shanghai")
	require.NoError(t, err)
	assert.Equal(t, "shanghai-id", gotID)
	assert.Equal(t, "shanghai-key", gotKey)
	assert.Equal(t, "ap-shanghai", gotRegion)
	assert.Equal(t, "shanghai-id", sink.SecretID)
	assert.Equal(t, "shanghai-key", sink.SecretKey)
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
			"realtime_batch_size=30",
			"max_inflight_requests=1",
			"request_timeout_ms=7000",
		},
	}, "moox-collector_dev")
	require.ErrorContains(t, err, "request waves")
}

func TestBuildCollectorCreateNodeItemReservesCLSFlushWindow(t *testing.T) {
	setCollectorCLSTestCredentials(t)
	_, err := buildCollectorCreateNodeItem(collectorPublishOptions{
		CloudAccountID: "account-a",
		Region:         "ap-guangzhou",
		Config: []string{
			"realtime_batch_size=30",
			"max_inflight_requests=32",
			"request_timeout_ms=11000",
		},
	}, "moox-collector_dev")
	require.ErrorContains(t, err, "configured reserves")
}

func TestBuildCollectorCreateNodeItemStandardManifestFitsTimerAndInvokeBudgets(t *testing.T) {
	setCollectorCLSTestCredentials(t)
	for _, triggerType := range []string{"timer", "invoke"} {
		_, err := buildCollectorCreateNodeItem(collectorPublishOptions{
			CloudAccountID: "account-a", Region: "ap-guangzhou", TriggerType: triggerType,
			Config: []string{"realtime_batch_size=10", "max_inflight_requests=10", "request_timeout_ms=2000"},
		}, "moox-collector_dev")
		require.NoError(t, err, "trigger_type=%s", triggerType)
	}
}

func TestBuildCollectorCreateNodeItemUsesLongStockCNInvokeTimeoutOnlyForInvoke(t *testing.T) {
	setCollectorCLSTestCredentials(t)
	fetcher := &setupconfig.SCFFetcherSpace{
		SpaceID: "stockcn", MemorySize: 64, TimeoutSeconds: 15,
		InvokeTimeoutSeconds: 60,
		RealtimeBatchSize:    10, MaxInflightRequests: 10, RequestTimeoutMS: 2000,
		HTTPMaxAttempts: 4, StorageMaxAttempts: 1, StorageTimeoutMS: 5000,
	}
	timer := mustBuildCollectorCreateNodeItem(t, collectorPublishOptions{
		SpaceID: "stockcn", CloudAccountID: "account-a", Region: "ap-guangzhou", TriggerType: "timer", FetcherConfig: fetcher,
	}, "moox-collector-stockcn_dev")
	assert.Equal(t, "60", timer.Config["timeout"])
	assert.Equal(t, "60", timer.Environment["MOOX_FETCH_TIMEOUT_SECONDS"])

	invoke := mustBuildCollectorCreateNodeItem(t, collectorPublishOptions{
		SpaceID: "stockcn", CloudAccountID: "account-a", Region: "ap-guangzhou", TriggerType: "invoke", FetcherConfig: fetcher,
	}, "moox-collector-stockcn_dev")
	assert.Equal(t, "60", invoke.Config["timeout"])
	assert.Equal(t, "60", invoke.Environment["MOOX_FETCH_TIMEOUT_SECONDS"])

	fetcher.InvokeTimeoutSeconds = 90
	configured := mustBuildCollectorCreateNodeItem(t, collectorPublishOptions{
		SpaceID: "stockcn", CloudAccountID: "account-a", Region: "ap-guangzhou", TriggerType: "invoke", FetcherConfig: fetcher,
	}, "moox-collector-stockcn_dev")
	assert.Equal(t, "90", configured.Config["timeout"])
	assert.Equal(t, "90", configured.Environment["MOOX_FETCH_TIMEOUT_SECONDS"])
}

func TestBuildCollectorCreateNodeItemUsesLongCryptoInvokeTimeout(t *testing.T) {
	setCollectorCLSTestCredentials(t)
	fetcher := &setupconfig.SCFFetcherSpace{
		SpaceID: "crypto", MemorySize: 64, TimeoutSeconds: 15,
		RealtimeBatchSize: 10, MaxInflightRequests: 10, RequestTimeoutMS: 2000,
		HTTPMaxAttempts: 4, StorageMaxAttempts: 1, StorageTimeoutMS: 5000,
	}

	item := mustBuildCollectorCreateNodeItem(t, collectorPublishOptions{
		SpaceID: "crypto", CloudAccountID: "account-a", Region: "ap-singapore", TriggerType: "invoke", FetcherConfig: fetcher,
	}, "moox-collector-crypto_dev")

	assert.Equal(t, "60", item.Config["timeout"])
	assert.Equal(t, "60", item.Environment["MOOX_FETCH_TIMEOUT_SECONDS"])
}

func TestBuildCollectorCreateNodeItemRejectsConcurrentSCFInstances(t *testing.T) {
	setCollectorCLSTestCredentials(t)
	_, err := buildCollectorCreateNodeItem(collectorPublishOptions{
		SpaceID: "stockcn", CloudAccountID: "account-a", Region: "ap-shanghai", TriggerType: "timer",
		Config: []string{"max_instance_concurrency=2"},
	}, "moox-collector-stockcn_dev")
	require.ErrorContains(t, err, "max_instance_concurrency is fixed at 1")
}

func TestCollectorTimerEnablePatchesIncludeFreshNodes(t *testing.T) {
	nodes := []adminclient.CloudNode{
		{NodeID: "sh-1", Region: "ap-shanghai", FunctionName: "stock-1", Metadata: map[string]any{"index": 1}},
		{NodeID: "gz-0", Region: "ap-guangzhou", FunctionName: "stock-0", Metadata: map[string]any{"index": 0}},
	}

	patches := collectorTimerEnablePatches(nodes)

	require.Len(t, patches, 2)
	assert.Equal(t, "gz-0", patches[0].NodeID)
	assert.Equal(t, "5 * * * * * *", patches[0].TimerCron)
	assert.Equal(t, "sh-1", patches[1].NodeID)
	assert.Equal(t, "6 * * * * * *", patches[1].TimerCron)
}

func TestCollectorTimerEnablePatchesKeepCryptoAssignmentCron(t *testing.T) {
	cfg := setupconfig.SCFFetcherSpace{SpaceID: "crypto"}
	patches := collectorTimerEnablePatches([]adminclient.CloudNode{
		{NodeID: "hourly", Metadata: map[string]any{"timer_cron": "0 4 * * * * *", "index": 0}},
		{NodeID: "fresh", Metadata: map[string]any{"index": 1}},
	}, cfg)

	require.Len(t, patches, 2)
	assert.Equal(t, "0 4 * * * * *", patches[0].TimerCron)
	assert.Equal(t, "0 * * * * * *", patches[1].TimerCron)
	assert.True(t, patches[0].TimerEnabled)
}

func TestCollectorTimerDisablePatchesCoverEveryPublishedNode(t *testing.T) {
	nodes := []adminclient.CloudNode{
		{NodeID: "node-a", Metadata: map[string]any{"timer_cron": "0 1 * * * * *"}},
		{NodeID: "node-b", Metadata: map[string]any{}},
	}

	patches := collectorTimerDisablePatches(nodes)

	require.Len(t, patches, 2)
	assert.False(t, patches[0].TimerEnabled)
	assert.Equal(t, "0 1 * * * * *", patches[0].TimerCron)
	assert.False(t, patches[1].TimerEnabled)
	assert.Equal(t, "0 * * * * * *", patches[1].TimerCron)
}

func TestCollectorTimerAssignmentReadbackRequiresCompleteExactIdentity(t *testing.T) {
	base := adminclient.CloudNode{
		NodeID: "timer-1", PackageID: "package-7",
		Metadata: map[string]any{
			"assignment_hash": "assignment-a", "assignment_count": 12,
			"binding_hash": "binding-a", "runtime_config_reconciled_at": "2026-10-03T00:00:00Z",
		},
	}
	identity, ready := collectorTimerAssignmentFromNode(base, "package-7")
	require.True(t, ready)
	require.Equal(t, collectorTimerAssignment{NodeID: "timer-1", PackageID: "package-7", AssignmentHash: "assignment-a", BindingHash: "binding-a", AssignmentCount: 12}, identity)

	for _, mutate := range []func(*adminclient.CloudNode){
		func(node *adminclient.CloudNode) { node.PackageID = "package-new" },
		func(node *adminclient.CloudNode) { delete(node.Metadata, "assignment_count") },
		func(node *adminclient.CloudNode) { node.Metadata["assignment_count"] = 0 },
		func(node *adminclient.CloudNode) { delete(node.Metadata, "assignment_hash") },
		func(node *adminclient.CloudNode) { delete(node.Metadata, "binding_hash") },
		func(node *adminclient.CloudNode) { delete(node.Metadata, "runtime_config_reconciled_at") },
	} {
		candidate := base
		candidate.Metadata = maps.Clone(base.Metadata)
		mutate(&candidate)
		_, ready := collectorTimerAssignmentFromNode(candidate, "package-7")
		require.False(t, ready, "candidate=%+v", candidate)
	}
}

func TestCollectorTimerAssignmentsMatchFenceSnapshot(t *testing.T) {
	want := map[string]collectorTimerAssignment{
		"timer-1": {NodeID: "timer-1", PackageID: "package-7", AssignmentHash: "assignment-a", BindingHash: "binding-a", AssignmentCount: 12},
	}
	require.True(t, collectorTimerAssignmentsMatch(want, maps.Clone(want)))
	changed := maps.Clone(want)
	assignment := changed["timer-1"]
	assignment.AssignmentHash = "assignment-b"
	changed["timer-1"] = assignment
	require.False(t, collectorTimerAssignmentsMatch(want, changed))
	require.False(t, collectorTimerAssignmentsMatch(want, map[string]collectorTimerAssignment{}))
}

func TestHandoffCollectorPublishLeaseWaitsForAssignmentAndReacquiresHigherFence(t *testing.T) {
	server, state := newStockCNHandoffTestServer(t, "assignment-a", "assignment-a", "assignment-a")
	defer server.Close()
	client := collectorTestClient(server)
	baseCtx := context.Background()
	_, guard, err := acquireCollectorPublishLease(baseCtx, client, "stockcn")
	require.NoError(t, err)
	fleets := []collectorPublishedTimerFleet{{
		opts:      collectorPublishOptions{SpaceID: "stockcn", Region: "ap-guangzhou", Namespace: "default", NodeType: "scf-event", BizType: "market_fetcher", TriggerType: "timer", NodeCount: 1, FunctionNamePrefix: "stock"},
		packageID: "package-7",
	}}

	leaseCtx, assignments, err := handoffCollectorPublishLeaseForTimerAssignment(baseCtx, client, "stockcn", &guard, fleets)
	require.NoError(t, err)
	require.NotNil(t, leaseCtx)
	require.Len(t, assignments, 1)
	require.Equal(t, "assignment-a", assignments["timer-1"].AssignmentHash)
	require.Equal(t, "package-7", fleets[0].nodes[0].PackageID)
	require.Equal(t, int64(2), client.CollectorPublishFence().FencingToken)
	assert.Equal(t, 2, state.acquireCount)
	assert.True(t, state.firstLeaseReleasedBeforeSecondAcquire)
	require.NoError(t, guard.Close())
}

func TestHandoffCollectorPublishLeaseRejectsChangedAssignmentWithoutRollbackAuthority(t *testing.T) {
	server, _ := newStockCNHandoffTestServer(t, "assignment-a", "assignment-a", "assignment-b")
	defer server.Close()
	client := collectorTestClient(server)
	baseCtx := context.Background()
	_, guard, err := acquireCollectorPublishLease(baseCtx, client, "stockcn")
	require.NoError(t, err)
	fleets := []collectorPublishedTimerFleet{{
		opts:      collectorPublishOptions{SpaceID: "stockcn", Region: "ap-guangzhou", Namespace: "default", NodeType: "scf-event", BizType: "market_fetcher", TriggerType: "timer", NodeCount: 1, FunctionNamePrefix: "stock"},
		packageID: "package-7",
	}}

	leaseCtx, _, err := handoffCollectorPublishLeaseForTimerAssignment(baseCtx, client, "stockcn", &guard, fleets)
	require.ErrorIs(t, err, errCollectorPublishFenceChanged)
	require.NotNil(t, leaseCtx)
	require.NotNil(t, guard, "the new lease remains held for orderly release, but callers must not rollback across the changed assignment")
	require.NoError(t, guard.Close())
}

type stockCNHandoffTestState struct {
	acquireCount                          int
	listCount                             int
	releasedLeaseID                       string
	firstLeaseReleasedBeforeSecondAcquire bool
}

func newStockCNHandoffTestServer(t *testing.T, assignmentHashes ...string) (*httptest.Server, *stockCNHandoffTestState) {
	t.Helper()
	state := &stockCNHandoffTestState{}
	server := newControlFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/admin/publishlease/AcquireCollectorPublishLease":
			state.acquireCount++
			if state.acquireCount == 2 {
				state.firstLeaseReleasedBeforeSecondAcquire = state.releasedLeaseID == "lease-1"
			}
			_, _ = fmt.Fprintf(w, `{"ret_info":{"code":0},"space_id":"stockcn","lease_id":"lease-%d","fencing_token":"%d","expires_at":"2026-10-03T20:02:00Z"}`, state.acquireCount, state.acquireCount)
		case "/api/admin/publishlease/ReleaseCollectorPublishLease":
			var request struct {
				LeaseID string `json:"lease_id"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			state.releasedLeaseID = request.LeaseID
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"released":true}`))
		case "/api/admin/cloudnode/GetNodeList":
			state.listCount++
			index := min(state.listCount-1, len(assignmentHashes)-1)
			assignmentHash := assignmentHashes[index]
			packageID := "package-7"
			_, _ = fmt.Fprintf(w, `{"ret_info":{"code":0},"items":[{"node_id":"timer-1","package_id":%q,"region":"ap-guangzhou","namespace":"default","node_type":"scf-event","biz_type":"market_fetcher","trigger_type":"timer","function_name":"stock-ap-guangzhou-0","metadata":{"index":0,"assignment_hash":%q,"binding_hash":"binding-a","assignment_count":12,"runtime_config_reconciled_at":"2026-10-03T00:00:00Z"}}],"page":{"has_more":false}}`, packageID, assignmentHash)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	return server, state
}

func TestSubmitCollectorTimerRuntimeConfigsBatchesAtCloudNodeLimit(t *testing.T) {
	batchSizes := make([]int, 0, 3)
	server := newControlFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Nodes []collectorRuntimeConfigPatch `json:"nodes"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		batchSizes = append(batchSizes, len(request.Nodes))
		_, _ = fmt.Fprintf(w, `{"ret_info":{"code":0},"job_id":"job-%d"}`, len(batchSizes))
	}))
	defer server.Close()
	patches := make([]collectorRuntimeConfigPatch, 201)
	for index := range patches {
		patches[index] = collectorRuntimeConfigPatch{NodeID: fmt.Sprintf("node-%03d", index)}
	}

	jobs, err := submitCollectorTimerRuntimeConfigs(context.Background(), collectorTestClient(server), patches)

	require.NoError(t, err)
	assert.Equal(t, []int{100, 100, 1}, batchSizes)
	assert.Equal(t, []string{"job-1", "job-2", "job-3"}, jobs)
}

func TestSubmitCollectorTimerRuntimeConfigsRetainsAcceptedChunkOnAmbiguousNextChunk(t *testing.T) {
	var submitCount, statusCount int
	server := newControlFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/admin/cloudnode/SubmitUpdateNodeRuntimeConfigs":
			submitCount++
			if submitCount == 2 {
				http.Error(w, "response lost after submission", http.StatusBadGateway)
				return
			}
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"job_id":"timer-job-1"}`))
		case "/api/admin/cloudnode/GetNodeBatchChange":
			statusCount++
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"job":{"job_id":"timer-job-1","status":"NODE_BATCH_STATUS_SUCCESS"}}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	patches := make([]collectorRuntimeConfigPatch, collectorRuntimeConfigBatchSize+1)
	for index := range patches {
		patches[index] = collectorRuntimeConfigPatch{NodeID: fmt.Sprintf("node-%03d", index)}
	}

	jobs, err := submitCollectorTimerRuntimeConfigs(context.Background(), collectorTestClient(server), patches)

	require.ErrorIs(t, err, errCollectorBatchOutcomeUnknown)
	assert.Equal(t, []string{"timer-job-1"}, jobs)
	assert.Equal(t, 2, submitCount)
	assert.Equal(t, 1, statusCount)
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

func writeMinimalSCFZip(t *testing.T, zipPath string, files map[string]string) {
	t.Helper()
	out, err := os.Create(zipPath)
	require.NoError(t, err)
	zw := zip.NewWriter(out)
	if len(files) == 0 {
		files = map[string]string{"main": "binary"}
	}
	for name, content := range files {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0o644)
		if name == "main" {
			header.SetMode(0o755)
			if content == "binary" {
				content = string(testfixture.LinuxExecutable(elf.EM_X86_64))
			}
		}
		writer, err := zw.CreateHeader(header)
		require.NoError(t, err)
		_, err = writer.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	require.NoError(t, out.Close())
}

func TestDeployCollectorFunctionWithExistingZip(t *testing.T) {
	setCollectorCLSTestCredentials(t)
	credentialFile := setCollectorFleetRuntimeTestEnvironment(t)
	_, ca, err := collectorEventBusCredentialMaterial(collectorPublishOptions{EventBusCredentialFile: credentialFile})
	require.NoError(t, err)
	zipPath := filepath.Join(t.TempDir(), "collector.zip")
	writeMinimalSCFZip(t, zipPath, map[string]string{"main": "binary", "certs/eventbus-ca.pem": string(ca), "sources/market/binance.yaml": "storage:\n  bindings:\n    spot:\n      auth_info: {app_id: moox-collector, app_key: ''}\n"})

	var requests int
	server := newControlFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		t.Errorf("canary-blocked deployment made unexpected control-plane request: %s", r.URL.Path)
		http.Error(w, "control-plane request must be blocked", http.StatusInternalServerError)
	}))
	defer server.Close()

	_, err = deployCollectorFunction(context.Background(), collectorDeployOptions{
		ControlURL:             server.URL,
		SpaceID:                "stockcn",
		CloudAccountID:         "account-a",
		NodeID:                 "node-1",
		ZipPath:                zipPath,
		EventBusCredentialFile: credentialFile,
	})
	require.ErrorContains(t, err, "canary verification contract is unavailable")
	require.Zero(t, requests, "deployment must stop before package upload or node mutation")
}

func TestDeployCollectorFunctionBudgetAndMissingMasterFailBeforeUpload(t *testing.T) {
	credentialFile := setCollectorFleetRuntimeTestEnvironment(t)
	_, ca, err := collectorEventBusCredentialMaterial(collectorPublishOptions{EventBusCredentialFile: credentialFile})
	require.NoError(t, err)
	zipPath := filepath.Join(t.TempDir(), "collector.zip")
	writeMinimalSCFZip(t, zipPath, map[string]string{"main": "binary", "certs/eventbus-ca.pem": string(ca), "sources/market/binance.yaml": "storage:\n  bindings:\n    spot:\n      auth_info: {app_id: moox-collector, app_key: ''}\n"})
	called := 0
	server := newControlFixtureServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called++ }))
	defer server.Close()
	opts := collectorDeployOptions{ControlURL: server.URL, CloudAccountID: "account", NodeID: "node", ZipPath: zipPath, EventBusCredentialFile: credentialFile}
	require.NoError(t, os.WriteFile(credentialFile, []byte("version: 1\nurls: [tls://203.0.113.10:4222]\nusername: publisher\npassword: "+strings.Repeat("private-value", 400)+"\nca_file: eventbus-ca.pem\n"), 0o600))
	_, err = deployCollectorFunction(context.Background(), opts)
	require.ErrorContains(t, err, "4096")
	require.NotContains(t, err.Error(), "private-value")
	require.Zero(t, called)
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "")
	t.Setenv("HOME", t.TempDir())
	_, err = deployCollectorFunction(context.Background(), opts)
	require.ErrorContains(t, err, "Storage Primary auth secret")
	require.Zero(t, called)
}

func TestCollectorPackageIDBudgetCheckedBeforeUpload(t *testing.T) {
	credentialFile := setCollectorFleetRuntimeTestEnvironment(t)
	t.Setenv("MOOX_SPACE_ID", "stockcn")
	t.Setenv("MOOX_COLLECTOR_RPC_GATEWAY_TARGET", "ip://collector.example:11003")
	t.Setenv("MOOX_COLLECTOR_GATEWAY_TARGET_NODE", "collector")
	_, ca, err := collectorEventBusCredentialMaterial(collectorPublishOptions{EventBusCredentialFile: credentialFile})
	require.NoError(t, err)
	zipPath := filepath.Join(t.TempDir(), "collector.zip")
	writeMinimalSCFZip(t, zipPath, map[string]string{"main": "binary", "certs/eventbus-ca.pem": string(ca), "sources/market/binance.yaml": "storage:\n  bindings:\n    spot:\n      auth_info: {app_id: moox-collector, app_key: ''}\n"})
	base := collectorPublishOptions{SpaceID: "stockcn", TriggerType: "timer", BizType: "market_fetcher", ZipPath: zipPath, EventBusCredentialFile: credentialFile}
	require.NoError(t, prepareCollectorPublicationTrust(&base))
	env, err := collectorFunctionEnvironment(base, "preflight-package-id")
	require.NoError(t, err)
	// Only the realistic package name/version/UUID pushes the final map over 4096.
	name := strings.Repeat("n", 4096-tencent.SCFEnvironmentBytes(env)+len("preflight-package-id"))
	called := 0
	server := newControlFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		http.Error(w, "unexpected control access", http.StatusInternalServerError)
	}))
	defer server.Close()
	_, err = deployCollectorFunction(context.Background(), collectorDeployOptions{
		collectorPackageOptions: collectorPackageOptions{Version: "release"}, PackageName: name,
		SpaceID: "stockcn", ControlURL: server.URL, CloudAccountID: "account", NodeID: "node", ZipPath: zipPath, EventBusCredentialFile: credentialFile,
	})
	require.ErrorContains(t, err, "4096")
	require.Zero(t, called)
	_, err = publishCollectorFunction(context.Background(), collectorPublishOptions{
		collectorPackageOptions: collectorPackageOptions{Version: "release"}, PackageName: name,
		SpaceID: "stockcn", ControlURL: server.URL, AccessToken: "token", CloudAccountID: "account", Region: "ap-singapore", NodeCount: 1, ZipPath: zipPath, EventBusCredentialFile: credentialFile,
	})
	require.ErrorContains(t, err, "4096")
	require.Zero(t, called)
}

func TestCollectorPreflightPackageIDMatchesCloudNodeSanitization(t *testing.T) {
	assert.Equal(t, "moox-collector_dev_00000000-0000-0000-0000-000000000000", collectorPreflightPackageID(collectorPublishOptions{}))
	assert.Equal(t, "a_b___c_v_1_00000000-0000-0000-0000-000000000000", collectorPreflightPackageID(collectorPublishOptions{
		PackageName: " ..a/b 中 c-- ", collectorPackageOptions: collectorPackageOptions{Version: " .v/1. "},
	}))
}

func TestCollectorRegionalPreflightChecksInvokeAndOverflowShards(t *testing.T) {
	credentialFile := setCollectorFleetRuntimeTestEnvironment(t)
	fetcher := defaultCollectorSCFFetcherSpace()
	fetcher.SpaceID = "stockcn"
	fetcher.InvokeTimeoutSeconds = 900
	opts := collectorPublishOptions{SpaceID: "stockcn", TriggerType: "timer", BizType: "market_fetcher", FetcherConfig: fetcher, EventBusCredentialFile: credentialFile, StorageAppKeysJSON: collectorTestStorageAppKeysJSON}
	base, err := buildCollectorCreateNodeItem(opts, collectorPreflightPackageID(opts))
	require.NoError(t, err)
	opts.Env = []string{"PADDING=" + strings.Repeat("x", 4096-tencent.SCFEnvironmentBytes(base.Environment)-len("PADDING")-2)}
	shards := []setupconfig.SCFNamespaceShard{{Namespace: "moox-stockcn", Timers: 1}, {Namespace: "moox-stockcn-ns2", Invokes: 1}}
	err = preflightCollectorRegionalEnvironment(opts, shards)
	require.ErrorContains(t, err, "moox-stockcn-ns2 Invoke")
	require.ErrorContains(t, err, "4096")
	require.NotContains(t, err.Error(), "xxx")
}

func TestDeployCollectorFunctionRejectsPlaceholderAuth(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "collector.zip")
	writeMinimalSCFZip(t, zipPath, map[string]string{
		"main":                        "binary",
		"sources/market/binance.yaml": "storage:\n  bindings:\n    spot:\n      auth_info:\n        app_id: \"moox-collector\"\n        app_key: \"binance-spot-collector\"\n",
	})
	_, err := deployCollectorFunction(context.Background(), collectorDeployOptions{
		ControlURL:     "http://127.0.0.1:1",
		CloudAccountID: "account-a",
		NodeID:         "node-1",
		ZipPath:        zipPath,
	})
	require.ErrorContains(t, err, "nonempty credential field app_key")
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

func selectCollectorFleetNodes(nodes []adminclient.CloudNode, prefix string, bizType string, expected int) ([]adminclient.CloudNode, error) {
	return selectCollectorFleetNodesForTrigger(nodes, prefix, bizType, expected, "", 0)
}
