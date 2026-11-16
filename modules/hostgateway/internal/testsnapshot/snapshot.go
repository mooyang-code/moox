// Package testsnapshot builds catalog-derived, synthetic test snapshots.
package testsnapshot

import (
	"crypto/ed25519"
	"crypto/sha256"
	"slices"
	"testing"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	directorypb "github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen"
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

func build(t testing.TB, hostID, outbound string, components []string) *pb.HostGatewaySnapshot {
	t.Helper()
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	topology := servicecatalog.Topology{ControlHostID: "control", Hosts: []servicecatalog.Host{{ID: "control", Address: "192.0.2.1", Status: servicecatalog.Enabled}}}
	if hostID != "control" && hostID != outbound {
		topology.Hosts = append(topology.Hosts, servicecatalog.Host{ID: hostID, Address: "192.0.2.2", Status: servicecatalog.Enabled})
	}
	if outbound != "" {
		topology.Hosts = append(topology.Hosts, servicecatalog.Host{ID: outbound, Address: "192.168.0.103", Status: servicecatalog.Enabled, Reachability: servicecatalog.ReachabilityOutbound})
	}
	seen := map[string]bool{}
	add := func(host, component string) {
		key := host + "/" + component
		if !seen[key] {
			topology.Deployments = append(topology.Deployments, servicecatalog.Deployment{HostID: host, ComponentID: component, Status: servicecatalog.Enabled})
			seen[key] = true
		}
	}
	for _, c := range catalog.Components {
		if c.Scope == servicecatalog.ScopeHost {
			for _, h := range topology.Hosts {
				add(h.ID, c.ID)
			}
		}
		if c.Scope == servicecatalog.ScopeControl && c.Protected {
			add("control", c.ID)
		}
	}
	for _, c := range components {
		add(hostID, c)
	}
	compiled, err := catalog.Compile(topology, hostID)
	if err != nil {
		t.Fatal(err)
	}
	raw := &pb.HostGatewaySnapshot{SchemaVersion: 1, HostId: hostID, Disabled: compiled.Disabled,
		Directory: &directorypb.ServiceDirectory{Version: compiled.Directory.Version, Hosts: map[string]*directorypb.DirectoryHost{}, Services: map[string]*directorypb.ServiceHosts{}}}
	for id, host := range compiled.Directory.Hosts {
		raw.Directory.Hosts[id] = &directorypb.DirectoryHost{Address: host.Address, PrivateAddress: host.PrivateAddress, Region: host.Region, Relay: host.Relay}
	}
	for path, ids := range compiled.Directory.Services {
		raw.Directory.Services[path] = &directorypb.ServiceHosts{HostIds: ids}
	}
	for _, route := range compiled.Routes {
		raw.Routes = append(raw.Routes, &pb.HostGatewayRoute{ComponentId: route.ComponentID, ServicePath: route.ServicePath, Method: route.Method, Address: route.Address,
			TimeoutMs: route.TimeoutMS, MaxBodyBytes: route.MaxBodyBytes, Callers: route.Callers, ReadOnly: route.ReadOnly})
	}
	for _, relay := range compiled.Relays {
		item := &pb.HostGatewayRelay{HostId: relay.HostID}
		for _, port := range relay.Ports {
			item.Ports = append(item.Ports, &pb.HostGatewayRelayPort{Port: int32(port.Port), Callers: port.Callers})
		}
		raw.Relays = append(raw.Relays, item)
	}
	// 每台主机一把校验公钥，连同部署在它上面的调用方；非主机身份只有 moox-cli。
	signing := catalog.SigningCallers(topology)
	// 测试拓扑只部署了被测主机要的组件。为了让别的调用方也能签名，夹具把其余组件（与 console）都算作部署在 control 上；
	// “调用方必须在签名主机上”另有专门的测试。
	for _, component := range catalog.Components {
		if !component.ThirdParty {
			signing["control"] = append(signing["control"], component.ID)
		}
	}
	signing["control"] = append(signing["control"], servicecatalog.OperatorConsole)
	slices.Sort(signing["control"])
	signing["control"] = slices.Compact(signing["control"])
	for _, host := range topology.Hosts {
		raw.VerificationKeys = append(raw.VerificationKeys, &pb.GatewayVerificationKey{HostId: host.ID, PublicKey: HostKey(host.ID).Public().(ed25519.PublicKey), Callers: signing[host.ID]})
	}
	raw.VerificationKeys = append(raw.VerificationKeys, &pb.GatewayVerificationKey{Identity: "moox-cli", PublicKey: Credential("moox-cli", "").PublicKey()})
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
