package unitdeploy

import (
	"strings"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitinstall"
	"github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/require"
)

func TestBusinessUnitSelectionAndExportRolesFollowActualDeployments(t *testing.T) {
	manifest := setupconfig.Manifest{Deployments: map[string][]string{"storage1": {"storage-primary", "storage-node"}, "view1": {"storage-view"}, "edge1": {"access", "egress-proxy"}, "trade1": {"trade"}, "notify1": {"callback-dispatcher"}}}
	for _, test := range []struct {
		host, profile     string
		components, roles []string
	}{
		{"storage1", "storage", []string{"storage-primary", "storage-node"}, []string{"metrics-publisher", "storage-eventbus"}},
		{"view1", "storage", []string{"storage-view"}, []string{"metrics-publisher", "storage-eventbus"}},
		{"edge1", "access", []string{"access"}, []string{"metrics-publisher"}},
		{"edge1", "egress-proxy", []string{"egress-proxy"}, []string{"metrics-publisher"}},
		{"trade1", "trade", []string{"trade"}, []string{"metrics-publisher", "trade-eventbus"}},
		{"notify1", "callback-dispatcher", []string{"callback-dispatcher"}, []string{"callback-dispatcher-eventbus", "metrics-publisher"}},
	} {
		components, err := unitComponents(manifest, test.host, test.profile)
		require.NoError(t, err)
		require.Equal(t, test.components, components)
		require.Equal(t, test.roles, unitinstall.EventBusRoles(components))
	}
	_, err := unitComponents(manifest, "edge1", "trade")
	require.Error(t, err)
	_, err = unitComponents(manifest, "unknown", "storage")
	require.Error(t, err)
}

func TestStorageEnvironmentSharesOnlyRequiredPersistentCredentials(t *testing.T) {
	manifest := setupconfig.Manifest{HostCatalog: map[string]setupconfig.HostDefinition{"control": {Address: "192.0.2.1"}, "primary": {Address: "192.0.2.2"}, "view": {Address: "192.0.2.3"}}, Deployments: map[string][]string{"control": {"admin"}, "primary": {"storage-primary", "storage-node", "storage-view"}}}
	identity := runtimeIdentity{StorageNodeSecret: strings.Repeat("n", 32), StoragePrimarySecret: strings.Repeat("p", 32), StorageViewSecret: strings.Repeat("v", 32)}
	components := []string{"storage-primary", "storage-node", "storage-view", "access"}
	environment, err := unitEnvironment(manifest, identity, "primary", components)
	require.NoError(t, err)
	require.Equal(t, identity.StorageNodeSecret, environment["storage-primary"]["MOOX_STORAGE_NODE_AUTH_SECRET"])
	require.Equal(t, identity.StorageNodeSecret, environment["storage-node"]["MOOX_STORAGE_NODE_AUTH_SECRET"])
	require.NotContains(t, environment["storage-view"], "MOOX_STORAGE_NODE_AUTH_SECRET")
	require.NotContains(t, environment["storage-node"], "MOOX_STORAGE_PRIMARY_AUTH_SECRET")
	require.NotContains(t, environment["access"], "MOOX_STORAGE_VIEW_AUTH_SECRET")
	require.Equal(t, "ip://127.0.0.1:20102", environment["storage-view"]["MOOX_STORAGE_PRIMARY_TARGET"])
	require.Equal(t, "ip://127.0.0.1:20100", environment["storage-view"]["MOOX_STORAGE_METADATA_TARGET"])
	require.Equal(t, "storage-node-0", environment["storage-node"]["MOOX_STORAGE_NODE_ID"])
	require.Equal(t, "./var/storage", environment["storage-primary"]["MOOX_STORAGE_HOME"])
	require.Equal(t, "primary", environment["storage-primary"]["MOOX_STORAGE_ROLE"])
	require.Equal(t, "view", environment["storage-view"]["MOOX_STORAGE_ROLE"])
	_, err = unitEnvironment(manifest, runtimeIdentity{}, "primary", components)
	require.Error(t, err, "missing durable secrets must not be regenerated")
	manifest.Deployments["primary"] = []string{"storage-primary", "storage-node"}
	manifest.Deployments["view"] = []string{"storage-view"}
	_, err = unitEnvironment(manifest, identity, "view", []string{"storage-view"})
	require.Error(t, err, "loopback RPC templates cannot reach a different host")
}

// collector-stream 只拿到按 Storage 主密钥派生的 moox-collector 应用密钥，不拿主密钥。
func TestCollectorStreamGetsOnlyTheDerivedCollectorAppKey(t *testing.T) {
	manifest := setupconfig.Manifest{Deployments: map[string][]string{"overseas": {"access", "collector-stream"}}}
	components, err := unitComponents(manifest, "overseas", "collector-stream")
	require.NoError(t, err)
	require.Equal(t, []string{"collector-stream"}, components)
	identity := runtimeIdentity{StoragePrimarySecret: strings.Repeat("p", 32), StorageViewSecret: strings.Repeat("v", 32), HealthAccessKey: "a", HealthSecret: "s"}
	environment, err := unitEnvironment(manifest, identity, "overseas", components)
	require.NoError(t, err)
	values := environment["collector-stream"]
	require.NotContains(t, values, "MOOX_STORAGE_PRIMARY_AUTH_SECRET")
	require.NotContains(t, values, "MOOX_STORAGE_VIEW_AUTH_SECRET")
	require.Equal(t, `{"moox-collector":"`+security.HMACSHA256Hex(identity.StoragePrimarySecret, []byte("moox-collector"))+`"}`, values["MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON"])
	require.Equal(t, "crypto", values["MOOX_SPACE_ID"])
	_, err = unitEnvironment(manifest, runtimeIdentity{}, "overseas", components)
	require.Error(t, err)
}

// 启用了服务日志上报 CLS 时，接入了日志上报的组件带上 MOOX_LOG_CLS_*，没接入的（access 等）不拿密钥；没有启用时一个都不带。
// 变量名不能是 MOOX_CLS_*：那一组是云函数采集日志用的，指向另一个主题和另一把密钥。
func TestUnitEnvironmentCarriesServiceLogCLS(t *testing.T) {
	manifest := setupconfig.Manifest{HostCatalog: map[string]setupconfig.HostDefinition{"overseas": {Address: "192.0.2.9"}}}
	components := []string{"trade", "access"}
	disabled, err := unitEnvironment(manifest, runtimeIdentity{}, "overseas", components)
	require.NoError(t, err)
	for key := range disabled["trade"] {
		require.NotContains(t, key, "CLS")
	}
	manifest.Logging.CLS = setupconfig.LoggingCLS{Enabled: true, Endpoint: "ap-guangzhou.cls.tencentcs.com", TopicID: "topic-1", WriterSecretID: "AKIDwriter", WriterSecretKey: "writer-key", MinLevel: "warn"}
	enabled, err := unitEnvironment(manifest, runtimeIdentity{}, "overseas", components)
	require.NoError(t, err)
	for key := range enabled["access"] {
		require.NotContains(t, key, "CLS")
	}
	for _, component := range []string{"trade"} {
		values := enabled[component]
		require.Equal(t, "true", values["MOOX_LOG_CLS_ENABLED"])
		require.Equal(t, "ap-guangzhou.cls.tencentcs.com", values["MOOX_LOG_CLS_ENDPOINT"])
		require.Equal(t, "topic-1", values["MOOX_LOG_CLS_TOPIC_ID"])
		require.Equal(t, "AKIDwriter", values["MOOX_LOG_CLS_SECRET_ID"])
		require.Equal(t, "writer-key", values["MOOX_LOG_CLS_SECRET_KEY"])
		require.Equal(t, "warn", values["MOOX_LOG_CLS_MIN_LEVEL"])
		require.Equal(t, "overseas", values["MOOX_LOG_CLS_SOURCE"])
		for key := range values {
			require.False(t, strings.HasPrefix(key, "MOOX_CLS_"), key)
		}
	}
}

// CloudFunction 开通采集日志主题时的保留天数来自 moox.toml；别的组件不带这个变量。
func TestUnitEnvironmentCarriesLogRetentionForCloudFunction(t *testing.T) {
	manifest := setupconfig.Manifest{HostCatalog: map[string]setupconfig.HostDefinition{"control": {Address: "192.0.2.1"}}}
	manifest.Logging.RetentionDays = 7
	environment, err := unitEnvironment(manifest, runtimeIdentity{}, "control", []string{"cloudfunction", "web-host"})
	require.NoError(t, err)
	require.Equal(t, "7", environment["cloudfunction"]["MOOX_CLS_RETENTION_DAYS"])
	require.NotContains(t, environment["web-host"], "MOOX_CLS_RETENTION_DAYS")
}
