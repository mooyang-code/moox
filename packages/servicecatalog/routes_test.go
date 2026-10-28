package servicecatalog

import (
	"bytes"
	"net"
	"slices"
	"strings"
	"testing"
)

// This is the intended three-host deployment, not a claim that production has
// migrated. The old service IDs and duplicate compute gateway are not inputs.
func productionTopology() Topology {
	t := Topology{ControlHostID: "control", Hosts: []Host{
		{ID: "control", Address: "203.0.113.10", Status: Enabled},
		{ID: "storage", Address: "146.56.196.204", PrivateAddress: "10.206.0.5", Region: "ap-nanjing", Status: Enabled},
		{ID: "overseas", Address: "43.132.204.177", PrivateAddress: "172.19.32.13", Region: "ap-hongkong", Status: Enabled},
	}}
	for _, host := range t.Hosts {
		for _, id := range []string{"host-gateway", "host-agent"} {
			t.Deployments = append(t.Deployments, Deployment{HostID: host.ID, ComponentID: id, Status: Enabled})
		}
	}
	for host, ids := range map[string][]string{
		"control":  {"console-proxy", "web-host", "admin", "eventbus", "monitor", "collector", "cloudfunction", "factor-mgr", "strategy"},
		"storage":  {"storage-primary", "storage-node", "storage-view", "access"},
		"overseas": {"access", "egress-proxy", "trade", "collector-stream"},
	} {
		for _, id := range ids {
			t.Deployments = append(t.Deployments, Deployment{HostID: host, ComponentID: id, Status: Enabled})
		}
	}
	return t
}

func TestTopologyRejectsInvalidDeploymentsAtomically(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Catalog, *Topology)
	}{
		{"control component elsewhere", func(c *Catalog, p *Topology) {
			for i := range p.Deployments {
				if p.Deployments[i].ComponentID == "console-proxy" {
					p.Deployments[i].HostID = "storage"
				}
			}
		}},
		{"single replicas", func(c *Catalog, p *Topology) {
			p.Deployments = append(p.Deployments, Deployment{HostID: "overseas", ComponentID: "collector", Status: Enabled})
		}},
		{"single on disabled host still counts", func(c *Catalog, p *Topology) {
			p.Hosts[2].Status = Disabled
			p.Deployments = append(p.Deployments, Deployment{HostID: "overseas", ComponentID: "collector", Status: Enabled})
		}},
		{"same-host port conflict", func(c *Catalog, p *Topology) { componentPtr(c, "access").Health.Port = 11012 }},
		{"unknown host", func(c *Catalog, p *Topology) { p.Deployments[0].HostID = "unknown" }},
		{"unknown component", func(c *Catalog, p *Topology) { p.Deployments[0].ComponentID = "unknown" }},
		{"duplicate deployment", func(c *Catalog, p *Topology) { p.Deployments = append(p.Deployments, p.Deployments[0]) }},
		{"duplicate host", func(c *Catalog, p *Topology) { p.Hosts = append(p.Hosts, p.Hosts[0]) }},
		{"duplicate address", func(c *Catalog, p *Topology) { p.Hosts[1].Address = p.Hosts[0].Address }},
		{"host address includes scheme", func(c *Catalog, p *Topology) { p.Hosts[1].Address = "http://storage:11003" }},
		{"host address includes port", func(c *Catalog, p *Topology) { p.Hosts[1].Address = "storage:11003" }},
		{"host ID exceeds directory limit", func(c *Catalog, p *Topology) { p.Hosts[1].ID = strings.Repeat("s", 129) }},
		{"region exceeds directory limit", func(c *Catalog, p *Topology) { p.Hosts[1].Region = strings.Repeat("r", 129) }},
		{"invalid host status", func(c *Catalog, p *Topology) { p.Hosts[1].Status = "active" }},
		{"invalid deployment status", func(c *Catalog, p *Topology) { p.Deployments[0].Status = "active" }},
		{"control disabled", func(c *Catalog, p *Topology) { p.Hosts[0].Status = Disabled }},
		{"protected disabled", func(c *Catalog, p *Topology) {
			for i := range p.Deployments {
				if p.Deployments[i].ComponentID == "admin" {
					p.Deployments[i].Status = Disabled
				}
			}
		}},
		{"protected removed", func(c *Catalog, p *Topology) {
			p.Deployments = slices.DeleteFunc(p.Deployments, func(p Deployment) bool { return p.ComponentID == "admin" })
		}},
		{"automatic host deployment removed", func(c *Catalog, p *Topology) {
			p.Deployments = slices.DeleteFunc(p.Deployments, func(p Deployment) bool { return p.ComponentID == "host-agent" && p.HostID == "storage" })
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

func TestPortCollisionIsPerHostAndDisabledDeploymentHasNoListener(t *testing.T) {
	c, p := mustCatalog(t), productionTopology()
	componentPtr(&c, "egress-proxy").Health.Port = 20211 // same as storage-view, on another host
	p.Deployments = append(p.Deployments, Deployment{HostID: "overseas", ComponentID: "collector", Status: Disabled})
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
		"control":  {188, []string{"access", "admin", "cloudfunction", "collector", "collector-stream", "console", "host-agent", "host-gateway@control", "host-gateway@overseas", "host-gateway@storage", "monitor", "moox-cli", "storage-view", "strategy", "trade"}, "96c1c2f984748922148e697e4e0f56632d8f91de556de33489e7c7107d4aeaff"},
		"storage":  {82, []string{"access", "archive-mgr", "collector", "collector-stream", "console", "factor-mgr", "monitor", "moox-cli", "strategy"}, "e0effeb80dbfc814badc9ea9bc7e5c934d838b18ce9a1069fa8b1e30a8d7f520"},
		"overseas": {38, []string{"collector", "console", "monitor", "moox-cli", "strategy"}, "f82ea3d40b5f3cfdfa3188507f36e4c4e6f90a83ec5b724a927b75db19f82a41"},
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
			t.Errorf("review snapshot change for %s: routes=%d callers=%v hash=%s", host.ID, len(compiled.Routes), compiled.VerificationCallers, compiled.Hash)
		}
		if !slices.Equal(compiled.Directory.Services["trpc.moox.storage.PrimaryStore"], []string{"storage"}) || !slices.Equal(compiled.Directory.Services["trpc.moox.trade.TradeConsoleService"], []string{"overseas"}) || !slices.Equal(compiled.Directory.Services["trpc.moox.hostagent.HostAgentMgr"], []string{"control", "overseas", "storage"}) {
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
			for _, deployment := range p.Deployments {
				found = found || deployment.HostID == host.ID && deployment.ComponentID == route.ComponentID
			}
			if !found {
				t.Fatalf("route forwards to another host: %+v", route)
			}
			for _, caller := range route.Callers {
				if !c.Allowed(caller, route.ServicePath, route.Method) {
					t.Fatalf("compiled ACL widened: %s %s/%s", caller, route.ServicePath, route.Method)
				}
			}
			if route.ServicePath == GatewayControlPath && !slices.Equal(route.Callers, []string{"host-gateway@control", "host-gateway@overseas", "host-gateway@storage"}) {
				t.Fatal("GatewayControl key scope is incorrect")
			}
		}
		for _, caller := range compiled.VerificationCallers {
			if strings.Contains(caller, "*") || slices.Contains([]string{"scf-collector", "factor-engine", "archive-worker", "moox-skill", "collector-operator"}, caller) {
				t.Fatalf("unexpanded or external verification scope %q", caller)
			}
		}
		if (host.ID == "overseas") && slices.Contains(compiled.VerificationCallers, "access") {
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
	compiled, err := c.Compile(productionTopology(), "overseas")
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
	slices.Reverse(p.Deployments)
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
	servicePtr(&c, "trpc.moox.cloudfunction.CloudFunctionMgr").ACL[1].Callers = append(servicePtr(&c, "trpc.moox.cloudfunction.CloudFunctionMgr").ACL[1].Callers, "monitor")
	changed, err = c.Compile(p, "control")
	if err != nil {
		t.Fatal(err)
	}
	if first.Hash == changed.Hash || first.Directory.Version != changed.Directory.Version {
		t.Fatal("ACL should change route hash and preserve directory version")
	}
}

func TestDisabledHostAndDeploymentWithdrawRoutes(t *testing.T) {
	c, p := mustCatalog(t), productionTopology()
	p.Hosts[2].Status = Disabled
	compiled, err := c.Compile(p, "overseas")
	if err != nil {
		t.Fatal(err)
	}
	if !compiled.Disabled || len(compiled.Routes) != 0 || len(compiled.VerificationCallers) != 0 {
		t.Fatal("disabled host retains routes or keys")
	}
	if _, exists := compiled.Directory.Hosts["overseas"]; exists {
		t.Fatal("disabled host retained in directory")
	}
	if len(compiled.Directory.Services["trpc.moox.trade.TradeConsoleService"]) != 0 {
		t.Fatal("disabled host service retained in directory")
	}
	control, err := c.Compile(p, "control")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(control.VerificationCallers, "host-gateway@overseas") {
		t.Fatal("disabled host cannot fetch disabled snapshot")
	}
	p = productionTopology()
	for i := range p.Deployments {
		if p.Deployments[i].ComponentID == "collector" {
			p.Deployments[i].Status = Disabled
		}
	}
	compiled, err = c.Compile(p, "control")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range compiled.Routes {
		if route.ComponentID == "collector" {
			t.Fatal("disabled deployment retains a route")
		}
	}
	if len(compiled.Directory.Services["trpc.moox.collector.CollectMgr"]) != 0 {
		t.Fatal("disabled deployment remains discoverable")
	}
	if _, err := c.Compile(p, "unknown"); err == nil {
		t.Fatal("unknown host compiled")
	}
}

// 长等待方法使用方法级超时，不被服务级 5 秒预算截断。
func TestRouteTimeoutUsesMethodOverride(t *testing.T) {
	c, p := mustCatalog(t), productionTopology()
	compiled, err := c.Compile(p, "storage")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, route := range compiled.Routes {
		if route.ServicePath == "trpc.moox.storage.PrimaryStore" && (route.Method == "WaitViewSyncPoint" || route.Method == "ReadRows") {
			got[route.Method] = route.TimeoutMS
		}
	}
	if got["WaitViewSyncPoint"] != 35000 || got["ReadRows"] != 5000 {
		t.Fatalf("unexpected route timeouts: %v", got)
	}
	service, _ := c.Service("trpc.moox.storage.PrimaryStore")
	if service.TimeoutFor("WaitViewSyncPoint") != 35000 || service.TimeoutFor("ReadRows") != 5000 {
		t.Fatal("TimeoutFor does not honor the method override")
	}
}

func TestArchiveMgrRoutesWhenPlacedOnControl(t *testing.T) {
	c, p := mustCatalog(t), productionTopology()
	p.Deployments = append(p.Deployments, Deployment{HostID: "control", ComponentID: "archive-mgr", Status: Enabled})
	if err := c.ValidateTopology(p); err != nil {
		t.Fatal(err)
	}
	control, err := c.Compile(p, "control")
	if err != nil {
		t.Fatal(err)
	}
	routes := map[string]Route{}
	for _, route := range control.Routes {
		if route.ComponentID == "archive-mgr" {
			routes[route.ServicePath+"/"+route.Method] = route
		}
	}
	if len(routes) != 15+4 {
		t.Fatalf("archive-mgr routes = %d, want 19", len(routes))
	}
	retire := routes["trpc.moox.archive.ArchiveMgr/RetireArchiveWorker"]
	if !slices.Equal(retire.Callers, []string{"moox-cli"}) || retire.ReadOnly {
		t.Fatalf("RetireArchiveWorker route = %+v", retire)
	}
	create := routes["trpc.moox.archive.ArchiveMgr/CreateArchiveTask"]
	if !slices.Equal(create.Callers, []string{"console", "moox-cli"}) || create.ReadOnly {
		t.Fatalf("CreateArchiveTask route = %+v", create)
	}
	list := routes["trpc.moox.archive.ArchiveMgr/ListArchiveTasks"]
	if !slices.Equal(list.Callers, []string{"console", "monitor", "moox-cli"}) || !list.ReadOnly {
		t.Fatalf("ListArchiveTasks 只读方法允许 monitor: %+v", list)
	}
	// ArchiveWorker 的 acl 为空：只有外部身份 archive-worker（经 Access 白名单）能调用。
	pull := routes["trpc.moox.archive.ArchiveWorker/PullArchiveRun"]
	if !slices.Equal(pull.Callers, []string{"access"}) {
		t.Fatalf("PullArchiveRun callers = %v", pull.Callers)
	}
	if !c.PrincipalAllowed("archive-worker", "trpc.moox.archive.ArchiveWorker", "PullArchiveRun") ||
		c.PrincipalAllowed("archive-worker", "trpc.moox.archive.ArchiveMgr", "CreateArchiveTask") ||
		c.PrincipalAllowed("archive-worker", "trpc.moox.storage.PrimaryStore", "UpsertFields") ||
		c.PrincipalAllowed("factor-engine", "trpc.moox.archive.ArchiveWorker", "PullArchiveRun") {
		t.Fatal("archive-worker 的外部白名单边界不对")
	}
}

// 出站主机：目录记录中转主机，control 快照列出该主机允许的中转端口与调用方；直达主机不带许可，control 不能是出站主机。
func TestOutboundHostCompilesRelayAllowlist(t *testing.T) {
	c, p := mustCatalog(t), productionTopology()
	baseline, err := c.Compile(p, "storage")
	if err != nil {
		t.Fatal(err)
	}
	p.Hosts[2].Reachability = ReachabilityOutbound
	control, err := c.Compile(p, "control")
	if err != nil {
		t.Fatal(err)
	}
	if control.Directory.Hosts["overseas"].Relay != "control" || control.Directory.Hosts["storage"].Relay != "" {
		t.Fatalf("directory relay = %+v", control.Directory.Hosts)
	}
	if len(control.Relays) != 1 || control.Relays[0].HostID != "overseas" {
		t.Fatalf("relays = %+v", control.Relays)
	}
	relay := control.Relays[0]
	if relay.Ports[0].Port != HostGatewayRemotePort || !slices.Equal(relay.Ports[0].Callers, []string{"collector", "console", "monitor", "moox-cli", "strategy"}) {
		t.Fatalf("gateway port = %+v", relay.Ports[0])
	}
	if !relay.Allowed("monitor", HostGatewayRemotePort) || relay.Allowed("admin", HostGatewayRemotePort) || relay.Allowed("monitor", 1) {
		t.Fatal("relay allowlist is wrong")
	}
	if len(relay.Ports) < 2 {
		t.Fatal("component health ports are missing")
	}
	for _, port := range relay.Ports[1:] {
		if !slices.Equal(port.Callers, []string{"monitor", "moox-cli"}) {
			t.Fatalf("health port %d open to %v", port.Port, port.Callers)
		}
	}
	storage, err := c.Compile(p, "storage")
	if err != nil {
		t.Fatal(err)
	}
	if len(storage.Relays) != 0 || storage.Directory.Hosts["overseas"].Relay != "control" || storage.Hash == baseline.Hash {
		t.Fatal("direct host must see the relay in its directory but carry no allowlist")
	}
	p.Hosts[2].Status = Disabled
	if control, err = c.Compile(p, "control"); err != nil || len(control.Relays) != 0 {
		t.Fatalf("disabled outbound host keeps relay: %v %+v", err, control.Relays)
	}

	p = productionTopology()
	p.Hosts[0].Reachability = ReachabilityOutbound
	if _, err := c.Compile(p, "control"); err == nil {
		t.Fatal("outbound control host accepted")
	}
	p = productionTopology()
	p.Hosts[1].Reachability = "inbound"
	if _, err := c.Compile(p, "control"); err == nil {
		t.Fatal("unknown reachability accepted")
	}
}

// 因子机是只能出站的主机：外部程序没有网关路由，只通过 Access 访问系统；它们的健康端口进入中转许可，只对 Monitor 与 moox-cli 开放。
func TestOutboundFactorHostRelaysExternalProgramHealthPorts(t *testing.T) {
	c, p := mustCatalog(t), productionTopology()
	p.Hosts = append(p.Hosts, Host{ID: "factor", Address: "192.168.0.103", Status: Enabled, Reachability: ReachabilityOutbound})
	for _, id := range []string{"host-gateway", "host-agent", "factor-engine", "archive-worker"} {
		p.Deployments = append(p.Deployments, Deployment{HostID: "factor", ComponentID: id, Status: Enabled})
	}
	if err := c.ValidateTopology(p); err != nil {
		t.Fatal(err)
	}
	control, err := c.Compile(p, "control")
	if err != nil {
		t.Fatal(err)
	}
	var relay *RelayTarget
	for i := range control.Relays {
		if control.Relays[i].HostID == "factor" {
			relay = &control.Relays[i]
		}
	}
	if relay == nil || !c.ValidRelay(*relay) {
		t.Fatalf("factor relay = %+v", control.Relays)
	}
	for _, port := range []int{11417, 11418, 11012, 11425} {
		if !relay.Allowed("monitor", port) || !relay.Allowed("moox-cli", port) || relay.Allowed("collector", port) {
			t.Fatalf("health port %d is not limited to monitor and moox-cli: %+v", port, relay.Ports)
		}
	}
	if relay.Allowed("moox-cli", 22) || relay.Allowed("monitor", 22) {
		t.Fatal("ssh port must not be relayed")
	}
	// 外部程序不是网关调用方：全部路由的调用方里没有它们，外部身份只经 access 进入。
	for _, route := range control.Routes {
		if slices.Contains(route.Callers, "factor-engine") || slices.Contains(route.Callers, "archive-worker") {
			t.Fatalf("external program is a gateway caller: %+v", route)
		}
	}
	if slices.Contains(control.VerificationCallers, "factor-engine") || slices.Contains(control.VerificationCallers, "archive-worker") {
		t.Fatalf("external program is a verification caller: %v", control.VerificationCallers)
	}
	// 停用主机后，中转许可随之撤销。
	for i := range p.Hosts {
		if p.Hosts[i].ID == "factor" {
			p.Hosts[i].Status = Disabled
		}
	}
	if control, err = c.Compile(p, "control"); err != nil {
		t.Fatal(err)
	}
	for _, relay := range control.Relays {
		if relay.HostID == "factor" {
			t.Fatalf("disabled factor host keeps relay: %+v", relay)
		}
	}
}
