package servicecatalog

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"testing"
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

func TestEmbeddedCatalogDefinesEveryComponent(t *testing.T) {
	c := mustCatalog(t)
	want := map[string]int{"host-gateway": 11012, "host-agent": 11425, "console-proxy": 19528, "web-host": 19527, "admin": 11010, "eventbus": 11419, "monitor": 11409, "collector": 11412, "cloudnode": 11411, "factor-mgr": 11414, "strategy": 11431, "archive": 11416, "storage-primary": 20210, "storage-node": 20212, "storage-view": 20211, "access": 11014, "egress-proxy": 11441, "trade": 11210}
	if len(c.Components) != len(want) {
		t.Fatalf("components = %d", len(c.Components))
	}
	for _, component := range c.Components {
		if component.Health.Kind != "readyz" || component.Health.Port != want[component.ID] {
			t.Fatalf("incorrect health for %s", component.ID)
		}
		if component.Protected != slices.Contains([]string{"host-gateway", "console-proxy", "web-host", "admin"}, component.ID) {
			t.Fatalf("incorrect protection for %s", component.ID)
		}
		if (component.Replicas == Multi) != (component.ID == "access") {
			t.Fatalf("incorrect replicas for %s", component.ID)
		}
	}
	proxy, _ := c.Component("console-proxy")
	if proxy.Scope != ScopeControl || !proxy.Health.Loopback || !slices.Equal(proxy.Ports, []int{9527}) {
		t.Fatal("console proxy registration is incorrect")
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
	const cloud = "trpc.moox.cloudnode.CloudNodeMgr"
	cases := []struct {
		name   string
		mutate func(*Catalog)
	}{
		{"duplicate component", func(c *Catalog) { c.Components = append(c.Components, c.Components[0]) }},
		{"binary alias", func(c *Catalog) { componentPtr(c, "console-proxy").Binary = "caddy" }},
		{"invalid scope", func(c *Catalog) { c.Components[0].Scope = "arbitrary" }},
		{"invalid replicas", func(c *Catalog) { c.Components[0].Replicas = "unlimited" }},
		{"host multi", func(c *Catalog) { c.Components[0].Replicas = Multi }},
		{"missing health port", func(c *Catalog) { c.Components[0].Health.Port = 0 }},
		{"invalid health kind", func(c *Catalog) { c.Components[0].Health.Kind = "http" }},
		{"duplicate port", func(c *Catalog) { c.Components[0].Ports = append(c.Components[0].Ports, 11012) }},
		{"unknown caller", func(c *Catalog) { servicePtr(c, cloud).ACL[0].Callers = []string{"typo"} }},
		{"wildcard caller", func(c *Catalog) { servicePtr(c, cloud).ACL[0].Callers = []string{"*"} }},
		{"external caller bypass", func(c *Catalog) { servicePtr(c, cloud).ACL[0].Callers = []string{"scf-collector"} }},
		{"undeclared ACL method", func(c *Catalog) { servicePtr(c, cloud).ACL[0].Methods = []string{"Unregistered"} }},
		{"wildcard ACL method", func(c *Catalog) { servicePtr(c, cloud).ACL[0].Methods = []string{"*"} }},
		{"unknown read only method", func(c *Catalog) { servicePtr(c, cloud).ReadOnlyMethods = []string{"Unregistered"} }},
		{"duplicate service", func(c *Catalog) {
			componentPtr(c, "cloudnode").Services = append(componentPtr(c, "cloudnode").Services, *servicePtr(c, cloud))
		}},
		{"console without permission", func(c *Catalog) {
			servicePtr(c, cloud).ACL = []Grant{{Methods: []string{"CollectGarbage"}, Callers: []string{"admin"}}}
		}},
		{"ambiguous console method", func(c *Catalog) { servicePtr(c, "trpc.moox.ops.Ssh").ConsoleName = "sysdeploy" }},
		{"unknown principal method", func(c *Catalog) { c.Principals[0].Allow[0].Methods = []string{"Unregistered"} }},
		{"unknown principal service", func(c *Catalog) { c.Principals[0].Allow[0].Service = "trpc.moox.typo.Service" }},
		{"duplicate principal", func(c *Catalog) { c.Principals = append(c.Principals, c.Principals[0]) }},
		{"host instance on business service", func(c *Catalog) { servicePtr(c, cloud).ACL[0].Callers = []string{"host-gateway@*"} }},
		{"unsafe doctor path", func(c *Catalog) { c.Components[0].Doctor.WritablePaths = []string{"../outside"} }},
		{"unknown dependency", func(c *Catalog) { c.Components[0].Doctor.Dependencies = []string{"unknown"} }},
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
		{"console", "trpc.moox.cloudnode.CloudNodeMgr", "CollectGarbage", false},
		{"admin", "trpc.moox.cloudnode.CloudNodeMgr", "CollectGarbage", true},
		{"console", "trpc.moox.ops.SecretMgr", "GetSecretValue", false},
		{"trade", "trpc.moox.ops.SecretMgr", "GetSecretValue", true},
		{"console", "trpc.moox.admin.CollectorPublishLease", "AcquireCollectorPublishLease", true},
		{"cloudnode", "trpc.moox.admin.CollectorPublishLease", "BeginCollectorPublishOperation", true},
		{"console", "trpc.moox.ops.SysDeploy", "SyncHostPlacements", false},
		{"moox-cli", "trpc.moox.ops.SysDeploy", "SyncHostPlacements", true},
		{"strategy", "trpc.moox.trade.TradeConsoleService", "GetLogicalAccount", true},
		{"strategy", "trpc.moox.trade.TradeConsoleService", "PlaceManualOrder", false},
		{"scf-collector", "trpc.moox.storage.PrimaryStore", "CommitTimeSeriesBatch", false},
		{"access", "trpc.moox.storage.PrimaryStore", "CommitTimeSeriesBatch", true},
		{"access", "trpc.moox.storage.Metadata", "DeleteDataset", false},
		{"access", "trpc.moox.storage.PrimaryStore", "DeleteDatasetRows", false},
		{"host-gateway@control", GatewayControlPath, "PullSnapshot", true},
		{"host-gateway@*", GatewayControlPath, "PullSnapshot", false},
		{"host-gateway@../escape", GatewayControlPath, "PullSnapshot", false},
		{"console", GatewayControlPath, "PullSnapshot", false},
		{"console", "trpc.moox.cloudnode.CloudNodeMgr", "Unknown", false},
		{"console", "trpc.moox.unknown.Service", "Get", false},
	}
	for _, method := range []string{"ValidateCollectorPublishLease", "BeginCollectorPublishOperation", "RenewCollectorPublishOperation", "EndCollectorPublishOperation"} {
		cases = append(cases, struct {
			caller, path, method string
			want                 bool
		}{"console", "trpc.moox.admin.CollectorPublishLease", method, false})
	}
	for _, tc := range cases {
		if got := c.Allowed(tc.caller, tc.path, tc.method); got != tc.want {
			t.Errorf("Allowed(%s,%s,%s)=%t", tc.caller, tc.path, tc.method, got)
		}
	}
}

func TestExternalPrincipalWhitelistIsExact(t *testing.T) {
	c := mustCatalog(t)
	want := map[string][]Permission{
		"scf-collector": {{Service: "trpc.moox.storage.PrimaryStore", Methods: []string{"EnsureDatasetPeriod", "CommitTimeSeriesBatch", "RecordDatasetPeriodFailures", "GetDatasetPeriodStatus"}}, {Service: "trpc.moox.collector.MarketFetchRuntime", Methods: []string{"ClaimTimerBatch"}}},
		"factor-engine": {{Service: "trpc.moox.storage.PrimaryStore", Methods: []string{"ReadTimeSeriesRows", "WriteFactorRows", "ReportFactorPeriodComputed", "GetFactorPeriodComputed"}}, {Service: "trpc.moox.storage.Metadata", Methods: []string{"GetDataset", "ListDatasetColumns", "ListDatasetSubjects"}}, {Service: "trpc.moox.factor.FactorEngine", Methods: []string{"SyncEngineCatalog", "EngineHeartbeat", "PullRecalcJob", "ReportRecalcProgress"}}},
		"moox-skill":    {{Service: "trpc.moox.storage.PrimaryStore", Methods: []string{"ReadTimeSeriesRows"}}},
	}
	if len(c.Principals) != len(want) {
		t.Fatal("unexpected principal")
	}
	for _, principal := range c.Principals {
		if !reflect.DeepEqual(principal.Allow, want[principal.ID]) {
			t.Errorf("unexpected whitelist for %s", principal.ID)
		}
		for _, component := range c.Components {
			for _, service := range component.Services {
				for _, method := range service.Methods {
					allowed := false
					for _, permission := range want[principal.ID] {
						allowed = allowed || permission.Service == service.Path && slices.Contains(permission.Methods, method)
					}
					if c.PrincipalAllowed(principal.ID, service.Path, method) != allowed {
						t.Fatalf("whitelist mismatch %s %s/%s", principal.ID, service.Path, method)
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
	for _, route := range [][2]string{{"secret", "GetSecretValue"}, {"cloudnode", "CollectGarbage"}, {"trpc.moox.ops.SecretMgr", "GetSecretValue"}, {"storage", "Unknown"}, {"", "GetDataset"}} {
		if _, ok := c.ConsoleService(route[0], route[1]); ok {
			t.Fatalf("forbidden console route %v", route)
		}
	}
}

func TestCatalogMethodsMatchExistingProtos(t *testing.T) {
	// Stage A declares the next protocol before stages A6/B2/E3 implement it.
	// All other methods must match the current service definitions exactly.
	planned := map[string]bool{"trpc.moox.ops.SysDeploy": true, "trpc.moox.egress.Proxy": true}
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
			if !slices.Equal(service.Methods, actual[service.Path]) {
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
		{"trpc.moox.storage.PrimaryStore", "EnsureDatasetPeriod", false},
		{"trpc.moox.storage.PrimaryStore", "CommitTimeSeriesBatch", false},
		{"trpc.moox.storage.PrimaryStore", "ReportFactorPeriodComputed", false},
		{"trpc.moox.collector.MarketFetchRuntime", "ClaimTimerBatch", false},
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
