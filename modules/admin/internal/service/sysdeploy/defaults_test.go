package sysdeploy

import (
	"encoding/json"
	"os"
	"path/filepath"
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

func TestDefaultServiceGatewaysUseSplitHTTPSAndLoopbackEndpoints(t *testing.T) {
	byName := map[string]Deployment{}
	for _, row := range DefaultDeployments() {
		byName[row.ServiceName] = row
	}
	public := byName["service_gateway"]
	if public.Protocol != "https" || public.Port != 11001 || public.Scope != "public" {
		t.Fatalf("service_gateway = %#v", public)
	}
	internal := byName["service_gateway_internal"]
	if internal.Protocol != "http" || internal.Host != "127.0.0.1" || internal.Port != 11002 || internal.Scope != "internal" {
		t.Fatalf("service_gateway_internal = %#v", internal)
	}
}

func TestDefaultDeploymentsIncludeMonitorHealthMetadata(t *testing.T) {
	rows := DefaultDeployments()
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
	if healthURL(byName["moox_cloudnode"].ExtraConfig) != "http://127.0.0.1:11411/readyz" {
		t.Fatalf("cloudnode extra_config = %s", byName["moox_cloudnode"].ExtraConfig)
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
	if healthURL(byName["storage_metadata"].ExtraConfig) != "http://127.0.0.1:20210/readyz" {
		t.Fatalf("storage_metadata extra_config = %s", byName["storage_metadata"].ExtraConfig)
	}
	if healthURL(byName["storage_access"].ExtraConfig) != "http://127.0.0.1:20210/readyz" {
		t.Fatalf("storage_access extra_config = %s", byName["storage_access"].ExtraConfig)
	}
	if healthURL(byName["storage_view"].ExtraConfig) != "http://127.0.0.1:20212/readyz" {
		t.Fatalf("storage_view extra_config = %s", byName["storage_view"].ExtraConfig)
	}
	if healthURL(byName["storage_view_builder"].ExtraConfig) != "http://127.0.0.1:20211/readyz" {
		t.Fatalf("storage_view_builder extra_config = %s", byName["storage_view_builder"].ExtraConfig)
	}
	if healthURL(byName["storage_view_query"].ExtraConfig) != "http://127.0.0.1:20212/readyz" {
		t.Fatalf("storage_view_query extra_config = %s", byName["storage_view_query"].ExtraConfig)
	}
	if byName["storage_view_query"].Port != 20202 {
		t.Fatalf("storage_view_query port = %d, want DataView HTTP 20202", byName["storage_view_query"].Port)
	}
	if healthURL(byName["storage_view_index"].ExtraConfig) != "http://127.0.0.1:20213/readyz" {
		t.Fatalf("storage_view_index extra_config = %s", byName["storage_view_index"].ExtraConfig)
	}
	if byName["storage_view_index"].Port != 20104 || byName["storage_view_index"].Protocol != "trpc" {
		t.Fatalf("storage_view_index endpoint = %s/%d, want trpc/20104", byName["storage_view_index"].Protocol, byName["storage_view_index"].Port)
	}
	if healthURL(byName["trade_account"].ExtraConfig) != "" {
		t.Fatalf("trade_account should not default to local health_url: %s", byName["trade_account"].ExtraConfig)
	}
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

func TestMergeDefaultExtraConfigMigratesLegacyHealthzToReadiness(t *testing.T) {
	merged, changed := mergeDefaultExtraConfig(
		`{"health_url":"http://127.0.0.1:11411/healthz","monitor_enabled":true}`,
		`{"health_url":"http://127.0.0.1:11411/readyz","health_kind":"readiness","monitor_enabled":true}`,
	)
	if !changed {
		t.Fatal("changed = false, want legacy health migration")
	}
	var extra map[string]interface{}
	if err := json.Unmarshal([]byte(merged), &extra); err != nil {
		t.Fatalf("unmarshal merged: %v", err)
	}
	if extra["health_url"] != "http://127.0.0.1:11411/readyz" || extra["health_kind"] != "readiness" {
		t.Fatalf("health metadata = %v", extra)
	}
}

func healthURL(raw string) string {
	var extra struct {
		HealthURL string `json:"health_url"`
	}
	_ = json.Unmarshal([]byte(raw), &extra)
	return extra.HealthURL
}
