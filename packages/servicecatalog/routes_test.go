package servicecatalog

import (
	"bytes"
	"net"
	"reflect"
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
		"control":  {"admin", "eventbus", "monitor", "collector", "factor-mgr", "strategy"},
		"storage":  {"storage-primary", "storage-view"},
		"overseas": {"egress-proxy", "trade", "collector-stream"},
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
				if p.Deployments[i].ComponentID == "admin" {
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
		{"same-host port conflict", func(c *Catalog, p *Topology) { componentPtr(c, "trade").Health.Port = 11012 }},
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

// relay_http 端口计入组件端口（拓扑的同主机端口冲突与部署预检都用它）；与别的组件撞端口时目录直接拒绝。
func TestRelayHTTPPortJoinsComponentPorts(t *testing.T) {
	c, p := mustCatalog(t), productionTopology()
	p.Deployments = append(p.Deployments, Deployment{HostID: "control", ComponentID: "archive-worker", Status: Enabled})
	if err := c.ValidateTopology(p); err != nil {
		t.Fatal(err)
	}
	worker, _ := c.Component("archive-worker")
	if !slices.Contains(componentPorts(worker), 11428) {
		t.Fatalf("componentPorts misses relay_http: %v", componentPorts(worker))
	}
	componentPtr(&c, "monitor").Ports = append(componentPtr(&c, "monitor").Ports, 11428)
	if err := c.ValidateTopology(p); err == nil || !strings.Contains(err.Error(), "11428") {
		t.Fatalf("relay_http port conflict accepted: %v", err)
	}
}

// routeCallers 返回一台主机全部路由调用方的并集（排序后）：这台主机的网关只接受这些调用方的请求。
func routeCallerUnion(compiled CompiledHost) []string {
	set := map[string]bool{}
	for _, route := range compiled.Routes {
		for _, caller := range route.Callers {
			set[caller] = true
		}
	}
	callers := make([]string, 0, len(set))
	for caller := range set {
		callers = append(callers, caller)
	}
	slices.Sort(callers)
	return callers
}

func TestCompileThreeHostRoutesAndCallerScopes(t *testing.T) {
	c, p := mustCatalog(t), productionTopology()
	if err := c.ValidateTopology(p); err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		routes  int
		callers []string
	}{
		"control":  {170, []string{"collector", "collector-stream", "console", "factor-engine", "host-agent", "host-gateway", "monitor", "moox-cli", "strategy", "trade"}},
		"storage":  {66, []string{"archive-mgr", "archive-worker", "collector", "collector-stream", "console", "factor-engine", "factor-mgr", "monitor", "moox-cli", "moox-skill", "scf-collector", "strategy"}},
		"overseas": {51, []string{"collector", "console", "monitor", "moox-cli", "strategy"}},
	}
	for _, host := range p.Hosts {
		compiled, err := c.Compile(p, host.ID)
		if err != nil {
			t.Fatal(err)
		}
		if compiled.Disabled || compiled.HostID != host.ID || len(compiled.Unknown) != 0 || len(compiled.Relays) != 0 {
			t.Fatalf("invalid compiled view for %s: %+v", host.ID, compiled)
		}
		callers := routeCallerUnion(compiled)
		if len(compiled.Routes) != want[host.ID].routes || !slices.Equal(callers, want[host.ID].callers) {
			t.Errorf("review route change for %s: routes=%d callers=%v", host.ID, len(compiled.Routes), callers)
		}
		if err := compiled.Directory.Validate(); err != nil {
			t.Fatalf("%s directory: %v", host.ID, err)
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
				// 外部身份在名单上，但只能从外部入口进来（PrincipalAllowed）；其余调用方按 Allowed。
				if !c.Allowed(caller, route.ServicePath, route.Method) && !c.PrincipalAllowed(caller, route.ServicePath, route.Method) {
					t.Fatalf("compiled ACL widened: %s %s/%s", caller, route.ServicePath, route.Method)
				}
			}
			if route.ServicePath == GatewayControlPath && !slices.Equal(route.Callers, []string{"host-gateway"}) {
				t.Fatal("GatewayControl key scope is incorrect")
			}
		}
		for _, caller := range callers {
			if strings.Contains(caller, "*") || strings.Contains(caller, "@") || caller == "access" {
				t.Fatalf("unexpanded or removed caller scope %q", caller)
			}
			if host.ID == "overseas" && c.ExternalIdentity(caller) {
				t.Fatalf("外部身份 %s 不该出现在海外机（Trade、出口代理）的名单上", caller)
			}
		}
		t.Logf("%s routes=%d callers=%v", host.ID, len(compiled.Routes), callers)
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
		if slices.Contains(route.Callers, "strategy") != slices.Contains([]string{"GetLogicalAccount", "CloseTargetSession"}, route.Method) {
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
	slices.Reverse(c.Identities)
	slices.Reverse(p.Hosts)
	slices.Reverse(p.Deployments)
	for i := range c.Components {
		for j := range c.Components[i].Services {
			s := &c.Components[i].Services[j]
			slices.Reverse(s.Methods)
			for k := range s.Methods {
				slices.Reverse(s.Methods[k].Callers)
				if s.Methods[k].Operators != nil {
					reversed := slices.Clone(*s.Methods[k].Operators)
					slices.Reverse(reversed)
					s.Methods[k].Operators = &reversed
				}
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

// 别的主机改了地址：本机路由不变，目录里那台主机的地址跟着变。目录里放宽一个方法的调用方：只有这条路由变，目录不变。
func TestCompileTracksRemoteDirectoryAndLocalACLChanges(t *testing.T) {
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
	if changed.Directory.Hosts["storage"].PrivateAddress != "10.206.0.6" || first.Directory.Hosts["storage"].PrivateAddress != "10.206.0.5" {
		t.Fatalf("remote address change did not reach the directory: %+v", changed.Directory.Hosts["storage"])
	}
	if !reflect.DeepEqual(first.Routes, changed.Routes) || !reflect.DeepEqual(first.Directory.Services, changed.Directory.Services) {
		t.Fatal("remote address change must not touch local routes or service placement")
	}
	p = productionTopology()
	create := methodPtr(&c, "trpc.moox.collector.CollectMgr", "CreateTask")
	if slices.Contains(c.routeCallers(*create), "monitor") {
		t.Fatal("test precondition: monitor must not already call CreateTask")
	}
	create.Callers = append(create.Callers, "monitor")
	changed, err = c.Compile(p, "control")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Directory, changed.Directory) || len(first.Routes) != len(changed.Routes) {
		t.Fatal("ACL change must preserve the directory and the route set")
	}
	for i, route := range changed.Routes {
		isCreate := route.ServicePath == "trpc.moox.collector.CollectMgr" && route.Method == "CreateTask"
		if isCreate && (!slices.Contains(route.Callers, "monitor") || slices.Contains(first.Routes[i].Callers, "monitor")) {
			t.Fatalf("ACL change did not reach the route: %+v", route)
		}
		if !isCreate && !reflect.DeepEqual(route, first.Routes[i]) {
			t.Fatalf("ACL change widened another route: %+v", route)
		}
	}
}

func TestDisabledHostAndDeploymentWithdrawRoutes(t *testing.T) {
	c, p := mustCatalog(t), productionTopology()
	p.Hosts[2].Status = Disabled
	compiled, err := c.Compile(p, "overseas")
	if err != nil {
		t.Fatal(err)
	}
	if !compiled.Disabled || len(compiled.Routes) != 0 || len(compiled.Relays) != 0 {
		t.Fatal("disabled host retains routes or relays")
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
	// 停用的主机仍要验签取到快照（才知道自己已停用）：control 上取快照的路由仍给 host-gateway，
	// 它的网关还在签名调用方里，别的组件不在。
	pull := slices.IndexFunc(control.Routes, func(route Route) bool {
		return route.ServicePath == GatewayControlPath && route.Method == "PullSnapshot"
	})
	if pull < 0 || !slices.Equal(control.Routes[pull].Callers, []string{"host-gateway"}) || !slices.Equal(c.SigningCallers(p)["overseas"], []string{"host-gateway"}) {
		t.Fatalf("disabled host cannot fetch its snapshot: %v", c.SigningCallers(p)["overseas"])
	}
	if signing := c.SigningCallers(p)["control"]; !slices.Contains(signing, "console") || !slices.Contains(signing, "admin") || slices.Contains(c.SigningCallers(p)["storage"], "console") {
		t.Fatalf("console signs only on the host that runs admin: %v", signing)
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
		if route.ServicePath == "trpc.moox.storage.DataView" && (route.Method == "WaitViewApplied" || route.Method == "QueryTimeSeriesRows") {
			got[route.Method] = route.TimeoutMS
		}
	}
	if got["WaitViewApplied"] != 35000 || got["QueryTimeSeriesRows"] != 25000 {
		t.Fatalf("unexpected route timeouts: %v", got)
	}
	service, _ := c.Service("trpc.moox.storage.DataView")
	if service.TimeoutFor("WaitViewApplied") != 35000 || service.TimeoutFor("QueryTimeSeriesRows") != 25000 {
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
	// ArchiveMgr 17 个（含生命周期两个）、ArchiveWorker 4 个。
	if len(routes) != 17+4 {
		t.Fatalf("archive-mgr routes = %d, want 21", len(routes))
	}
	if route := routes["trpc.moox.archive.ArchiveMgr/GetLifecycle"]; !slices.Equal(route.Callers, []string{"console", "moox-cli"}) || !route.ReadOnly {
		t.Fatalf("GetLifecycle 给管理台与 moox-cli 读: %+v", route)
	}
	for _, method := range []string{"StartArchiveImport", "ClearArchiveUnrecoverable", "ControlLifecycle"} {
		if route := routes["trpc.moox.archive.ArchiveMgr/"+method]; !slices.Equal(route.Callers, []string{"moox-cli"}) || route.ReadOnly {
			t.Fatalf("%s 只给 moox-cli: %+v", method, route)
		}
	}
	create := routes["trpc.moox.archive.ArchiveMgr/CreateArchiveTask"]
	if !slices.Equal(create.Callers, []string{"console", "moox-cli"}) || create.ReadOnly {
		t.Fatalf("CreateArchiveTask route = %+v", create)
	}
	list := routes["trpc.moox.archive.ArchiveMgr/ListArchiveTasks"]
	if !slices.Equal(list.Callers, []string{"console", "monitor", "moox-cli", "strategy"}) || !list.ReadOnly {
		t.Fatalf("ListArchiveTasks 只读方法允许 monitor 与 strategy: %+v", list)
	}
	// ArchiveWorker 的方法只有归档 worker 能调：它用所在主机的密钥直接到网关，不是外部身份。
	pull := routes["trpc.moox.archive.ArchiveWorker/PullArchiveRun"]
	if !slices.Equal(pull.Callers, []string{"archive-worker"}) {
		t.Fatalf("PullArchiveRun callers = %v", pull.Callers)
	}
	if !c.Allowed("archive-worker", "trpc.moox.archive.ArchiveWorker", "PullArchiveRun") ||
		c.PrincipalAllowed("archive-worker", "trpc.moox.archive.ArchiveWorker", "PullArchiveRun") ||
		c.Allowed("archive-worker", "trpc.moox.archive.ArchiveMgr", "CreateArchiveTask") ||
		c.Allowed("archive-worker", "trpc.moox.storage.PrimaryStore", "UpsertFields") ||
		c.Allowed("factor-engine", "trpc.moox.archive.ArchiveWorker", "PullArchiveRun") {
		t.Fatal("archive-worker 能调的方法边界不对")
	}
}

// 出站主机：目录记录中转主机，control 快照列出该主机允许的中转端口与调用方；直达主机不带许可，control 不能是出站主机。
func TestOutboundHostCompilesRelayAllowlist(t *testing.T) {
	c, p := mustCatalog(t), productionTopology()
	p.Hosts[2].Reachability = ReachabilityOutbound
	if err := c.ValidateTopology(p); err != nil {
		t.Fatal(err)
	}
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
	if len(relay.Ports) != 1 {
		t.Fatalf("组件健康端口不经中转（组件健康只看上报）: %+v", relay.Ports)
	}
	storage, err := c.Compile(p, "storage")
	if err != nil {
		t.Fatal(err)
	}
	if len(storage.Relays) != 0 || storage.Directory.Hosts["overseas"].Relay != "control" {
		t.Fatal("direct host must see the relay in its directory but carry no allowlist")
	}
	p.Hosts[2].Status = Disabled
	if control, err = c.Compile(p, "control"); err != nil || len(control.Relays) != 0 {
		t.Fatalf("disabled outbound host keeps relay: %v %+v", err, control.Relays)
	}

	p = productionTopology()
	p.Hosts[0].Reachability = ReachabilityOutbound
	if err := c.ValidateTopology(p); err == nil {
		t.Fatal("outbound control host accepted")
	}
	p = productionTopology()
	p.Hosts[1].Reachability = "inbound"
	if err := c.ValidateTopology(p); err == nil {
		t.Fatal("unknown reachability accepted")
	}
}

// 因子机是只能出站的主机：组件健康只看上报，它们的健康端口不进中转许可。
// 因子引擎声明的服务另见 TestExternalProgramServiceRoutesThroughItsHostGateway。
func TestOutboundFactorHostDoesNotRelayHealthPorts(t *testing.T) {
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
	if relay == nil {
		t.Fatalf("factor relay = %+v", control.Relays)
	}
	for _, port := range []int{11417, 11418, 11012, 11425} {
		if relay.Allowed("monitor", port) || relay.Allowed("moox-cli", port) || relay.Allowed("collector", port) {
			t.Fatalf("health port %d must not be relayed: %+v", port, relay.Ports)
		}
	}
	if relay.Allowed("moox-cli", 22) || relay.Allowed("monitor", 22) {
		t.Fatal("ssh port must not be relayed")
	}
	// 归档 worker 的数据端口只对控制台（下载与预览）与 moox-cli（导入写入）开放。
	if !relay.Allowed("console", 11428) || !relay.Allowed("moox-cli", 11428) || relay.Allowed("monitor", 11428) || relay.Allowed("console", 11418) {
		t.Fatalf("archive data port relay is wrong: %+v", relay.Ports)
	}
	if callers, ok := c.RelayHTTPCallers(11428); !ok || !slices.Equal(callers, []string{"console", "moox-cli"}) || !c.LocalRelayPort(11428) || c.LocalRelayPort(11418) || c.LocalRelayPort(HostGatewayRemotePort) {
		t.Fatalf("relay_http port declaration is wrong: %v", callers)
	}
	for i := 1; i < len(relay.Ports); i++ {
		if relay.Ports[0].Port != HostGatewayRemotePort || i > 1 && relay.Ports[i-1].Port >= relay.Ports[i].Port {
			t.Fatalf("relay ports are not ordered: %+v", relay.Ports)
		}
	}
	// 装在可直达主机上时没有中转许可，但端口照样参与同主机端口冲突检查。
	direct := productionTopology()
	direct.Deployments = append(direct.Deployments, Deployment{HostID: "storage", ComponentID: "archive-worker", Status: Enabled})
	if err := c.ValidateTopology(direct); err != nil {
		t.Fatal(err)
	}
	// 外部程序是普通的网关调用方：只出现在 callers 里写了它的方法上。
	for _, route := range control.Routes {
		for _, id := range []string{"factor-engine", "archive-worker"} {
			if slices.Contains(route.Callers, id) != c.Allowed(id, route.ServicePath, route.Method) {
				t.Fatalf("external program route callers differ from the catalog: %+v", route)
			}
		}
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

// 外部程序声明服务之后（因子引擎的 EngineControl）：所在主机算出它的路由（指向本机端口、回放方法只给 strategy）；
// 因子机只能出站时，control 的中转许可让 strategy 打开到因子机网关端口的中转，
// 服务端口本身不能直接中转。
func TestExternalProgramServiceRoutesThroughItsHostGateway(t *testing.T) {
	c, p := mustCatalog(t), productionTopology()
	p.Hosts = append(p.Hosts, Host{ID: "factor", Address: "192.168.0.103", Status: Enabled, Reachability: ReachabilityOutbound})
	for _, id := range []string{"host-gateway", "host-agent", "factor-engine", "archive-worker"} {
		p.Deployments = append(p.Deployments, Deployment{HostID: "factor", ComponentID: id, Status: Enabled})
	}
	factor, err := c.Compile(p, "factor")
	if err != nil {
		t.Fatal(err)
	}
	// 回放三个方法只给 strategy；生命周期、任务检查与本地写入只给 moox-cli。
	type want struct {
		timeout  int64
		readOnly bool
		caller   string
	}
	wants := map[string]want{
		"OpenReplay": {600000, false, "strategy"}, "StepReplay": {120000, false, "strategy"}, "CloseReplay": {120000, false, "strategy"},
		"GetLifecycle": {120000, true, "moox-cli"}, "ControlLifecycle": {120000, false, "moox-cli"}, "CheckTask": {120000, true, "moox-cli"},
		"WriteSeries": {120000, false, "moox-cli"}, "GetSeriesFields": {120000, true, "moox-cli"}, "CheckSeriesSubject": {120000, true, "moox-cli"},
		"GetSeriesBounds": {120000, true, "moox-cli"},
	}
	got := map[string]Route{}
	for _, route := range factor.Routes {
		if route.ServicePath == "trpc.moox.factor.EngineControl" {
			got[route.Method] = route
		}
	}
	if len(got) != len(wants) {
		t.Fatalf("factor host EngineControl routes = %+v", got)
	}
	for method, w := range wants {
		route := got[method]
		if route.ComponentID != "factor-engine" || route.Address != "127.0.0.1:11423" || route.TimeoutMS != w.timeout ||
			route.MaxBodyBytes != DefaultMaxBodyBytes || route.ReadOnly != w.readOnly || !slices.Equal(route.Callers, []string{w.caller}) {
			t.Fatalf("%s route = %+v", method, route)
		}
	}
	// 外部程序用所在主机的密钥签名：因子机上能签名的是它启用的组件，strategy、moox-cli 这些调用方用各自主机的密钥。
	if signing := c.SigningCallers(p)["factor"]; !slices.Equal(signing, []string{"archive-worker", "factor-engine", "host-agent", "host-gateway"}) {
		t.Fatalf("factor host signing callers = %v", signing)
	}
	if !slices.Equal(factor.Directory.Services["trpc.moox.factor.EngineControl"], []string{"factor"}) {
		t.Fatalf("directory = %v", factor.Directory.Services["trpc.moox.factor.EngineControl"])
	}

	control, err := c.Compile(p, "control")
	if err != nil {
		t.Fatal(err)
	}
	if control.Directory.Hosts["factor"].Relay != "control" {
		t.Fatalf("factor host must be reached through control: %+v", control.Directory.Hosts["factor"])
	}
	for _, route := range control.Routes {
		if route.ServicePath == "trpc.moox.factor.EngineControl" {
			t.Fatalf("control must not route EngineControl locally: %+v", route)
		}
	}
	var relay *RelayTarget
	for i := range control.Relays {
		if control.Relays[i].HostID == "factor" {
			relay = &control.Relays[i]
		}
	}
	if relay == nil {
		t.Fatalf("factor relay = %+v", control.Relays)
	}
	if !relay.Allowed("strategy", HostGatewayRemotePort) || relay.Allowed("strategy", 11423) || relay.Allowed("console", 11423) {
		t.Fatalf("strategy must reach EngineControl only through the factor gateway port: %+v", relay.Ports)
	}

	// 停用因子引擎的部署后，路由与中转许可里的 strategy 随之撤销（因子机上其他服务都没有给 strategy）。
	for i := range p.Deployments {
		if p.Deployments[i].ComponentID == "factor-engine" {
			p.Deployments[i].Status = Disabled
		}
	}
	if factor, err = c.Compile(p, "factor"); err != nil {
		t.Fatal(err)
	}
	for _, route := range factor.Routes {
		if route.ComponentID == "factor-engine" {
			t.Fatalf("disabled factor-engine keeps a route: %+v", route)
		}
	}
	if control, err = c.Compile(p, "control"); err != nil {
		t.Fatal(err)
	}
	for _, relay := range control.Relays {
		if relay.HostID == "factor" && relay.Allowed("strategy", HostGatewayRemotePort) {
			t.Fatalf("disabled factor-engine keeps strategy in the relay: %+v", relay.Ports)
		}
	}
}

// 拓扑里有本进程内嵌目录不认识的组件（控制机与本机版本不一致时出现）：Compile 忽略它，其余路由、目录与中转许可照常生效，
// Unknown 里列出它（排序、去重）。
func TestCompileIgnoresUnknownComponents(t *testing.T) {
	c, p := mustCatalog(t), productionTopology()
	p.Hosts[2].Reachability = ReachabilityOutbound
	want := map[string]CompiledHost{}
	for _, host := range p.Hosts {
		compiled, err := c.Compile(p, host.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(compiled.Unknown) != 0 || len(compiled.Routes) == 0 {
			t.Fatalf("%s baseline: unknown=%v routes=%d", host.ID, compiled.Unknown, len(compiled.Routes))
		}
		want[host.ID] = compiled
	}
	signing := c.SigningCallers(p)

	p.Deployments = append([]Deployment{
		{HostID: "storage", ComponentID: "next-version-b", Status: Enabled},
		{HostID: "control", ComponentID: "next-version-a", Status: Enabled},
	}, p.Deployments...)
	p.Deployments = append(p.Deployments,
		Deployment{HostID: "overseas", ComponentID: "next-version-a", Status: Enabled},
		Deployment{HostID: "overseas", ComponentID: "next-version-c", Status: Disabled},
	)
	if err := c.ValidateTopology(p); err == nil {
		t.Fatal("Admin 写入时仍要拒绝不认识的组件")
	}
	unknown := []string{"next-version-a", "next-version-b", "next-version-c"}
	for _, host := range p.Hosts {
		compiled, err := c.Compile(p, host.ID)
		if err != nil {
			t.Fatalf("%s: %v", host.ID, err)
		}
		if !slices.Equal(compiled.Unknown, unknown) {
			t.Fatalf("%s unknown = %v", host.ID, compiled.Unknown)
		}
		compiled.Unknown = nil
		if !reflect.DeepEqual(compiled, want[host.ID]) {
			t.Fatalf("%s: 不认识的组件改变了其余路由、目录或中转许可", host.ID)
		}
		for _, route := range compiled.Routes {
			if strings.HasPrefix(route.ComponentID, "next-version") {
				t.Fatalf("unknown component got a route: %+v", route)
			}
		}
	}
	directory, resolved := c.Resolve(p)
	if !slices.Equal(resolved, unknown) || !reflect.DeepEqual(directory, want["control"].Directory) {
		t.Fatalf("Resolve unknown = %v", resolved)
	}
	if err := directory.Validate(); err != nil {
		t.Fatal(err)
	}
	// 不认识的组件不能用主机密钥签名。
	if !reflect.DeepEqual(c.SigningCallers(p), signing) {
		t.Fatalf("unknown component became a signing caller: %v", c.SigningCallers(p))
	}
}

// 快照与目录只下发主机和部署：控制机就是部署了 admin 的那台，拼出的拓扑与原拓扑算出同样的结果。
func TestNewTopologyFindsControlByAdminDeployment(t *testing.T) {
	c, p := mustCatalog(t), productionTopology()
	rebuilt := NewTopology(p.Hosts, p.Deployments)
	if rebuilt.ControlHostID != "control" {
		t.Fatalf("control host = %q", rebuilt.ControlHostID)
	}
	if err := c.ValidateTopology(rebuilt); err != nil {
		t.Fatal(err)
	}
	for _, host := range p.Hosts {
		want, err := c.Compile(p, host.ID)
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.Compile(rebuilt, host.ID)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: rebuilt topology compiles differently: %v", host.ID, err)
		}
	}
	withoutAdmin := slices.DeleteFunc(slices.Clone(p.Deployments), func(d Deployment) bool { return d.ComponentID == "admin" })
	if got := NewTopology(p.Hosts, withoutAdmin); got.ControlHostID != "" {
		t.Fatalf("control host without admin = %q", got.ControlHostID)
	}
}
