// Package testsnapshot 给测试拼一份快照：主机、部署与测试用的公钥，路由由被测代码按内嵌目录自己算。
package testsnapshot

import (
	"crypto/ed25519"
	"crypto/sha256"
	"slices"
	"testing"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

func New(t testing.TB, hostID string, components ...string) *pb.HostGatewaySnapshot {
	t.Helper()
	return build(t, hostID, "", components)
}

// NewWithOutbound 在拓扑里加入一台只能出站、由 control 中转的主机（部署全部主机级组件），返回 hostID 的快照。
func NewWithOutbound(t testing.TB, hostID, outbound string, components ...string) *pb.HostGatewaySnapshot {
	t.Helper()
	return build(t, hostID, outbound, components)
}

// build 的拓扑：control 加被测主机（加可选的出站主机）。components 部署在被测主机上；其余组件都放在 control 上，
// 这样测试里“控制机上的某个组件”都能用控制机的主机密钥签名。每台主机都有公钥，非主机身份只有 moox-cli。
func build(t testing.TB, hostID, outbound string, components []string) *pb.HostGatewaySnapshot {
	t.Helper()
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	raw := &pb.HostGatewaySnapshot{SchemaVersion: 2}
	addHost := func(id, address, reachability string) {
		raw.Hosts = append(raw.Hosts, &pb.SnapshotHost{HostId: id, Address: address, Reachability: reachability, Enabled: true, SignPublicKey: HostKey(id).Public().(ed25519.PublicKey)})
	}
	addHost("control", "192.0.2.1", servicecatalog.ReachabilityDirect)
	if hostID != "control" && hostID != outbound {
		addHost(hostID, "192.0.2.2", servicecatalog.ReachabilityDirect)
	}
	if outbound != "" {
		addHost(outbound, "192.168.0.103", servicecatalog.ReachabilityOutbound)
	}
	for _, c := range components {
		raw.Deployments = append(raw.Deployments, &pb.SnapshotDeployment{HostId: hostID, ComponentId: c, Enabled: true})
	}
	for _, c := range catalog.Components {
		switch {
		case c.Scope == servicecatalog.ScopeHost:
			for _, h := range raw.Hosts {
				raw.Deployments = append(raw.Deployments, &pb.SnapshotDeployment{HostId: h.HostId, ComponentId: c.ID, Enabled: true})
			}
		case !slices.Contains(components, c.ID):
			raw.Deployments = append(raw.Deployments, &pb.SnapshotDeployment{HostId: "control", ComponentId: c.ID, Enabled: true})
		}
	}
	raw.Identities = []*pb.SnapshotIdentity{{Identity: "moox-cli", SignPublicKey: Credential("moox-cli", "").PublicKey()}}
	Rehash(t, raw)
	return raw
}

// HostKey 是测试里一台主机的签名私钥（由主机 ID 派生，只用于测试）。
func HostKey(hostID string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("synthetic-only/host/" + hostID))
	return ed25519.NewKeyFromSeed(seed[:])
}

// Credential 返回测试用的签名身份：host 非空时是那台主机上的调用方，用主机密钥；为空时是不属于主机的身份。
func Credential(caller, host string) gatewayauth.Credentials {
	if host != "" {
		return gatewayauth.Credentials{Caller: caller, Host: host, Key: HostKey(host)}
	}
	seed := sha256.Sum256([]byte("synthetic-only/identity/" + caller))
	return gatewayauth.Credentials{Caller: caller, Key: ed25519.NewKeyFromSeed(seed[:])}
}

func Rehash(t testing.TB, raw *pb.HostGatewaySnapshot) {
	t.Helper()
	hash, err := pb.SnapshotHash(raw)
	if err != nil {
		t.Fatal(err)
	}
	raw.Hash = hash
}

// Host 返回快照里的一台主机，测试改它之后要 Rehash。
func Host(t testing.TB, raw *pb.HostGatewaySnapshot, hostID string) *pb.SnapshotHost {
	t.Helper()
	for _, host := range raw.Hosts {
		if host.HostId == hostID {
			return host
		}
	}
	t.Fatalf("快照里没有主机 %s", hostID)
	return nil
}

// Deployment 返回快照里的一条部署，测试改它之后要 Rehash。
func Deployment(t testing.TB, raw *pb.HostGatewaySnapshot, hostID, componentID string) *pb.SnapshotDeployment {
	t.Helper()
	for _, deployment := range raw.Deployments {
		if deployment.HostId == hostID && deployment.ComponentId == componentID {
			return deployment
		}
	}
	t.Fatalf("快照里没有部署 %s/%s", hostID, componentID)
	return nil
}

// AddHost 往快照里加一台可直达的主机（带主机级组件与派生的公钥），entry 表示它登记成外部入口。加完要 Rehash。
func AddHost(t testing.TB, raw *pb.HostGatewaySnapshot, hostID, address string, entry bool) {
	t.Helper()
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	raw.Hosts = append(raw.Hosts, &pb.SnapshotHost{HostId: hostID, Address: address, Reachability: servicecatalog.ReachabilityDirect, Enabled: true, ExternalEntry: entry, SignPublicKey: HostKey(hostID).Public().(ed25519.PublicKey)})
	for _, c := range catalog.Components {
		if c.Scope == servicecatalog.ScopeHost {
			raw.Deployments = append(raw.Deployments, &pb.SnapshotDeployment{HostId: hostID, ComponentId: c.ID, Enabled: true})
		}
	}
}

// AddIdentity 登记一个不属于主机的身份（公钥由 Credential 派生）。加完要 Rehash。
func AddIdentity(raw *pb.HostGatewaySnapshot, identity string) {
	raw.Identities = append(raw.Identities, &pb.SnapshotIdentity{Identity: identity, SignPublicKey: Credential(identity, "").PublicKey()})
}
