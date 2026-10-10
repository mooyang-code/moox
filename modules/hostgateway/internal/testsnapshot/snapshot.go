// Package testsnapshot builds catalog-derived, synthetic test snapshots.
package testsnapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	directorypb "github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

func New(t testing.TB, hostID string, components ...string) *pb.HostGatewaySnapshot {
	t.Helper()
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	topology := servicecatalog.Topology{ControlHostID: "control", Hosts: []servicecatalog.Host{{ID: "control", Address: "192.0.2.1", Status: servicecatalog.Enabled}}}
	if hostID != "control" {
		topology.Hosts = append(topology.Hosts, servicecatalog.Host{ID: hostID, Address: "192.0.2.2", Status: servicecatalog.Enabled})
	}
	seen := map[string]bool{}
	add := func(host, component string) {
		key := host + "/" + component
		if !seen[key] {
			topology.Placements = append(topology.Placements, servicecatalog.Placement{HostID: host, ComponentID: component, Status: servicecatalog.Enabled})
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
		raw.Directory.Hosts[id] = &directorypb.DirectoryHost{Address: host.Address, PrivateAddress: host.PrivateAddress, Region: host.Region}
	}
	for path, ids := range compiled.Directory.Services {
		raw.Directory.Services[path] = &directorypb.ServiceHosts{HostIds: ids}
	}
	for _, route := range compiled.Routes {
		raw.Routes = append(raw.Routes, &pb.HostGatewayRoute{ComponentId: route.ComponentID, ServicePath: route.ServicePath, Method: route.Method, Address: route.Address,
			TimeoutMs: route.TimeoutMS, MaxBodyBytes: route.MaxBodyBytes, Callers: route.Callers, ReadOnly: route.ReadOnly})
	}
	for _, caller := range compiled.VerificationCallers {
		credential := Credential(caller)
		raw.VerificationKeys = append(raw.VerificationKeys, &pb.GatewayVerificationKey{Caller: caller, KeyId: credential.KeyID, Secret: []byte(credential.Secret)})
	}
	Rehash(t, raw)
	return raw
}

func Credential(caller string) gatewayauth.Credentials {
	sum := sha256.Sum256([]byte("synthetic-only/" + caller))
	return gatewayauth.Credentials{Caller: caller, KeyID: "test-" + caller, Secret: hex.EncodeToString(sum[:])}
}

func Rehash(t testing.TB, raw *pb.HostGatewaySnapshot) {
	t.Helper()
	hash, err := pb.SnapshotHash(raw)
	if err != nil {
		t.Fatal(err)
	}
	raw.Hash = hash
}
