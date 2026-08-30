package sysdeploy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestTRPCHealthAndAdminRPCServicesBindLoopback(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "..")
	matches, err := filepath.Glob(filepath.Join(root, "modules", "*", "config", "trpc_go*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no trpc configs found")
	}
	for _, path := range matches {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(raw), "\n")
		for i, line := range lines {
			if !strings.Contains(line, "- name:") || (!strings.Contains(line, ".Health") && !strings.Contains(line, "trpc.moox.infra.") && !strings.Contains(line, "trpc.moox.ops.") && !strings.Contains(line, "trpc.moox.admin.SpaceMgr")) {
				continue
			}
			end := i + 7
			if end > len(lines) {
				end = len(lines)
			}
			if !strings.Contains(strings.Join(lines[i:end], "\n"), "ip: 127.0.0.1") {
				t.Errorf("%s:%d lacks loopback ip: %s", path, i+1, strings.TrimSpace(line))
			}
		}
	}
}

func TestDefaultDeploymentsDefineCanonicalGatewayEndpoints(t *testing.T) {
	byName := map[string]Deployment{}
	for _, row := range DefaultDeployments(testAdminNodeID) {
		byName[row.ServiceName] = row
	}
	if _, ok := byName["service_gateway_internal"]; ok {
		t.Fatal("legacy internal service gateway row still exists")
	}
	for name, wantPort := range map[string]int32{"service_gateway": 11001, "service_gateway_native": 11003} {
		row, ok := byName[name]
		if !ok || row.Scope != "public" || row.Port != wantPort {
			t.Fatalf("%s deployment = %#v", name, row)
		}
	}
}

func TestDefaultDeploymentsIncludeMonitorHealthMetadata(t *testing.T) {
	rows := DefaultDeployments(testAdminNodeID)
	byName := make(map[string]Deployment, len(rows))
	for _, row := range rows {
		byName[row.ServiceName] = row
	}
	if _, ok := byName["monitor"]; ok {
		t.Fatal("legacy admin monitor deployment row still exists")
	}
	eventbus, ok := byName["eventbus"]
	if !ok {
		t.Fatal("eventbus deployment row missing")
	}
	if eventbus.GatewayPath != "trpc.moox.eventbus.EventBusMgr" || eventbus.Port != 11420 || healthURL(eventbus.ExtraConfig) != "http://127.0.0.1:11419/readyz" {
		t.Fatalf("eventbus deployment = %#v", eventbus)
	}
	monitor, ok := byName["moox_monitor"]
	if !ok {
		t.Fatal("moox_monitor deployment row missing")
	}
	if healthURL(monitor.ExtraConfig) != "http://127.0.0.1:11409/readyz" {
		t.Fatalf("moox_monitor extra_config = %s", monitor.ExtraConfig)
	}
	gateway, ok := byName["moox_gateway"]
	if !ok {
		t.Fatal("moox_gateway deployment row missing")
	}
	if gateway.Port != 11002 || healthURL(gateway.ExtraConfig) != "http://127.0.0.1:11012/readyz" {
		t.Fatalf("moox_gateway deployment = %#v", gateway)
	}
	if !strings.Contains(gateway.ExtraConfig, `"health_body_contains":"ready"`) {
		t.Fatalf("moox_gateway extra_config = %s", gateway.ExtraConfig)
	}
	if healthURL(byName["moox_cloudnode"].ExtraConfig) != "http://127.0.0.1:11411/readyz" {
		t.Fatalf("cloudnode extra_config = %s", byName["moox_cloudnode"].ExtraConfig)
	}
	var factorExtra struct {
		TimeoutMS int64 `json:"timeout_ms"`
	}
	if err := json.Unmarshal([]byte(byName["moox_factor"].ExtraConfig), &factorExtra); err != nil {
		t.Fatalf("unmarshal factor extra_config: %v", err)
	}
	if factorExtra.TimeoutMS != 120000 {
		t.Fatalf("factor gateway timeout = %d, want 120000", factorExtra.TimeoutMS)
	}
	var cloudNodeExtra struct {
		TimeoutMS int64 `json:"timeout_ms"`
	}
	if err := json.Unmarshal([]byte(byName["moox_cloudnode"].ExtraConfig), &cloudNodeExtra); err != nil {
		t.Fatalf("unmarshal cloudnode extra_config: %v", err)
	}
	if cloudNodeExtra.TimeoutMS != 120000 {
		t.Fatalf("cloudnode gateway timeout = %d, want 120000", cloudNodeExtra.TimeoutMS)
	}
	for name, want := range map[string]string{
		"moox_strategy":  "http://127.0.0.1:11431/readyz",
		"moox_archive":   "http://127.0.0.1:11416/readyz",
		"moox_hostagent": "http://127.0.0.1:11425/readyz",
		"moox_trade":     "http://127.0.0.1:11210/readyz",
	} {
		if healthURL(byName[name].ExtraConfig) != want {
			t.Fatalf("%s health URL = %s, want %s", name, healthURL(byName[name].ExtraConfig), want)
		}
	}
	if !monitorEnabled(byName["moox_strategy"].ExtraConfig) {
		t.Fatal("moox_strategy monitoring must be enabled after standard release integration")
	}
	if healthURL(byName["storage-primary"].ExtraConfig) != "http://127.0.0.1:20210/readyz" {
		t.Fatalf("storage-primary extra_config = %s", byName["storage-primary"].ExtraConfig)
	}
	if item := byName["storage-primary"]; item.Port != 20200 || item.GatewayPath != "trpc.moox.storage.Metadata" {
		t.Fatalf("storage-primary endpoint = %d/%s, want 20200/trpc.moox.storage.Metadata", item.Port, item.GatewayPath)
	}
	var storageExtra struct {
		GatewayMethods []string `json:"gateway_methods"`
		GatewayRoutes  []struct {
			ServicePath    string   `json:"service_path"`
			Port           int32    `json:"port"`
			GatewayMethods []string `json:"gateway_methods"`
			GatewayCallers []string `json:"gateway_callers"`
		} `json:"gateway_routes"`
	}
	if err := json.Unmarshal([]byte(byName["storage-primary"].ExtraConfig), &storageExtra); err != nil {
		t.Fatalf("unmarshal storage-primary extra_config: %v", err)
	}
	for _, method := range []string{"GetDataNode", "ListDataNodes", "UpdateDataNode", "DeleteDataNode", "CheckDatasetActivation", "ActivateDataset", "RebindDatasetDataNode", "RequestViewRebuild"} {
		if !containsString(storageExtra.GatewayMethods, method) {
			t.Fatalf("storage-primary gateway methods missing %s: %v", method, storageExtra.GatewayMethods)
		}
	}
	if containsString(storageExtra.GatewayMethods, "DeleteSpace") {
		t.Fatalf("storage-primary general gateway methods expose DeleteSpace: %v", storageExtra.GatewayMethods)
	}
	for _, method := range []string{
		"Register" + "DataNode",
		"Create" + "PrimaryStore" + "Node",
		"List" + "PrimaryStore" + "Nodes",
		"Create" + "PrimaryStore" + "Route",
		"List" + "PrimaryStore" + "Routes",
	} {
		if containsString(storageExtra.GatewayMethods, method) {
			t.Fatalf("storage-primary gateway methods must not include %s: %v", method, storageExtra.GatewayMethods)
		}
	}
	var metadataRoute, deleteSpaceRoute, primaryRoute, readTimeSeriesRoute *struct {
		ServicePath    string   `json:"service_path"`
		Port           int32    `json:"port"`
		GatewayMethods []string `json:"gateway_methods"`
		GatewayCallers []string `json:"gateway_callers"`
	}
	for i := range storageExtra.GatewayRoutes {
		if storageExtra.GatewayRoutes[i].ServicePath == "trpc.moox.storage.Metadata" &&
			reflect.DeepEqual(storageExtra.GatewayRoutes[i].GatewayMethods, []string{"DeleteSpace"}) {
			deleteSpaceRoute = &storageExtra.GatewayRoutes[i]
		}
		if storageExtra.GatewayRoutes[i].ServicePath == "trpc.moox.storage.Metadata" &&
			containsString(storageExtra.GatewayRoutes[i].GatewayMethods, "ClaimViewIndexBuild") {
			metadataRoute = &storageExtra.GatewayRoutes[i]
		}
		if storageExtra.GatewayRoutes[i].ServicePath == "trpc.moox.storage.PrimaryStore" &&
			reflect.DeepEqual(storageExtra.GatewayRoutes[i].GatewayMethods, []string{"ReadTimeSeriesRows"}) {
			readTimeSeriesRoute = &storageExtra.GatewayRoutes[i]
		}
		if storageExtra.GatewayRoutes[i].ServicePath == "trpc.moox.storage.PrimaryStore" &&
			containsString(storageExtra.GatewayRoutes[i].GatewayMethods, "UpsertFields") {
			primaryRoute = &storageExtra.GatewayRoutes[i]
		}
	}
	if deleteSpaceRoute == nil || deleteSpaceRoute.Port != 20100 ||
		!reflect.DeepEqual(deleteSpaceRoute.GatewayCallers, []string{"admin-gateway", "moox-cli"}) {
		t.Fatalf("DeleteSpace gateway route = %+v", deleteSpaceRoute)
	}
	if metadataRoute == nil || metadataRoute.Port != 20100 || !reflect.DeepEqual(metadataRoute.GatewayMethods, []string{"ClaimViewIndexBuild", "UpdateViewIndexBuild", "ActivateViewIndex", "FailViewIndexBuild", "CreateViewRebuildLog", "UpdateViewRebuildLog", "UpsertSkippedViewRebuildLog"}) || !reflect.DeepEqual(metadataRoute.GatewayCallers, []string{"storage-view"}) {
		t.Fatalf("storage-view metadata gateway route = %+v", metadataRoute)
	}
	if primaryRoute == nil || primaryRoute.Port != 20102 {
		t.Fatalf("storage-primary gateway route = %+v", primaryRoute)
	}
	if containsString(primaryRoute.GatewayMethods, "ReadTimeSeriesRows") ||
		containsString(primaryRoute.GatewayCallers, "moox-skill") {
		t.Fatalf("storage-primary general route exposes skill read access: %+v", primaryRoute)
	}
	if readTimeSeriesRoute == nil || readTimeSeriesRoute.Port != 20102 ||
		!reflect.DeepEqual(readTimeSeriesRoute.GatewayMethods, []string{"ReadTimeSeriesRows"}) ||
		!reflect.DeepEqual(readTimeSeriesRoute.GatewayCallers, []string{"admin-gateway", "collector", "factor", "monitor", "archive", "storage-view", "strategy", "moox-skill"}) {
		t.Fatalf("storage-primary ReadTimeSeriesRows route = %+v", readTimeSeriesRoute)
	}
	for _, method := range []string{"ReportDatasetPeriodCollected", "AppendDatasetSyncPoint", "WaitViewSyncPoint", "ReportFactorPeriodComputed", "GetFactorPeriodComputed"} {
		if !containsString(primaryRoute.GatewayMethods, method) {
			t.Fatalf("storage-primary gateway route missing %s: %v", method, primaryRoute.GatewayMethods)
		}
	}
	for i := range storageExtra.GatewayRoutes {
		if storageExtra.GatewayRoutes[i].ServicePath == "trpc.moox.storage.DataShard" {
			t.Fatalf("storage-primary must not embed DataShard gateway route")
		}
	}
	if healthURL(byName["storage-view"].ExtraConfig) != "http://127.0.0.1:20211/readyz" {
		t.Fatalf("storage-view extra_config = %s", byName["storage-view"].ExtraConfig)
	}
	for _, name := range []string{"storage_view_builder", "storage_view_query", "storage_view_index"} {
		if _, exists := byName[name]; exists {
			t.Fatalf("obsolete split View deployment %s must not be registered", name)
		}
	}
	for name, expected := range map[string]struct {
		port int32
		path string
	}{
		"trade_console": {11200, "trpc.moox.trade.TradeConsoleService"},
	} {
		row, ok := byName[name]
		if !ok || row.Port != expected.port || row.GatewayPath != expected.path ||
			row.Protocol != "http" || row.Host != "127.0.0.1" || row.Scope != "internal" {
			t.Fatalf("%s deployment = %+v", name, row)
		}
		if healthURL(row.ExtraConfig) != "" {
			t.Fatalf("%s should not default to local health_url: %s", name, row.ExtraConfig)
		}
	}
	for _, name := range []string{
		"trade_account", "trade_balance", "trade_fund", "trade_apikey",
		"trade_channel", "trade_tradeop", "trade_order", "trade_tradeq",
		"trade_position", "trade_rebalance", "trade_ops",
	} {
		if _, ok := byName[name]; ok {
			t.Fatalf("obsolete Trade deployment %s must not be registered", name)
		}
	}
}

func TestDefaultFactorGatewayRoutesSeparateReadAccess(t *testing.T) {
	for _, item := range DefaultDeployments(testAdminNodeID) {
		if item.ServiceName != "moox_factor" {
			continue
		}
		var extra struct {
			GatewayMethods []string `json:"gateway_methods"`
			GatewayCallers []string `json:"gateway_callers"`
			GatewayRoutes  []struct {
				GatewayMethods []string `json:"gateway_methods"`
				GatewayCallers []string `json:"gateway_callers"`
			} `json:"gateway_routes"`
		}
		if err := json.Unmarshal([]byte(item.ExtraConfig), &extra); err != nil {
			t.Fatal(err)
		}
		wantMethods := []string{
			"CreateFactor", "UpdateFactor", "SetFactorStatus", "DeleteFactor",
			"UpsertBinding", "DeleteBinding", "RecalcFactor", "GetEngineStatus",
		}
		if !reflect.DeepEqual(extra.GatewayMethods, wantMethods) ||
			!reflect.DeepEqual(extra.GatewayCallers, []string{"admin-gateway", "moox-cli"}) {
			t.Fatalf("moox_factor gateway contract = methods %v callers %v", extra.GatewayMethods, extra.GatewayCallers)
		}
		if len(extra.GatewayRoutes) != 1 || !reflect.DeepEqual(extra.GatewayRoutes[0].GatewayMethods, []string{"GetFactor", "ListFactors", "ListBindings"}) ||
			!reflect.DeepEqual(extra.GatewayRoutes[0].GatewayCallers, []string{"admin-gateway", "moox-cli", "strategy"}) {
			t.Fatalf("moox_factor read gateway contract = %+v", extra.GatewayRoutes)
		}
		return
	}
	t.Fatal("moox_factor deployment is missing")
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestGatewayRoutesMonitorAliasToIndependentService(t *testing.T) {
	if got := gatewayDeploymentName("monitor"); got != "moox_monitor" {
		t.Fatalf("gatewayDeploymentName(monitor) = %q, want moox_monitor", got)
	}
	for input, want := range map[string]string{"archive": "moox_archive", "hostagent": "moox_hostagent", "trade": "moox_trade"} {
		if got := gatewayDeploymentName(input); got != want {
			t.Fatalf("gatewayDeploymentName(%s) = %q, want %q", input, got, want)
		}
	}
}

func TestMergeDefaultExtraConfigBackfillsMissingFields(t *testing.T) {
	merged, changed := mergeDefaultExtraConfig(`{"monitor_enabled":false,"owner":"ops"}`, `{"health_url":"http://127.0.0.1:20210/healthz","monitor_enabled":true}`)
	if !changed {
		t.Fatal("changed = false, want true")
	}
	var extra map[string]interface{}
	if err := json.Unmarshal([]byte(merged), &extra); err != nil {
		t.Fatalf("unmarshal merged: %v", err)
	}
	if extra["health_url"] != "http://127.0.0.1:20210/healthz" {
		t.Fatalf("health_url not backfilled: %s", merged)
	}
	if extra["monitor_enabled"] != false {
		t.Fatalf("explicit monitor_enabled overwritten: %s", merged)
	}
	if extra["owner"] != "ops" {
		t.Fatalf("existing owner lost: %s", merged)
	}
}

func TestMergeDefaultExtraConfigBackfillsNewGatewayMethods(t *testing.T) {
	merged, changed := mergeDefaultExtraConfig(
		`{"gateway_methods":["GetView"],"gateway_callers":["admin-gateway"]}`,
		`{"gateway_methods":["GetView","ListViews"]}`,
	)
	if !changed {
		t.Fatal("changed = false, want ListViews to be added")
	}
	var extra struct {
		GatewayMethods []string `json:"gateway_methods"`
	}
	if err := json.Unmarshal([]byte(merged), &extra); err != nil {
		t.Fatalf("unmarshal merged: %v", err)
	}
	if !containsString(extra.GatewayMethods, "GetView") || !containsString(extra.GatewayMethods, "ListViews") {
		t.Fatalf("gateway methods = %v", extra.GatewayMethods)
	}
}

func TestMergeDefaultExtraConfigPreservesWildcardGatewayMethods(t *testing.T) {
	merged, changed := mergeDefaultExtraConfig(
		`{"gateway_methods":["*"]}`,
		`{"gateway_methods":["ListViews"]}`,
	)
	if changed {
		t.Fatal("wildcard gateway methods should not be rewritten")
	}
	if merged != `{"gateway_methods":["*"]}` {
		t.Fatalf("merged = %s", merged)
	}
}

func TestMergeDefaultExtraConfigPreservesUserGatewayRouteByIdentity(t *testing.T) {
	merged, changed := mergeDefaultExtraConfig(
		`{"gateway_routes":[{"service_path":"trpc.moox.storage.PrimaryStore","port":20102,"gateway_methods":["ReadFields"],"gateway_callers":["operator"],"owner":"ops"}]}`,
		`{"gateway_routes":[{"service_path":"trpc.moox.storage.PrimaryStore","port":20102,"gateway_methods":["ReadFields"],"gateway_callers":["admin-gateway"]},{"service_path":"trpc.moox.storage.PrimaryStore","port":20102,"gateway_methods":["ReadTimeSeriesRows"],"gateway_callers":["admin-gateway","collector","moox-skill"]}]}`,
	)
	if !changed {
		t.Fatal("changed = false, want missing route to be appended")
	}
	var extra struct {
		GatewayRoutes []map[string]any `json:"gateway_routes"`
	}
	if err := json.Unmarshal([]byte(merged), &extra); err != nil {
		t.Fatal(err)
	}
	if len(extra.GatewayRoutes) != 2 {
		t.Fatalf("gateway routes = %v, want preserved user route plus one default", extra.GatewayRoutes)
	}
	if extra.GatewayRoutes[0]["owner"] != "ops" || !reflect.DeepEqual(extra.GatewayRoutes[0]["gateway_callers"], []any{"operator"}) {
		t.Fatalf("user route was overwritten: %v", extra.GatewayRoutes[0])
	}
	if !reflect.DeepEqual(extra.GatewayRoutes[1]["gateway_callers"], []any{"moox-skill"}) {
		t.Fatalf("new read route callers = %v, want only moox-skill", extra.GatewayRoutes[1]["gateway_callers"])
	}
}

func TestMergeDefaultExtraConfigDoesNotRewriteUnrelatedServiceWithSameMethod(t *testing.T) {
	existing := `{"gateway_routes":[{"service_path":"trpc.example.Analytics","port":29999,"gateway_methods":["ReadTimeSeriesRows"],"gateway_callers":["operator"],"owner":"ops"}]}`
	merged, changed := mergeDefaultExtraConfig(
		existing,
		`{"gateway_routes":[{"service_path":"trpc.moox.storage.PrimaryStore","port":20102,"gateway_methods":["ReadTimeSeriesRows"],"gateway_callers":["moox-skill"]}]}`,
	)
	if !changed {
		t.Fatal("changed = false, want the independent PrimaryStore route to be added")
	}
	var extra struct {
		GatewayRoutes []map[string]any `json:"gateway_routes"`
	}
	if err := json.Unmarshal([]byte(merged), &extra); err != nil {
		t.Fatal(err)
	}
	if len(extra.GatewayRoutes) != 2 ||
		extra.GatewayRoutes[0]["service_path"] != "trpc.example.Analytics" ||
		extra.GatewayRoutes[0]["port"] != float64(29999) ||
		!reflect.DeepEqual(extra.GatewayRoutes[0]["gateway_callers"], []any{"operator"}) {
		t.Fatalf("unrelated route was rewritten: %v", extra.GatewayRoutes)
	}
	if extra.GatewayRoutes[1]["service_path"] != "trpc.moox.storage.PrimaryStore" {
		t.Fatalf("PrimaryStore route missing: %v", extra.GatewayRoutes)
	}
}

func TestMergeDefaultExtraConfigIgnoresUnrelatedWildcardRoute(t *testing.T) {
	existing := `{"gateway_routes":[{"service_path":"trpc.example.Analytics","port":29999,"gateway_methods":["*"],"gateway_callers":["operator"]},{"service_path":"trpc.moox.storage.PrimaryStore","port":20102,"gateway_methods":["UpsertFields","ReadTimeSeriesRows"],"gateway_callers":["admin-gateway"]}]}`
	merged, changed := mergeDefaultExtraConfig(
		existing,
		`{"gateway_routes":[{"service_path":"trpc.moox.storage.PrimaryStore","port":20102,"gateway_methods":["ReadTimeSeriesRows"],"gateway_callers":["moox-skill"]}]}`,
	)
	if !changed {
		t.Fatal("changed = false, want the PrimaryStore mixed route to be split")
	}
	var extra struct {
		GatewayRoutes []map[string]any `json:"gateway_routes"`
	}
	if err := json.Unmarshal([]byte(merged), &extra); err != nil {
		t.Fatal(err)
	}
	if len(extra.GatewayRoutes) != 3 || extra.GatewayRoutes[0]["service_path"] != "trpc.example.Analytics" {
		t.Fatalf("unrelated wildcard route was changed: %v", extra.GatewayRoutes)
	}
	var foundRead, foundGeneral bool
	for _, route := range extra.GatewayRoutes[1:] {
		methods, _ := route["gateway_methods"].([]any)
		if reflect.DeepEqual(methods, []any{"ReadTimeSeriesRows"}) {
			foundRead = reflect.DeepEqual(route["gateway_callers"], []any{"admin-gateway", "moox-skill"})
		}
		if reflect.DeepEqual(methods, []any{"UpsertFields"}) {
			foundGeneral = reflect.DeepEqual(route["gateway_callers"], []any{"admin-gateway"})
		}
	}
	if !foundRead || !foundGeneral {
		t.Fatalf("PrimaryStore routes were not split safely: %v", extra.GatewayRoutes)
	}
}

func TestMergeDefaultExtraConfigSeedsAllGatewayRoutesWhenExplicitlyEmpty(t *testing.T) {
	merged, changed := mergeDefaultExtraConfig(
		`{"gateway_routes":[]}`,
		`{"gateway_routes":[{"service_path":"trpc.moox.storage.Metadata","port":20100,"gateway_methods":["GetSpace"],"gateway_callers":["admin-gateway"]},{"service_path":"trpc.moox.storage.PrimaryStore","port":20102,"gateway_methods":["ReadTimeSeriesRows"],"gateway_callers":["moox-skill"]}]}`,
	)
	if !changed {
		t.Fatal("empty gateway_routes must receive fresh defaults")
	}
	var extra struct {
		GatewayRoutes []map[string]any `json:"gateway_routes"`
	}
	if err := json.Unmarshal([]byte(merged), &extra); err != nil {
		t.Fatal(err)
	}
	if len(extra.GatewayRoutes) != 2 {
		t.Fatalf("gateway routes = %v, want all fresh defaults", extra.GatewayRoutes)
	}
}

func TestMergeDefaultExtraConfigPreservesTightenedReadRouteCallers(t *testing.T) {
	merged, changed := mergeDefaultExtraConfig(
		`{"gateway_routes":[{"service_path":"trpc.moox.storage.PrimaryStore","port":20102,"gateway_methods":["ReadTimeSeriesRows"],"gateway_callers":["admin-gateway"],"owner":"ops"}]}`,
		`{"gateway_routes":[{"service_path":"trpc.moox.storage.PrimaryStore","port":20102,"gateway_methods":["ReadTimeSeriesRows"],"gateway_callers":["admin-gateway","collector","factor","moox-skill"]}]}`,
	)
	if changed {
		t.Fatal("operator-owned read callers must not be expanded")
	}
	var extra struct {
		GatewayRoutes []map[string]any `json:"gateway_routes"`
	}
	if err := json.Unmarshal([]byte(merged), &extra); err != nil {
		t.Fatal(err)
	}
	if len(extra.GatewayRoutes) != 1 ||
		!reflect.DeepEqual(extra.GatewayRoutes[0]["gateway_callers"], []any{"admin-gateway"}) ||
		extra.GatewayRoutes[0]["owner"] != "ops" {
		t.Fatalf("read route was expanded: %v", extra.GatewayRoutes)
	}
}

func TestMergeDefaultExtraConfigMigratesPrimaryWildcardToExplicitRoutes(t *testing.T) {
	existing := `{"gateway_routes":[{"service_path":"trpc.moox.storage.PrimaryStore","port":20102,"gateway_methods":["*"],"gateway_callers":["admin-gateway"],"owner":"ops"}]}`
	merged, changed := mergeDefaultExtraConfig(
		existing,
		`{"gateway_routes":[{"service_path":"trpc.moox.storage.PrimaryStore","port":20102,"gateway_methods":["UpsertFields"],"gateway_callers":["admin-gateway"]},{"service_path":"trpc.moox.storage.PrimaryStore","port":20102,"gateway_methods":["ReadTimeSeriesRows"],"gateway_callers":["moox-skill"]}]}`,
	)
	if !changed {
		t.Fatal("changed = false, want storage wildcard migration")
	}
	var extra struct {
		GatewayRoutes []map[string]any `json:"gateway_routes"`
	}
	if err := json.Unmarshal([]byte(merged), &extra); err != nil {
		t.Fatal(err)
	}
	if len(extra.GatewayRoutes) != 2 ||
		!reflect.DeepEqual(extra.GatewayRoutes[0]["gateway_methods"], []any{"UpsertFields"}) ||
		!reflect.DeepEqual(extra.GatewayRoutes[0]["gateway_callers"], []any{"admin-gateway"}) ||
		!reflect.DeepEqual(extra.GatewayRoutes[1]["gateway_methods"], []any{"ReadTimeSeriesRows"}) ||
		!reflect.DeepEqual(extra.GatewayRoutes[1]["gateway_callers"], []any{"admin-gateway", "moox-skill"}) {
		t.Fatalf("storage wildcard route was not migrated to explicit ACLs: %v", extra.GatewayRoutes)
	}
}

func TestMergeDefaultExtraConfigPreservesExplicitSkillRemovalFromReadCallers(t *testing.T) {
	merged, changed := mergeDefaultExtraConfig(
		`{"gateway_routes":[{"service_path":"trpc.moox.storage.PrimaryStore","port":20102,"gateway_methods":["ReadTimeSeriesRows"],"gateway_callers":["admin-gateway","collector","factor"]}]}`,
		`{"gateway_routes":[{"service_path":"trpc.moox.storage.PrimaryStore","port":20102,"gateway_methods":["ReadTimeSeriesRows"],"gateway_callers":["admin-gateway","collector","factor","moox-skill"]}]}`,
	)
	if changed {
		t.Fatal("an existing dedicated read route must preserve explicit skill removal")
	}
	var extra struct {
		GatewayRoutes []map[string]any `json:"gateway_routes"`
	}
	if err := json.Unmarshal([]byte(merged), &extra); err != nil {
		t.Fatal(err)
	}
	if len(extra.GatewayRoutes) != 1 || !reflect.DeepEqual(extra.GatewayRoutes[0]["gateway_callers"], []any{"admin-gateway", "collector", "factor"}) {
		t.Fatalf("read callers were expanded = %v", extra.GatewayRoutes)
	}
}

func TestMergeDefaultExtraConfigMergesDuplicateReadRoutesAtNativeEndpoint(t *testing.T) {
	merged, changed := mergeDefaultExtraConfig(
		`{"gateway_routes":[{"service_path":"trpc.moox.storage.PrimaryStore","port":20201,"gateway_methods":["ReadTimeSeriesRows"],"gateway_callers":["admin-gateway"],"owner":"historical"},{"service_path":"trpc.moox.storage.PrimaryStore","port":20102,"gateway_methods":["ReadTimeSeriesRows"],"gateway_callers":["collector"],"timeout_ms":9000}]}`,
		`{"gateway_routes":[{"service_path":"trpc.moox.storage.PrimaryStore","port":20102,"gateway_methods":["ReadTimeSeriesRows"],"gateway_callers":["moox-skill"]}]}`,
	)
	if !changed {
		t.Fatal("duplicate read routes must be merged")
	}
	var extra struct {
		GatewayRoutes []map[string]any `json:"gateway_routes"`
	}
	if err := json.Unmarshal([]byte(merged), &extra); err != nil {
		t.Fatal(err)
	}
	if len(extra.GatewayRoutes) != 1 ||
		extra.GatewayRoutes[0]["port"] != float64(20102) ||
		extra.GatewayRoutes[0]["service_path"] != "trpc.moox.storage.PrimaryStore" ||
		extra.GatewayRoutes[0]["owner"] != "historical" ||
		extra.GatewayRoutes[0]["timeout_ms"] != float64(9000) ||
		!reflect.DeepEqual(extra.GatewayRoutes[0]["gateway_callers"], []any{"admin-gateway", "collector"}) {
		t.Fatalf("merged read route = %v", extra.GatewayRoutes)
	}
}

func TestMergeDefaultExtraConfigDoesNotExpandCustomMethodCallers(t *testing.T) {
	merged, changed := mergeDefaultExtraConfig(
		`{"gateway_routes":[{"service_path":"trpc.moox.storage.PrimaryStore","port":20102,"gateway_methods":["ReadFields","OperatorAudit"],"gateway_callers":["admin-gateway"],"owner":"ops"}]}`,
		`{"gateway_routes":[{"service_path":"trpc.moox.storage.PrimaryStore","port":20102,"gateway_methods":["ReadFields"],"gateway_callers":["admin-gateway","collector"]}]}`,
	)
	if changed {
		t.Fatal("non-empty operator routes must not be rewritten")
	}
	var extra struct {
		GatewayRoutes []map[string]any `json:"gateway_routes"`
	}
	if err := json.Unmarshal([]byte(merged), &extra); err != nil {
		t.Fatal(err)
	}
	if len(extra.GatewayRoutes) != 1 ||
		!reflect.DeepEqual(extra.GatewayRoutes[0]["gateway_methods"], []any{"ReadFields", "OperatorAudit"}) ||
		!reflect.DeepEqual(extra.GatewayRoutes[0]["gateway_callers"], []any{"admin-gateway"}) ||
		extra.GatewayRoutes[0]["owner"] != "ops" {
		t.Fatalf("custom method ACL was expanded: %v", extra.GatewayRoutes)
	}
}

func healthURL(raw string) string {
	var extra struct {
		HealthURL string `json:"health_url"`
	}
	_ = json.Unmarshal([]byte(raw), &extra)
	return extra.HealthURL
}

func monitorEnabled(raw string) bool {
	var extra struct {
		Enabled bool `json:"monitor_enabled"`
	}
	_ = json.Unmarshal([]byte(raw), &extra)
	return extra.Enabled
}
