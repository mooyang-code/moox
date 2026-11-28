package adminpb

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

func TestSnapshotHashCoversHostsDeploymentsAndKeysWithoutMutatingInput(t *testing.T) {
	message := &HostGatewaySnapshot{SchemaVersion: 2, Hash: "previous",
		Hosts: []*SnapshotHost{
			{HostId: "control", Address: "192.0.2.1", Reachability: "direct", Enabled: true, SignPublicKey: []byte("fixture-public-key-of-32-bytes...")},
			{HostId: "storage", Address: "192.0.2.2", PrivateAddress: "10.0.0.2", Reachability: "direct", Enabled: true},
		},
		Deployments: []*SnapshotDeployment{{HostId: "control", ComponentId: "admin", Enabled: true}, {HostId: "storage", ComponentId: "storage-primary", Enabled: true}},
		Identities:  []*SnapshotIdentity{{Identity: "moox-cli", SignPublicKey: []byte("another-public-key-of-32-bytes..")}},
	}
	before := proto.Clone(message)
	first, err := SnapshotHash(message)
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(before, message) {
		t.Fatal("hash mutated input")
	}
	if len(first) != 64 {
		t.Fatalf("hash = %q", first)
	}
	message.Hash = "another"
	second, err := SnapshotHash(message)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("hash field must not change identity")
	}
	for name, mutate := range map[string]func(*HostGatewaySnapshot){
		// 换公钥、主机地址或可达性变了、主机或部署启停、增删部署或身份，都要换哈希：网关与客户端据此重新取。
		"host key":         func(m *HostGatewaySnapshot) { m.Hosts[0].SignPublicKey[0] ^= 1 },
		"host key removed": func(m *HostGatewaySnapshot) { m.Hosts[0].SignPublicKey = nil },
		"host id":          func(m *HostGatewaySnapshot) { m.Hosts[1].HostId = "overseas" },
		"address":          func(m *HostGatewaySnapshot) { m.Hosts[1].Address = "192.0.2.3" },
		"private address":  func(m *HostGatewaySnapshot) { m.Hosts[1].PrivateAddress = "10.0.0.3" },
		"reachability":     func(m *HostGatewaySnapshot) { m.Hosts[1].Reachability = "outbound" },
		"host disabled":    func(m *HostGatewaySnapshot) { m.Hosts[1].Enabled = false },
		"external entry":   func(m *HostGatewaySnapshot) { m.Hosts[1].ExternalEntry = true },
		"host added": func(m *HostGatewaySnapshot) {
			m.Hosts = append(m.Hosts, &SnapshotHost{HostId: "overseas", Address: "192.0.2.4"})
		},
		"deployment stopped": func(m *HostGatewaySnapshot) { m.Deployments[1].Enabled = false },
		"deployment moved":   func(m *HostGatewaySnapshot) { m.Deployments[1].HostId = "control" },
		"deployment changed": func(m *HostGatewaySnapshot) { m.Deployments[1].ComponentId = "storage-view" },
		"deployment removed": func(m *HostGatewaySnapshot) { m.Deployments = m.Deployments[:1] },
		"identity key":       func(m *HostGatewaySnapshot) { m.Identities[0].SignPublicKey[0] ^= 1 },
		"identity name":      func(m *HostGatewaySnapshot) { m.Identities[0].Identity = "scf-collector" },
		"identity added": func(m *HostGatewaySnapshot) {
			m.Identities = append(m.Identities, &SnapshotIdentity{Identity: "scf-collector", SignPublicKey: []byte("another")})
		},
		"schema version": func(m *HostGatewaySnapshot) { m.SchemaVersion = 3 },
	} {
		changed := proto.Clone(message).(*HostGatewaySnapshot)
		mutate(changed)
		hash, err := SnapshotHash(changed)
		if err != nil {
			t.Fatal(err)
		}
		if hash == first {
			t.Fatalf("%s: snapshot content change did not change hash", name)
		}
	}
	if _, err := SnapshotHash(nil); err == nil {
		t.Fatal("nil snapshot was accepted")
	}
}
