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
	manifest := setupconfig.Manifest{Deployments: map[string][]string{"storage1": {"storage-primary", "storage-node"}, "view1": {"storage-view"}, "edge1": {"access", "egress-proxy"}, "trade1": {"trade"}}}
	for _, test := range []struct {
		host, profile     string
		components, roles []string
	}{
		{"storage1", "storage", []string{"storage-primary", "storage-node"}, []string{"metrics-publisher", "storage-eventbus"}},
		{"view1", "storage", []string{"storage-view"}, []string{"metrics-publisher", "storage-eventbus"}},
		{"edge1", "access", []string{"access"}, []string{"metrics-publisher"}},
		{"edge1", "egress-proxy", []string{"egress-proxy"}, []string{"metrics-publisher"}},
		{"trade1", "trade", []string{"trade"}, []string{"metrics-publisher", "trade-eventbus"}},
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
