package servicecatalog

import (
	"bytes"
	"net"
	"slices"
	"strings"
	"testing"
)

// This is the intended three-host placement, not a claim that production has
// migrated. The old service IDs and duplicate compute gateway are not inputs.
func productionTopology() Topology {
	t := Topology{ControlHostID: "control", Hosts: []Host{
		{ID: "control", Address: "106.53.107.122", Status: Enabled},
		{ID: "storage", Address: "146.56.196.204", PrivateAddress: "10.206.0.5", Region: "ap-nanjing", Status: Enabled},
		{ID: "compute-1", Address: "43.132.204.177", PrivateAddress: "172.19.32.13", Region: "ap-hongkong", Status: Enabled},
	}}
	for _, host := range t.Hosts {
		for _, id := range []string{"host-gateway", "host-agent"} {
			t.Placements = append(t.Placements, Placement{HostID: host.ID, ComponentID: id, Status: Enabled})
		}
	}
	for host, ids := range map[string][]string{
		"control":   {"console-proxy", "web-host", "admin", "eventbus", "monitor", "collector", "cloudnode", "factor-mgr", "strategy"},
		"storage":   {"storage-primary", "storage-node", "storage-view", "archive", "access"},
		"compute-1": {"access", "egress-proxy", "trade"},
	} {
		for _, id := range ids {
			t.Placements = append(t.Placements, Placement{HostID: host, ComponentID: id, Status: Enabled})
		}
	}
	return t
}

func TestTopologyRejectsInvalidPlacementsAtomically(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Catalog, *Topology)
	}{
		{"control component elsewhere", func(c *Catalog, p *Topology) {
			for i := range p.Placements {
				if p.Placements[i].ComponentID == "console-proxy" {
					p.Placements[i].HostID = "storage"
				}
			}
		}},
		{"single replicas", func(c *Catalog, p *Topology) {
			p.Placements = append(p.Placements, Placement{HostID: "compute-1", ComponentID: "collector", Status: Enabled})
		}},
		{"single on disabled host still counts", func(c *Catalog, p *Topology) {
			p.Hosts[2].Status = Disabled
			p.Placements = append(p.Placements, Placement{HostID: "compute-1", ComponentID: "collector", Status: Enabled})
		}},
		{"same-host port conflict", func(c *Catalog, p *Topology) { componentPtr(c, "access").Health.Port = 11012 }},
		{"unknown host", func(c *Catalog, p *Topology) { p.Placements[0].HostID = "unknown" }},
		{"unknown component", func(c *Catalog, p *Topology) { p.Placements[0].ComponentID = "unknown" }},
		{"duplicate placement", func(c *Catalog, p *Topology) { p.Placements = append(p.Placements, p.Placements[0]) }},
		{"duplicate host", func(c *Catalog, p *Topology) { p.Hosts = append(p.Hosts, p.Hosts[0]) }},
		{"duplicate address", func(c *Catalog, p *Topology) { p.Hosts[1].Address = p.Hosts[0].Address }},
		{"host address includes scheme", func(c *Catalog, p *Topology) { p.Hosts[1].Address = "http://storage:11003" }},
		{"host address includes port", func(c *Catalog, p *Topology) { p.Hosts[1].Address = "storage:11003" }},
		{"host ID exceeds directory limit", func(c *Catalog, p *Topology) { p.Hosts[1].ID = strings.Repeat("s", 129) }},
		{"region exceeds directory limit", func(c *Catalog, p *Topology) { p.Hosts[1].Region = strings.Repeat("r", 129) }},
		{"invalid host status", func(c *Catalog, p *Topology) { p.Hosts[1].Status = "active" }},
		{"invalid placement status", func(c *Catalog, p *Topology) { p.Placements[0].Status = "active" }},
		{"control disabled", func(c *Catalog, p *Topology) { p.Hosts[0].Status = Disabled }},
		{"protected disabled", func(c *Catalog, p *Topology) {
			for i := range p.Placements {
				if p.Placements[i].ComponentID == "admin" {
					p.Placements[i].Status = Disabled
				}
			}
		}},
		{"protected removed", func(c *Catalog, p *Topology) {
			p.Placements = slices.DeleteFunc(p.Placements, func(p Placement) bool { return p.ComponentID == "admin" })
		}},
		{"automatic host placement removed", func(c *Catalog, p *Topology) {
			p.Placements = slices.DeleteFunc(p.Placements, func(p Placement) bool { return p.ComponentID == "host-agent" && p.HostID == "storage" })
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, p := mustCatalog(t), productionTopology()
			tc.mutate(&c, &p)
			before := jsonBytes(t, p)
			if err := c.ValidateTopology(p); err == nil {
				t.Fatal("invalid topology accepted")
			}
			if !bytes.Equal(before, jsonBytes(t, p)) {
				t.Fatal("validation changed its input")
			}
		})
	}
}

func TestPortCollisionIsPerHostAndDisabledPlacementHasNoListener(t *testing.T) {
	c, p := mustCatalog(t), productionTopology()
	componentPtr(&c, "egress-proxy").Health.Port = 20211 // same as storage-view, on another host
	p.Placements = append(p.Placements, Placement{HostID: "compute-1", ComponentID: "collector", Status: Disabled})
	if err := c.ValidateTopology(p); err != nil {
		t.Fatal(err)
	}
}

func TestCompileThreeHostRoutesAndVerificationScopes(t *testing.T) {
	c, p := mustCatalog(t), productionTopology()
	want := map[string]struct {
		routes  int
		callers []string
		hash    string
	}{
		"control":   {134, []string{"access", "admin", "cloudnode", "collector", "console", "host-gateway@compute-1", "host-gateway@control", "host-gateway@storage", "monitor", "moox-cli", "strategy", "trade"}, "1f409a23e4dbda58deb52ae03c372e2a09c5ecbf612c33ac8893e5dd3d4df8c9"},
		"storage":   {99, []string{"access", "archive", "collector", "console", "factor-mgr", "monitor", "moox-cli", "storage-view", "strategy"}, "bcd0f2ad1a25efd1b268685c0988d6a0a6d495d32f9e93b6616d6420c5575fb1"},
		"compute-1": {37, []string{"collector", "console", "monitor", "moox-cli", "strategy"}, "e5fcdab308554aeb8675c8f2728dbd87033d3c46571cb0816e3ed31272829b39"},
	}
	for _, host := range p.Hosts {
		compiled, err := c.Compile(p, host.ID)
		if err != nil {
			t.Fatal(err)
		}
		if compiled.Disabled || len(compiled.Hash) != 64 || len(compiled.Directory.Version) != 64 {
			t.Fatalf("invalid snapshot for %s", host.ID)
		}
		if len(compiled.Routes) != want[host.ID].routes || !slices.Equal(compiled.VerificationCallers, want[host.ID].callers) || compiled.Hash != want[host.ID].hash {
			t.Fatalf("review snapshot change for %s: routes=%d callers=%v hash=%s", host.ID, len(compiled.Routes), compiled.VerificationCallers, compiled.Hash)
		}
		if !slices.Equal(compiled.Directory.Services["trpc.moox.storage.PrimaryStore"], []string{"storage"}) || !slices.Equal(compiled.Directory.Services["trpc.moox.trade.TradeConsoleService"], []string{"compute-1"}) || !slices.Equal(compiled.Directory.Services["trpc.moox.hostagent.HostAgentMgr"], []string{"compute-1", "control", "storage"}) {
			t.Fatal("directory does not represent the topology")
		}
		if len(compiled.Directory.Hosts) != 3 {
			t.Fatal("missing directory hosts")
		}
		for _, route := range compiled.Routes {
			address, _, err := net.SplitHostPort(route.Address)
			if err != nil || address != "127.0.0.1" {
				t.Fatalf("non-local route: %+v", route)
			}
			if route.TimeoutMS <= 0 || route.MaxBodyBytes <= 0 || len(route.Callers) == 0 {
				t.Fatalf("unbounded route: %+v", route)
			}
			found := false
			for _, placement := range p.Placements {
				found = found || placement.HostID == host.ID && placement.ComponentID == route.ComponentID
			}
			if !found {
				t.Fatalf("route forwards to another host: %+v", route)
			}
			for _, caller := range route.Callers {
				if !c.Allowed(caller, route.ServicePath, route.Method) {
					t.Fatalf("compiled ACL widened: %s %s/%s", caller, route.ServicePath, route.Method)
				}
			}
			if route.ServicePath == GatewayControlPath && !slices.Equal(route.Callers, []string{"host-gateway@compute-1", "host-gateway@control", "host-gateway@storage"}) {
				t.Fatal("GatewayControl key scope is incorrect")
			}
		}
		for _, caller := range compiled.VerificationCallers {
			if strings.Contains(caller, "*") || slices.Contains([]string{"scf-collector", "factor-engine", "moox-skill"}, caller) {
				t.Fatalf("unexpanded or external verification scope %q", caller)
			}
		}
		if (host.ID == "compute-1") && slices.Contains(compiled.VerificationCallers, "access") {
			t.Fatal("access must not have permission to trade or egress")
		}
		if (host.ID == "storage" || host.ID == "control") && !slices.Contains(compiled.VerificationCallers, "access") {
			t.Fatal("external whitelist did not add access")
		}
		t.Logf("%s routes=%d callers=%v hash=%s", host.ID, len(compiled.Routes), compiled.VerificationCallers, compiled.Hash)
	}
}

func TestCompilePreservesMethodSpecificPermissionBoundaries(t *testing.T) {
	c := mustCatalog(t)
	compiled, err := c.Compile(productionTopology(), "compute-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range compiled.Routes {
		if route.ServicePath != "trpc.moox.trade.TradeConsoleService" {
			continue
		}
		if slices.Contains(route.Callers, "strategy") != slices.Contains([]string{"GetLogicalAccount", "ClaimLogicalAccountOwner", "ReleaseLogicalAccountOwner", "RebindLogicalAccountOwner"}, route.Method) {
			t.Fatalf("strategy permission crossed method boundary: %+v", route)
		}
	}
}

func TestCompilationIsOrderIndependentAndDoesNotMutateInputs(t *testing.T) {
	c, p := mustCatalog(t), productionTopology()
	beforeC, beforeP := jsonBytes(t, c), jsonBytes(t, p)
	first, err := c.Compile(p, "control")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeC, jsonBytes(t, c)) || !bytes.Equal(beforeP, jsonBytes(t, p)) {
		t.Fatal("compiler changed input")
	}
	slices.Reverse(c.Components)
	slices.Reverse(c.Principals)
	slices.Reverse(p.Hosts)
	slices.Reverse(p.Placements)
	for i := range c.Components {
		for j := range c.Components[i].Services {
			s := &c.Components[i].Services[j]
			slices.Reverse(s.Methods)
			slices.Reverse(s.ACL)
			for k := range s.ACL {
				slices.Reverse(s.ACL[k].Methods)
				slices.Reverse(s.ACL[k].Callers)
			}
		}
	}
	second, err := c.Compile(p, "control")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(jsonBytes(t, first), jsonBytes(t, second)) {
		t.Fatal("input ordering changed compiled definition")
	}
}

func TestHashTracksRemoteDirectoryAndLocalACLChanges(t *testing.T) {
	c, p := mustCatalog(t), productionTopology()
	first, err := c.Compile(p, "control")
	if err != nil {
		t.Fatal(err)
	}
	p.Hosts[1].PrivateAddress = "10.206.0.6"
	changed, err := c.Compile(p, "control")
	if err != nil {
		t.Fatal(err)
	}
	if first.Hash == changed.Hash || first.Directory.Version == changed.Directory.Version {
		t.Fatal("remote address change did not invalidate snapshots")
	}
	p = productionTopology()
	servicePtr(&c, "trpc.moox.cloudnode.CloudNodeMgr").ACL[1].Callers = append(servicePtr(&c, "trpc.moox.cloudnode.CloudNodeMgr").ACL[1].Callers, "monitor")
	changed, err = c.Compile(p, "control")
	if err != nil {
		t.Fatal(err)
	}
	if first.Hash == changed.Hash || first.Directory.Version != changed.Directory.Version {
		t.Fatal("ACL should change route hash and preserve directory version")
	}
}

func TestDisabledHostAndPlacementWithdrawRoutes(t *testing.T) {
	c, p := mustCatalog(t), productionTopology()
	p.Hosts[2].Status = Disabled
	compiled, err := c.Compile(p, "compute-1")
	if err != nil {
		t.Fatal(err)
	}
	if !compiled.Disabled || len(compiled.Routes) != 0 || len(compiled.VerificationCallers) != 0 {
		t.Fatal("disabled host retains routes or keys")
	}
	if _, exists := compiled.Directory.Hosts["compute-1"]; exists {
		t.Fatal("disabled host retained in directory")
	}
	if len(compiled.Directory.Services["trpc.moox.trade.TradeConsoleService"]) != 0 {
		t.Fatal("disabled host service retained in directory")
	}
	control, err := c.Compile(p, "control")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(control.VerificationCallers, "host-gateway@compute-1") {
		t.Fatal("disabled host cannot fetch disabled snapshot")
	}
	p = productionTopology()
	for i := range p.Placements {
		if p.Placements[i].ComponentID == "collector" {
			p.Placements[i].Status = Disabled
		}
	}
	compiled, err = c.Compile(p, "control")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range compiled.Routes {
		if route.ComponentID == "collector" {
			t.Fatal("disabled placement retains a route")
		}
	}
	if len(compiled.Directory.Services["trpc.moox.collector.MarketFetchRuntime"]) != 0 {
		t.Fatal("disabled placement remains discoverable")
	}
	if _, err := c.Compile(p, "unknown"); err == nil {
		t.Fatal("unknown host compiled")
	}
}
