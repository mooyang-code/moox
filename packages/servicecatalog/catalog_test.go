package servicecatalog

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func mustCatalog(t *testing.T) Catalog {
	t.Helper()
	c, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func componentPtr(c *Catalog, id string) *Component {
	for i := range c.Components {
		if c.Components[i].ID == id {
			return &c.Components[i]
		}
	}
	panic("unknown component: " + id)
}

func servicePtr(c *Catalog, path string) *Service {
	for i := range c.Components {
		for j := range c.Components[i].Services {
			if c.Components[i].Services[j].Path == path {
				return &c.Components[i].Services[j]
			}
		}
	}
	panic("unknown service: " + path)
}

func methodPtr(c *Catalog, path, name string) *Method {
	service := servicePtr(c, path)
	for i := range service.Methods {
		if service.Methods[i].Name == name {
			return &service.Methods[i]
		}
	}
	panic("unknown method: " + path + "/" + name)
}

func operators(ids ...string) *[]string { return &ids }

func TestEmbeddedCatalogDefinesEveryComponent(t *testing.T) {
	c := mustCatalog(t)
	want := map[string]int{"host-gateway": 11012, "host-agent": 11425, "admin": 11010, "eventbus": 11419, "monitor": 11409, "collector": 11412, "factor-mgr": 11414, "archive-mgr": 11416, "strategy": 11431, "storage-primary": 20210, "storage-view": 20211, "access": 11014, "egress-proxy": 11441, "trade": 11210, "collector-stream": 11451, "callback-dispatcher": 11461, "factor-engine": 11417, "archive-worker": 11418}
	if len(c.Components) != len(want) {
		t.Fatalf("components = %d", len(c.Components))
	}
	for _, component := range c.Components {
		if component.Health.Port != want[component.ID] {
			t.Fatalf("incorrect health for %s", component.ID)
		}
		if component.Protected != slices.Contains([]string{"host-gateway", "admin"}, component.ID) {
			t.Fatalf("incorrect protection for %s", component.ID)
		}
		if (component.Replicas == Multi) != (component.ID == "access") {
			t.Fatalf("incorrect replicas for %s", component.ID)
		}
	}
	// 浏览器入口 9527 由 Admin 自己监听。
	admin, _ := c.Component("admin")
	if admin.Scope != ScopeControl || !slices.Equal(admin.Ports, []int{9527}) {
		t.Fatal("admin browser entry registration is incorrect")
	}
}

func TestCatalogDecodeIsStrictAndBounded(t *testing.T) {
	for _, raw := range [][]byte{append(slices.Clone(embedded), []byte("unknown: true\n")...), append(slices.Clone(embedded), []byte("---\nversion: 1\n")...), bytes.Repeat([]byte(" "), (2<<20)+1), nil} {
		if _, err := Decode(bytes.NewReader(raw)); err == nil {
			t.Fatal("invalid catalog was accepted")
		}
	}
}

func TestCatalogRejectsInvalidDefinitions(t *testing.T) {
	const cloud = "trpc.moox.collector.CollectMgr"
	cases := []struct {
		name   string
		mutate func(*Catalog)
	}{
		{"duplicate component", func(c *Catalog) { c.Components = append(c.Components, c.Components[0]) }},
		{"binary alias", func(c *Catalog) { componentPtr(c, "admin").Binary = "caddy" }},
		{"invalid scope", func(c *Catalog) { c.Components[0].Scope = "arbitrary" }},
		{"invalid replicas", func(c *Catalog) { c.Components[0].Replicas = "unlimited" }},
		{"host multi", func(c *Catalog) { c.Components[0].Replicas = Multi }},
		{"missing health port", func(c *Catalog) { c.Components[0].Health.Port = 0 }},
		{"duplicate port", func(c *Catalog) { c.Components[0].Ports = append(c.Components[0].Ports, 11012) }},
		{"unknown caller", func(c *Catalog) { methodPtr(c, cloud, "GetTaskList").Callers = []string{"typo"} }},
		{"wildcard caller", func(c *Catalog) { methodPtr(c, cloud, "GetTaskList").Callers = []string{"*"} }},
		{"duplicate caller", func(c *Catalog) { methodPtr(c, cloud, "GetTaskList").Callers = []string{"monitor", "monitor"} }},
		{"operator written as caller", func(c *Catalog) { methodPtr(c, cloud, "GetTaskList").Callers = []string{"console"} }},
		{"access written as caller", func(c *Catalog) { methodPtr(c, cloud, "GetTaskList").Callers = []string{"access"} }},
		{"host gateway instance as caller", func(c *Catalog) {
			methodPtr(c, GatewayControlPath, "PullSnapshot").Callers = []string{"host-gateway@*"}
		}},
		{"unknown operator", func(c *Catalog) { methodPtr(c, cloud, "GetTaskList").Operators = operators("monitor") }},
		{"duplicate operator", func(c *Catalog) { methodPtr(c, cloud, "GetTaskList").Operators = operators("console", "console") }},
		{"invalid method name", func(c *Catalog) { servicePtr(c, cloud).Methods[0].Name = "*" }},
		{"duplicate method", func(c *Catalog) {
			service := servicePtr(c, cloud)
			service.Methods = append(service.Methods, service.Methods[0])
		}},
		{"negative method timeout", func(c *Catalog) { methodPtr(c, cloud, "GetTaskList").TimeoutMS = -1 }},
		{"method timeout above the limit", func(c *Catalog) { methodPtr(c, cloud, "GetTaskList").TimeoutMS = 960001 }},
		{"read only and idempotent", func(c *Catalog) {
			method := methodPtr(c, cloud, "GetTaskList")
			method.ReadOnly, method.Idempotent = true, true
		}},
		{"duplicate service", func(c *Catalog) {
			componentPtr(c, "collector").Services = append(componentPtr(c, "collector").Services, *servicePtr(c, cloud))
		}},
		{"console without permission", func(c *Catalog) {
			for i := range servicePtr(c, cloud).Methods {
				servicePtr(c, cloud).Methods[i].Operators = operators()
			}
		}},
		{"ambiguous console method", func(c *Catalog) { servicePtr(c, "trpc.moox.admin.SpaceMgr").ConsoleName = "storage" }},
		{"duplicate identity", func(c *Catalog) { c.Identities = append(c.Identities, c.Identities[0]) }},
		{"identity is not external", func(c *Catalog) { c.Identities[0].External = false }},
		{"identity without methods", func(c *Catalog) { c.Identities = append(c.Identities, Identity{ID: "orphan", External: true}) }},
		{"identity shares a regular component ID", func(c *Catalog) { c.Identities = append(c.Identities, Identity{ID: "monitor", External: true}) }},
		{"identity uses an operator ID", func(c *Catalog) { c.Identities = append(c.Identities, Identity{ID: "moox-cli", External: true}) }},
		{"host gateway on business service", func(c *Catalog) { methodPtr(c, cloud, "GetTaskList").Callers = []string{"host-gateway"} }},
		{"unsafe doctor path", func(c *Catalog) { c.Components[0].Doctor.WritablePaths = []string{"../outside"} }},
		{"unknown dependency", func(c *Catalog) { c.Components[0].Doctor.Dependencies = []string{"unknown"} }},
		{"regular component shares an external principal ID", func(c *Catalog) { componentPtr(c, "factor-engine").ExternalProgram = false }},
		{"external program without its identity", func(c *Catalog) {
			c.Identities = slices.DeleteFunc(c.Identities, func(identity Identity) bool { return identity.ID == "factor-engine" })
			for i := range c.Components {
				for j := range c.Components[i].Services {
					for k := range c.Components[i].Services[j].Methods {
						method := &c.Components[i].Services[j].Methods[k]
						method.Callers = slices.DeleteFunc(method.Callers, func(caller string) bool { return caller == "factor-engine" })
					}
				}
			}
		}},
		{"external program declares a port", func(c *Catalog) { componentPtr(c, "archive-worker").Ports = []int{11999} }},
		{"external program service reuses its health port", func(c *Catalog) {
			componentPtr(c, "factor-engine").Services[0].Port = componentPtr(c, "factor-engine").Health.Port
		}},
		{"external program without a health port", func(c *Catalog) { componentPtr(c, "archive-worker").Health = Health{} }},
		{"relay_http on a regular component", func(c *Catalog) {
			componentPtr(c, "monitor").RelayHTTP = &RelayHTTP{Port: 11998, Callers: []string{"console"}}
		}},
		{"relay_http without callers", func(c *Catalog) { componentPtr(c, "archive-worker").RelayHTTP.Callers = nil }},
		{"relay_http unknown caller", func(c *Catalog) { componentPtr(c, "archive-worker").RelayHTTP.Callers = []string{"typo"} }},
		{"relay_http external caller", func(c *Catalog) {
			componentPtr(c, "archive-worker").RelayHTTP.Callers = []string{"factor-engine"}
		}},
		{"relay_http host gateway caller", func(c *Catalog) {
			componentPtr(c, "archive-worker").RelayHTTP.Callers = []string{"host-gateway@*"}
		}},
		{"relay_http duplicate caller", func(c *Catalog) {
			componentPtr(c, "archive-worker").RelayHTTP.Callers = []string{"console", "console"}
		}},
		{"relay_http shares its health port", func(c *Catalog) { componentPtr(c, "archive-worker").RelayHTTP.Port = 11418 }},
		{"relay_http uses another component's health port", func(c *Catalog) {
			componentPtr(c, "archive-worker").RelayHTTP.Port = componentPtr(c, "factor-engine").Health.Port
		}},
		{"relay_http uses the gateway remote port", func(c *Catalog) {
			componentPtr(c, "archive-worker").RelayHTTP.Port = HostGatewayRemotePort
		}},
		{"relay_http port out of range", func(c *Catalog) { componentPtr(c, "archive-worker").RelayHTTP.Port = 70000 }},
		{"external program outside systemd", func(c *Catalog) { componentPtr(c, "factor-engine").Doctor.Supervisor = SupervisorMooxRuntime }},
		{"systemd unit is missing", func(c *Catalog) { componentPtr(c, "factor-engine").Doctor.Unit = "" }},
		{"systemd unit is malformed", func(c *Catalog) { componentPtr(c, "factor-engine").Doctor.Unit = "../factor" }},
		{"unit without systemd", func(c *Catalog) { componentPtr(c, "monitor").Doctor.Unit = "moox-monitor" }},
		{"unknown supervisor", func(c *Catalog) { componentPtr(c, "monitor").Doctor.Supervisor = "launchd" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := mustCatalog(t)
			tc.mutate(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("invalid catalog accepted")
			}
		})
	}
}

func TestACLSeparatesConsoleMachinesAndExternalPrincipals(t *testing.T) {
	c := mustCatalog(t)
	cases := []struct {
		caller, path, method string
		want                 bool
	}{
		{"console", "trpc.moox.collector.CollectMgr", "DeleteCloudFunctions", false},
		{"moox-cli", "trpc.moox.collector.CollectMgr", "DeleteCloudFunctions", true},
		{"console", "trpc.moox.ops.SecretMgr", "GetSecretValue", false},
		{"trade", "trpc.moox.ops.SecretMgr", "GetSecretValue", true},
		{"console", "trpc.moox.ops.SysDeploy", "SyncHostDeployments", false},
		{"moox-cli", "trpc.moox.ops.SysDeploy", "SyncHostDeployments", true},
		{"monitor", "trpc.moox.collector.CollectMgr", "GetTaskResultInventory", true},
		{"monitor", "trpc.moox.collector.CollectMgr", "GetTaskList", false},
		{"monitor", "trpc.moox.collector.CollectMgr", "CreateTask", false},
		{"monitor", "trpc.moox.collector.CollectMgr", "UpdateTask", false},
		{"monitor", "trpc.moox.collector.CollectMgr", "DisableTask", false},
		{"monitor", "trpc.moox.collector.CollectMgr", "DeleteTask", false},
		{"monitor", "trpc.moox.storage.PrimaryStore", "UpsertFields", false},
		{"monitor", "trpc.moox.storage.PrimaryStore", "CommitTimeSeriesBatch", false},
		{"monitor", "trpc.moox.ops.SysDeploy", "ListHosts", true},
		{"monitor", "trpc.moox.ops.SysDeploy", "ListDeployments", true},
		{"monitor", "trpc.moox.ops.SysDeploy", "GetHostRoutes", true},
		{"monitor", "trpc.moox.ops.SysDeploy", "SetHostStatus", false},
		{"monitor", "trpc.moox.ops.SysDeploy", "SetDeploymentStatus", false},
		{"monitor", "trpc.moox.ops.SysDeploy", "SyncHostDeployments", false},
		{"strategy", "trpc.moox.factor.EngineControl", "OpenReplay", true},
		{"strategy", "trpc.moox.factor.EngineControl", "StepReplay", true},
		{"strategy", "trpc.moox.factor.EngineControl", "CloseReplay", true},
		{"console", "trpc.moox.factor.EngineControl", "OpenReplay", false},
		{"moox-cli", "trpc.moox.factor.EngineControl", "StepReplay", false},
		{"monitor", "trpc.moox.factor.EngineControl", "CloseReplay", false},
		{"access", "trpc.moox.factor.EngineControl", "OpenReplay", false},
		{"strategy", "trpc.moox.trade.TradeConsoleService", "GetLogicalAccount", true},
		{"strategy", "trpc.moox.trade.TradeConsoleService", "SubmitOrder", false},
		{"strategy", "trpc.moox.trade.TradeConsoleService", "PauseLogicalAccount", false},
		{"strategy", "trpc.moox.trade.TradeConsoleService", "CloseTargetSession", true},
		{"console", "trpc.moox.trade.TradeConsoleService", "CloseTargetSession", false},
		{"moox-cli", "trpc.moox.trade.TradeConsoleService", "CloseTargetSession", false},
		// 确认实盘与影子目标（批 4 T4）：人操作的两个身份都能调，机器身份都不能。
		{"console", "trpc.moox.strategy.StrategyMgr", "ConfirmStrategyInstance", true},
		{"moox-cli", "trpc.moox.strategy.StrategyMgr", "ConfirmStrategyInstance", true},
		{"strategy", "trpc.moox.strategy.StrategyMgr", "ConfirmStrategyInstance", false},
		{"console", "trpc.moox.trade.TradeConsoleService", "ListShadowTargets", true},
		{"moox-cli", "trpc.moox.trade.TradeConsoleService", "ListShadowTargets", true},
		{"strategy", "trpc.moox.trade.TradeConsoleService", "ListShadowTargets", false},
		{"scf-collector", "trpc.moox.storage.PrimaryStore", "CommitTimeSeriesBatch", false},
		{"access", "trpc.moox.storage.PrimaryStore", "CommitTimeSeriesBatch", true},
		{"access", "trpc.moox.storage.Metadata", "DeleteDataset", false},
		{"factor-engine", "trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows", false},
		{"access", "trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows", true},
		{"host-gateway@control", GatewayControlPath, "PullSnapshot", true},
		{"host-gateway", GatewayControlPath, "PullSnapshot", false},
		{"host-gateway@*", GatewayControlPath, "PullSnapshot", false},
		{"host-gateway@../escape", GatewayControlPath, "PullSnapshot", false},
		{"console", GatewayControlPath, "PullSnapshot", false},
		{"console", "trpc.moox.collector.CollectMgr", "Unknown", false},
		{"console", "trpc.moox.unknown.Service", "Get", false},
	}
	for _, tc := range cases {
		if got := c.Allowed(tc.caller, tc.path, tc.method); got != tc.want {
			t.Errorf("Allowed(%s,%s,%s)=%t", tc.caller, tc.path, tc.method, got)
		}
	}
}

func TestExternalPrincipalWhitelistIsExact(t *testing.T) {
	c := mustCatalog(t)
	want := map[string]map[string][]string{
		"scf-collector":      {"trpc.moox.storage.PrimaryStore": {"CommitTimeSeriesBatch", "UpsertFields"}},
		"factor-engine":      {"trpc.moox.storage.PrimaryStore": {"ReadTimeSeriesRows", "ReadDatasetChanges", "UpsertFields", "ReportDatasetPeriodCompleted", "GetDatasetPeriodCompleted"}, "trpc.moox.storage.Metadata": {"GetDataset", "ListDatasetColumns", "ListDatasetSubjects"}, "trpc.moox.factor.FactorEngine": {"SyncEngineCatalog", "EngineHeartbeat", "PullRecalcJob", "ReportRecalcProgress", "GetTaskParams", "GetUdfModule"}},
		"archive-worker":     {"trpc.moox.storage.PrimaryStore": {"ReadTimeSeriesRows", "ReadDatasetChanges"}, "trpc.moox.storage.Metadata": {"GetDataset", "ListDatasetColumns"}, "trpc.moox.archive.ArchiveWorker": {"ArchiveWorkerHeartbeat", "PullArchiveRun", "ReportArchiveRunProgress", "CompleteArchiveRun"}},
		"moox-skill":         {"trpc.moox.storage.PrimaryStore": {"ReadTimeSeriesRows"}},
		"collector-operator": {"trpc.moox.collector.CollectMgr": {"SetSubjectExclusions", "ClearSubjectExclusions", "ListSubjectExclusions", "SyncSubjects"}},
	}
	if !slices.Equal(c.ExternalIdentities(), []string{"scf-collector", "factor-engine", "archive-worker", "collector-operator", "moox-skill"}) {
		t.Fatalf("unexpected external identities %v", c.ExternalIdentities())
	}
	for _, principal := range c.ExternalIdentities() {
		got := map[string][]string{}
		for _, grant := range c.Grants(principal) {
			got[grant.Service] = slices.Sorted(slices.Values(grant.Methods))
		}
		expected := map[string][]string{}
		for service, methods := range want[principal] {
			expected[service] = slices.Sorted(slices.Values(methods))
		}
		if !reflect.DeepEqual(got, expected) {
			t.Errorf("unexpected whitelist for %s: %v", principal, got)
		}
		for _, component := range c.Components {
			for _, service := range component.Services {
				for _, method := range service.MethodNames() {
					allowed := slices.Contains(want[principal][service.Path], method)
					if c.PrincipalAllowed(principal, service.Path, method) != allowed {
						t.Fatalf("whitelist mismatch %s %s/%s", principal, service.Path, method)
					}
				}
			}
		}
	}
	if c.PrincipalAllowed("unknown", "trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows") {
		t.Fatal("unknown principal accepted")
	}
}

func TestConsoleResolvesStorageByNameAndMethod(t *testing.T) {
	c := mustCatalog(t)
	for method, want := range map[string]string{"GetDataset": "trpc.moox.storage.Metadata", "UpsertFields": "trpc.moox.storage.PrimaryStore", "QueryTimeSeriesRows": "trpc.moox.storage.DataView"} {
		service, ok := c.ConsoleService("storage", method)
		if !ok || service.Path != want {
			t.Errorf("storage/%s = %s", method, service.Path)
		}
	}
	for _, route := range [][2]string{{"secret", "GetSecretValue"}, {"collector", "DeleteCloudFunctions"}, {"trpc.moox.ops.SecretMgr", "GetSecretValue"}, {"storage", "Unknown"}, {"", "GetDataset"}} {
		if _, ok := c.ConsoleService(route[0], route[1]); ok {
			t.Fatalf("forbidden console route %v", route)
		}
	}
}

func TestCatalogMethodsMatchExistingProtos(t *testing.T) {
	// Stage A declares the next protocol before stages A6/B2 implement it.
	// All other methods must match the current service definitions exactly.
	planned := map[string]bool{"trpc.moox.ops.SysDeploy": true}
	files, err := filepath.Glob("../../modules/*/proto/*.proto")
	if err != nil {
		t.Fatal(err)
	}
	packageRE := regexp.MustCompile(`(?m)^package\s+(\S+);`)
	serviceRE := regexp.MustCompile(`(?ms)\bservice\s+(\w+)\s*\{(.*?)^\}`)
	methodRE := regexp.MustCompile(`\brpc\s+(\w+)\s*\(`)
	actual := map[string][]string{}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		pkg := packageRE.FindSubmatch(raw)
		if len(pkg) != 2 {
			continue
		}
		for _, service := range serviceRE.FindAllSubmatch(raw, -1) {
			methods := []string{}
			for _, method := range methodRE.FindAllSubmatch(service[2], -1) {
				methods = append(methods, string(method[1]))
			}
			actual[string(pkg[1])+"."+string(service[1])] = methods
		}
	}
	for _, component := range mustCatalog(t).Components {
		for _, service := range component.Services {
			if planned[service.Path] {
				continue
			}
			if !slices.Equal(service.MethodNames(), actual[service.Path]) {
				t.Errorf("catalog method drift for %s", service.Path)
			}
		}
	}
}

func jsonBytes(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestReadOnlyMethodsDoNotIncludeWrites(t *testing.T) {
	c := mustCatalog(t)
	for _, tc := range []struct {
		path, method string
		want         bool
	}{
		{"trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows", true},
		{"trpc.moox.storage.Metadata", "ListDatasetColumns", true},
		{"trpc.moox.ops.SecretMgr", "GetSecretValue", true},
		{"trpc.moox.storage.PrimaryStore", "CommitTimeSeriesBatch", false},
		{"trpc.moox.trade.TradeConsoleService", "ListShadowTargets", true},
		{"trpc.moox.strategy.StrategyMgr", "ConfirmStrategyInstance", false},
		{"trpc.moox.storage.PrimaryStore", "ReportDatasetPeriodCompleted", false},
		{"trpc.moox.factor.FactorEngine", "PullRecalcJob", false},
		{"trpc.moox.egress.Proxy", "Do", false},
		{GatewayControlPath, "ReportStatus", false},
		{"trpc.moox.unknown.Service", "GetValue", false},
	} {
		if c.ReadOnly(tc.path, tc.method) != tc.want {
			t.Errorf("unsafe retry classification for %s/%s", tc.path, tc.method)
		}
	}
}

// 请求可能已发出的网络失败后可以重发的方法：只读方法，以及显式声明幂等的写方法；其余写方法一律不重发。
func TestOnlyReadOnlyAndDeclaredIdempotentMethodsAreRetryable(t *testing.T) {
	c := mustCatalog(t)
	for _, tc := range []struct {
		path, method string
		want         bool
	}{
		{"trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows", true},
		{"trpc.moox.storage.PrimaryStore", "CommitTimeSeriesBatch", true},
		{"trpc.moox.storage.PrimaryStore", "UpsertFields", false},
		{"trpc.moox.storage.PrimaryStore", "ReportDatasetPeriodCompleted", false},
		{"trpc.moox.factor.FactorEngine", "PullRecalcJob", false},
		{"trpc.moox.unknown.Service", "CommitTimeSeriesBatch", false},
	} {
		if c.Retryable(tc.path, tc.method) != tc.want {
			t.Errorf("retry classification for %s/%s = %v, want %v", tc.path, tc.method, !tc.want, tc.want)
		}
	}
	if c.ReadOnly("trpc.moox.storage.PrimaryStore", "CommitTimeSeriesBatch") {
		t.Error("an idempotent write must not be classified read-only")
	}
}

func TestFactorInternalStorageMethodsHaveCompleteAndBoundedACL(t *testing.T) {
	c := mustCatalog(t)
	for service, methods := range map[string][]string{
		"PrimaryStore": {"ReadTimeSeriesRows", "UpsertFields", "ReportDatasetPeriodCompleted", "GetDatasetPeriodCompleted"},
		"Metadata":     {"GetDataset", "ListDatasetColumns", "CreateDataset", "UpsertDatasetColumn", "DeleteDataset", "UpdateDataset", "ListDatasetSubjects"},
	} {
		for _, method := range methods {
			if !c.Allowed("factor-mgr", "trpc.moox.storage."+service, method) {
				t.Errorf("Factor internal method is denied: %s/%s", service, method)
			}
			if c.Allowed("factor-engine", "trpc.moox.storage."+service, method) {
				t.Errorf("external engine may not authenticate directly to host gateways: %s/%s", service, method)
			}
		}
	}
	for service, methods := range map[string][]string{
		"PrimaryStore": {"CommitTimeSeriesBatch"},
		"Metadata":     {"CreateSpace", "DeleteSpace"},
	} {
		for _, method := range methods {
			if c.Allowed("factor-mgr", "trpc.moox.storage."+service, method) {
				t.Errorf("unrelated Factor write permission: %s/%s", service, method)
			}
		}
	}
}

// 外部程序组件：以同名外部身份经 Access 访问系统，自己不能直接调任何方法；可以声明只经网关路由的服务（因子引擎的
// EngineControl），不能声明其他端口。
func TestExternalProgramComponents(t *testing.T) {
	c := mustCatalog(t)
	for _, id := range []string{"factor-engine", "archive-worker"} {
		component, ok := c.Component(id)
		if !ok || !component.ExternalProgram || component.Doctor.Supervisor != SupervisorSystemdUser || component.Doctor.Unit != "moox-"+id {
			t.Fatalf("external program %s is not registered correctly: %+v", id, component)
		}
		if len(component.Ports) != 0 || component.Health.Port == 0 {
			t.Fatalf("external program %s must not own extra ports", id)
		}
		if !c.ExternalIdentity(id) || len(c.Grants(id)) == 0 {
			t.Fatalf("%s must be an external identity with grants", id)
		}
		for _, other := range c.Components {
			for _, service := range other.Services {
				for _, method := range service.MethodNames() {
					if c.Allowed(id, service.Path, method) {
						t.Fatalf("%s is an internal caller of %s/%s", id, service.Path, method)
					}
				}
			}
		}
	}
	// 因子引擎只有 EngineControl 这一个服务：本机端口 11423，服务级超时 120 秒、OpenReplay 10 分钟，包体上限用默认值；
	// 回放只给策略服务调，生命周期、任务检查与本地写入只给 moox-cli（管理台不能调）。
	engine, _ := c.Component("factor-engine")
	if len(engine.Services) != 1 {
		t.Fatalf("factor-engine services = %+v", engine.Services)
	}
	control := engine.Services[0]
	if control.Path != "trpc.moox.factor.EngineControl" || control.Port != 11423 || control.MaxBodyBytes != 0 || control.ConsoleName != "" ||
		control.TimeoutFor("OpenReplay") != 600000 || control.TimeoutFor("StepReplay") != 120000 || control.TimeoutFor("CloseReplay") != 120000 ||
		!slices.Equal(control.MethodNames(), []string{"OpenReplay", "StepReplay", "CloseReplay", "GetLifecycle", "ControlLifecycle", "CheckTask", "WriteSeries", "GetSeriesFields", "CheckSeriesSubject", "GetSeriesBounds"}) {
		t.Fatalf("EngineControl = %+v", control)
	}
	for _, method := range control.MethodNames() {
		replay := strings.HasSuffix(method, "Replay")
		if c.Allowed("strategy", control.Path, method) != replay || c.Allowed(OperatorMooxCLI, control.Path, method) == replay || c.Allowed(OperatorConsole, control.Path, method) {
			t.Fatalf("EngineControl/%s ACL: strategy=%t moox-cli=%t console=%t", method, c.Allowed("strategy", control.Path, method), c.Allowed(OperatorMooxCLI, control.Path, method), c.Allowed(OperatorConsole, control.Path, method))
		}
	}
	if !slices.Contains(engine.AllPorts(), 11423) || c.LocalRelayPort(11423) {
		t.Fatalf("EngineControl 的端口计入组件端口，但不能直接中转: %v", engine.AllPorts())
	}
	if worker, _ := c.Component("archive-worker"); len(worker.Services) != 0 {
		t.Fatalf("archive-worker services = %+v", worker.Services)
	}
	// 归档 worker 的数据端口只给控制台，计入它占用的端口。
	worker, _ := c.Component("archive-worker")
	if worker.RelayHTTP == nil || worker.RelayHTTP.Port != 11428 || !slices.Equal(worker.RelayHTTP.Callers, []string{"console"}) {
		t.Fatalf("archive-worker relay_http = %+v", worker.RelayHTTP)
	}
	if !slices.Contains(worker.AllPorts(), 11428) || !slices.Contains(worker.AllPorts(), 11418) {
		t.Fatalf("archive-worker ports = %v", worker.AllPorts())
	}
	if !c.LocalRelayPort(11428) || c.LocalRelayPort(11418) || c.LocalRelayPort(22) {
		t.Fatal("local relay ports must be relay_http ports only: health ports are not relayed")
	}
	if err := ValidateSupervisor("", ""); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSupervisor(SupervisorSystemdUser, "moox-factor-engine"); err != nil {
		t.Fatal(err)
	}
}

// 部署页和控制台只读：启停、主机登记与归档导入只留给 moox-cli。
func TestConsoleCannotMutateDeploymentState(t *testing.T) {
	c := mustCatalog(t)
	for _, tc := range []struct{ path, method string }{
		{"trpc.moox.ops.SysDeploy", "SetHostStatus"},
		{"trpc.moox.ops.SysDeploy", "SetDeploymentStatus"},
		{"trpc.moox.ops.SysDeploy", "SyncHostDeployments"},
		{"trpc.moox.ops.SysDeploy", "DeleteHost"},
		{"trpc.moox.archive.ArchiveMgr", "PrepareArchiveImport"},
		{"trpc.moox.archive.ArchiveMgr", "ImportArchiveFile"},
		{"trpc.moox.archive.ArchiveMgr", "ClearArchiveUnrecoverable"},
	} {
		if c.Allowed("console", tc.path, tc.method) {
			t.Fatalf("console can call %s/%s", tc.path, tc.method)
		}
		if !c.Allowed("moox-cli", tc.path, tc.method) {
			t.Fatalf("moox-cli cannot call %s/%s", tc.path, tc.method)
		}
	}
	for _, tc := range []struct{ path, method string }{
		{"trpc.moox.ops.SysDeploy", "ListHosts"},
		{"trpc.moox.ops.SysDeploy", "ListDeployments"},
		{"trpc.moox.storage.Metadata", "ListSpaces"},
	} {
		if !c.Allowed("console", tc.path, tc.method) {
			t.Fatalf("console lost read access to %s/%s", tc.path, tc.method)
		}
	}
}

func TestMissingGrantsNamesTheProvidingComponent(t *testing.T) {
	catalog, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	all := make([]string, 0, len(catalog.Components))
	for _, component := range catalog.Components {
		all = append(all, component.ID)
	}
	if missing := catalog.MissingGrants(all); len(missing) != 0 {
		t.Fatalf("全部部署时不应缺项: %+v", missing)
	}
	without := slices.DeleteFunc(slices.Clone(all), func(id string) bool { return id == "archive-mgr" })
	missing := catalog.MissingGrants(without)
	if len(missing) != 1 || missing[0].Component != "archive-mgr" || missing[0].Service != "trpc.moox.archive.ArchiveWorker" {
		t.Fatalf("应指出缺 archive-mgr: %+v", missing)
	}
}

// 模块生命周期（设计 3.7）：六个模块的服务都有 GetLifecycle、ControlLifecycle，读给管理台与 moox-cli、写只给 moox-cli，没有机器调用方；
// ControlLifecycle 要等在途操作结束（采集最长 5 分钟，策略、Trade、因子引擎最长 30 秒），所以路由超时与模块的 tRPC 服务端超时
// 都不能短于收敛时限，否则暂停会被网关或服务端先截断（H4）。
func TestLifecycleMethodsAndTimeoutsCoverQuiesce(t *testing.T) {
	c := mustCatalog(t)
	quiesce := map[string]int64{
		"trpc.moox.collector.CollectMgr": 300000, "trpc.moox.strategy.StrategyMgr": 30000, "trpc.moox.trade.TradeConsoleService": 30000,
		"trpc.moox.factor.EngineControl": 30000, "trpc.moox.factor.FactorMgr": 0, "trpc.moox.archive.ArchiveMgr": 0,
	}
	serverTimeouts := map[string]int64{}
	files, err := filepath.Glob("../../modules/*/config/trpc_go*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Server struct {
				Timeout int64 `yaml:"timeout"`
				Service []struct {
					Name    string `yaml:"name"`
					Timeout int64  `yaml:"timeout"`
				} `yaml:"service"`
			} `yaml:"server"`
		}
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		for _, service := range doc.Server.Service {
			timeout := service.Timeout
			if timeout == 0 {
				timeout = doc.Server.Timeout
			}
			serverTimeouts[service.Name] = timeout
		}
	}
	for path, wait := range quiesce {
		service, ok := c.Service(path)
		if !ok {
			t.Fatalf("服务目录没有 %s", path)
		}
		get, okGet := service.Method("GetLifecycle")
		control, okControl := service.Method("ControlLifecycle")
		if !okGet || !okControl || !get.ReadOnly || control.ReadOnly || control.Idempotent || len(get.Callers) != 0 || len(control.Callers) != 0 {
			t.Fatalf("%s 的生命周期方法登记不对：%+v %+v", path, get, control)
		}
		if !slices.Equal(control.OperatorList(), []string{OperatorMooxCLI}) {
			t.Fatalf("%s/ControlLifecycle 只给 moox-cli：%v", path, control.OperatorList())
		}
		wantRead := []string{OperatorConsole, OperatorMooxCLI}
		if service.ConsoleName == "" {
			wantRead = []string{OperatorMooxCLI}
		}
		if !slices.Equal(get.OperatorList(), wantRead) {
			t.Fatalf("%s/GetLifecycle 的读者：%v", path, get.OperatorList())
		}
		route := service.TimeoutFor("ControlLifecycle")
		if route <= wait {
			t.Fatalf("%s/ControlLifecycle 路由超时 %d 毫秒不长于收敛时限 %d 毫秒", path, route, wait)
		}
		if server := serverTimeouts[path]; server < route {
			t.Fatalf("%s 的 tRPC 服务端超时 %d 毫秒短于 ControlLifecycle 的路由超时 %d 毫秒", path, server, route)
		}
		// GetLifecycle 带 refresh_catchup 时同步算一次追赶进度（system resume 每次轮询都带），服务端超时同样不能更短。
		if read := service.TimeoutFor("GetLifecycle"); serverTimeouts[path] < read {
			t.Fatalf("%s 的 tRPC 服务端超时 %d 毫秒短于 GetLifecycle 的路由超时 %d 毫秒", path, serverTimeouts[path], read)
		}
	}
	// 采集的追赶进度要逐个空间把暂停区间跑一遍 K 线核对，与 CheckKline 同一量级：读取的路由超时不能短于它，
	// 否则暂停较长、空间较多时 system resume 在采集这一步整体报错退出。
	collect, _ := c.Service("trpc.moox.collector.CollectMgr")
	if read, check := collect.TimeoutFor("GetLifecycle"), collect.TimeoutFor("CheckKline"); read < check {
		t.Fatalf("CollectMgr/GetLifecycle 路由超时 %d 毫秒短于 CheckKline 的 %d 毫秒", read, check)
	}
}
