package config

import (
	"encoding/json"
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
[hosts.compile]
address = "192.0.2.30"
ssh = { username = "builder" }
[compile_host]
host = "compile"
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
			assert.Contains(t, topology.Placements, servicecatalog.Placement{HostID: id, ComponentID: component, Status: servicecatalog.Enabled})
		}
	}
	// A role follows the placement, and SSH data follows the host definition.
	m.Placements["compute"] = []string{"storage-primary", "storage-view"}
	assert.Equal(t, "compute", m.StorageHost().Name)
	assert.Equal(t, m.StorageHost(), m.ViewHost())
	host := m.HostCatalog["compute"]
	host.Address = "192.0.2.12"
	m.HostCatalog["compute"] = host
	assert.Equal(t, "192.0.2.12", m.StorageHost().Address)
	delete(m.Placements, "compute")
	assert.False(t, m.HasStorageHost())
	assert.Empty(t, m.HostByID("missing"))
	// Projections are values; they cannot silently mutate the source of truth.
	projection := m.ControlHost()
	projection.Address = "192.0.2.99"
	assert.Equal(t, "192.0.2.10", m.ControlHost().Address)
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
