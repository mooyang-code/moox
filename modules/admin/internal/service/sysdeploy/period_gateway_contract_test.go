package sysdeploy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mooyang-code/moox/packages/gatewayproxy"
	"gopkg.in/yaml.v3"
)

func TestCollectorPeriodGatewayContract(t *testing.T) {
	methods := []string{"EnsureDatasetPeriod", "CommitTimeSeriesBatch", "RecordDatasetPeriodFailures", "GetDatasetPeriodStatus"}
	tables := make(map[string]*gatewayproxy.Table)
	for _, source := range []string{"defaults", "deployment_yaml"} {
		table := collectorPeriodGatewayTable(t, source)
		tables[source] = table
		t.Run(source, func(t *testing.T) {
			for _, method := range methods {
				rpc := "/trpc.moox.storage.PrimaryStore/" + method
				route, resolved, found := table.ResolveRPCForCaller(rpc, "collector")
				if !found || resolved != method || route.Address != "127.0.0.1:20102" {
					t.Errorf("collector cannot reach %s: route=%+v method=%q found=%v", rpc, route, resolved, found)
				}
				for _, caller := range []string{"admin-gateway", "moox-cli", "moox-skill", "factor", "monitor", "archive", "storage-view", "strategy", ""} {
					if _, _, allowed := table.ResolveRPCForCaller(rpc, caller); allowed {
						t.Errorf("%s can invoke collector-only method %s", caller, method)
					}
				}
			}
		})
	}
	for _, method := range methods {
		rpc := "/trpc.moox.storage.PrimaryStore/" + method
		defaults, _, defaultFound := tables["defaults"].ResolveRPCForCaller(rpc, "collector")
		yamlRoute, _, yamlFound := tables["deployment_yaml"].ResolveRPCForCaller(rpc, "collector")
		if defaultFound != yamlFound || !reflect.DeepEqual(defaults, yamlRoute) {
			t.Errorf("default/YAML route drift for %s: defaults=%+v YAML=%+v", method, defaults, yamlRoute)
		}
	}
}

func collectorPeriodGatewayTable(t *testing.T, source string) *gatewayproxy.Table {
	t.Helper()
	rows := DefaultDeployments("control")
	if source == "deployment_yaml" {
		var seed struct {
			Services []struct {
				Name             string         `yaml:"name"`
				Host             string         `yaml:"host"`
				Port             int32          `yaml:"port"`
				GatewayPath      string         `yaml:"gateway_path"`
				GatewayServiceID string         `yaml:"gateway_service_id"`
				ExtraConfig      map[string]any `yaml:"extra_config"`
			} `yaml:"services"`
		}
		path := filepath.Join("..", "..", "..", "..", "..", "config", "setup", "service-deployments.yaml")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := yaml.Unmarshal(raw, &seed); err != nil {
			t.Fatal(err)
		}
		rows = nil
		for _, item := range seed.Services {
			if item.Name != "storage-primary" {
				continue
			}
			extra, err := json.Marshal(item.ExtraConfig)
			if err != nil {
				t.Fatal(err)
			}
			rows = append(rows, Deployment{ServiceName: item.Name, Host: item.Host, Port: item.Port,
				GatewayPath: item.GatewayPath, GatewayServiceID: item.GatewayServiceID, ExtraConfig: string(extra)})
		}
	}
	var routes []gatewayproxy.Route
	for _, row := range rows {
		if row.ServiceName != "storage-primary" {
			continue
		}
		extra, err := parseRouteExtraConfig(row.ExtraConfig)
		if err != nil {
			t.Fatal(err)
		}
		compiled, err := deploymentGatewayRoutes(row, extra)
		if err != nil {
			t.Fatal(err)
		}
		routes = append(routes, compiled...)
	}
	if len(routes) == 0 {
		t.Fatal("production storage-primary routes are missing")
	}
	snapshot, err := gatewayproxy.NormalizeAndHash("control", routes)
	if err != nil {
		t.Fatal(err)
	}
	table := &gatewayproxy.Table{}
	if err := table.Replace(snapshot); err != nil {
		t.Fatal(err)
	}
	return table
}
