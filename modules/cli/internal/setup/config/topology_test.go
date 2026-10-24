package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManifestTopologyUsesCanonicalHostsAndAutomaticComponents(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
[hosts.compute]
address = "192.0.2.11"
ssh = { username = "ubuntu" }
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	m := &snapshot.Manifest
	topology, err := m.Topology()
	require.NoError(t, err)
	assert.Equal(t, "control", topology.ControlHostID)
	require.Len(t, topology.Hosts, 2)
	assert.Equal(t, "control", topology.Hosts[0].ID)
	assert.Equal(t, "compute", topology.Hosts[1].ID)
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	require.NoError(t, catalog.ValidateTopology(topology))
	for _, id := range []string{"control", "compute"} {
		for _, component := range []string{"host-gateway", "host-agent"} {
			assert.Contains(t, topology.Deployments, servicecatalog.Deployment{HostID: id, ComponentID: component, Status: servicecatalog.Enabled})
		}
	}
	// A role follows the deployment, and SSH data follows the host definition.
	m.Deployments["compute"] = []string{"storage-primary", "storage-view"}
	assert.Equal(t, "compute", m.StorageHost().Name)
	assert.Equal(t, m.StorageHost(), m.ViewHost())
	host := m.HostCatalog["compute"]
	host.Address = "192.0.2.12"
	m.HostCatalog["compute"] = host
	assert.Equal(t, "192.0.2.12", m.StorageHost().Address)
	delete(m.Deployments, "compute")
	assert.False(t, m.HasStorageHost())
	assert.Empty(t, m.HostByID("missing"))
	// Projections are values; they cannot silently mutate the source of truth.
	projection := m.ControlHost()
	projection.Address = "192.0.2.99"
	assert.Equal(t, "192.0.2.10", m.ControlHost().Address)
}

func TestFactorHostIsAnOrdinaryOutboundHostWithExternalPrograms(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
[hosts.factor]
address = "192.168.0.103"
reachability = "outbound"
ssh = { username = "builder" }
`
	body = strings.Replace(body, "[deployments]\n", "[deployments]\nfactor = [\"factor-engine\", \"archive-worker\"]\n", 1)
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	m := snapshot.Manifest
	var names []string
	for _, host := range m.Hosts() {
		names = append(names, host.Name)
	}
	assert.Contains(t, names, "factor", "因子机是部署主机")
	factor := m.HostByID("factor")
	assert.Equal(t, "因子机", factor.Label, "中文名按主机 ID 生成")
	assert.Equal(t, servicecatalog.ReachabilityOutbound, factor.Reachability)
	assert.Equal(t, []string{"factor-engine", "archive-worker"}, m.Deployments["factor"])
}

func TestFactorEngineSectionIsRejectedWithMigrationHint(t *testing.T) {
	for _, value := range []string{"local", "factor"} {
		root := t.TempDir()
		_, err := Load(writeManifest(t, root, validManifest+"[factor_engine]\nhost = \""+value+"\"\n", 0o600), root)
		require.ErrorContains(t, err, "[factor_engine] 已取消")
		require.ErrorContains(t, err, "config migrate")
	}
}

func TestDefaultHostLabel(t *testing.T) {
	for id, want := range map[string]string{
		"control": "控制机", "storage": "存储机", "overseas": "海外机", "factor": "因子机",
		"overseas-2": "海外机 2", "storage-10": "存储机 10", "compute-1": "", "ops": "", "overseas2": "",
	} {
		assert.Equal(t, want, DefaultHostLabel(id), id)
	}
}

func TestConsoleSSHSyncRules(t *testing.T) {
	key := filepath.Join(t.TempDir(), "id_ed25519")
	require.NoError(t, os.WriteFile(key, []byte("PRIVATE KEY BODY\n"), 0o600))
	direct := Host{Name: "storage", Port: 22, Username: "ubuntu"}

	none, err := direct.ConsoleSSH()
	require.NoError(t, err)
	assert.Nil(t, none, "没有配置密码或私钥时控制台置灰")

	direct.Password = "p4ss"
	got, err := direct.ConsoleSSH()
	require.NoError(t, err)
	assert.Equal(t, &ConsoleSSH{Port: 22, User: "ubuntu", AuthType: ConsoleSSHPassword, Secret: "p4ss"}, got)

	direct.PrivateKeyFile, direct.PrivateKeyPassphrase = key, "phrase"
	got, err = direct.ConsoleSSH()
	require.NoError(t, err)
	assert.Equal(t, ConsoleSSHPrivateKey, got.AuthType, "私钥优先于密码")
	assert.Equal(t, "PRIVATE KEY BODY\n", got.Secret)
	assert.Equal(t, "phrase", got.Passphrase)

	direct.PrivateKeyFile = filepath.Join(t.TempDir(), "missing")
	_, err = direct.ConsoleSSH()
	require.ErrorContains(t, err, "private_key_file")

	outbound := Host{Name: "factor", Port: 22, Username: "builder", Password: "p4ss", Reachability: servicecatalog.ReachabilityOutbound}
	got, err = outbound.ConsoleSSH()
	require.NoError(t, err)
	assert.Nil(t, got, "只能出站的主机不同步 SSH")
}

func TestLoadHostNormalizationAndSecretRedaction(t *testing.T) {
	root := t.TempDir()
	body := strings.Replace(validManifest, `address = "192.0.2.10"`, `address = " 192.0.2.10 "
private_address = " 10.0.0.10 "
region = " AP-GUANGZHOU "
provider = " Tencent "
tls_mode = " INTERNAL "`, 1)
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	host := snapshot.Manifest.ControlHost()
	assert.Equal(t, "192.0.2.10", host.Address)
	assert.Equal(t, "10.0.0.10", host.PrivateAddress)
	assert.Equal(t, "ap-guangzhou", host.Region)
	assert.Equal(t, "tencent", host.Provider)
	assert.Equal(t, "internal", host.TLSMode)
	for _, value := range []any{host, snapshot.Manifest.HostCatalog} {
		raw, err := json.Marshal(value)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), "control-password")
	}
}

func TestLoadRejectsAddressAliasesAndDNSResolver(t *testing.T) {
	for name, body := range map[string]string{
		"DNS alias": strings.Replace(validManifest, `address = "192.0.2.10"`, `address = "CONTROL.example.test"`, 1) + `
[hosts.compute]
address = "control.example.test"
ssh = { username = "ubuntu" }
`,
		"IP alias": strings.Replace(validManifest, `address = "192.0.2.10"`, `address = "2001:db8::10"`, 1) + `
[hosts.compute]
address = "2001:0db8:0:0:0:0:0:0010"
ssh = { username = "ubuntu" }
`,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			_, err := Load(writeManifest(t, root, body, 0o600), root)
			require.ErrorContains(t, err, "duplicates hosts.")
		})
	}
	root := t.TempDir()
	_, err := Load(writeManifest(t, root, validManifest+"[dns_resolver]\ntrade_node = 'compute'\n", 0o600), root)
	require.ErrorContains(t, err, "[egress_proxy.dns]")
}
