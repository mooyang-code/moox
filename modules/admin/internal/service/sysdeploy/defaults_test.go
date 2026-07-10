package sysdeploy

import (
	"encoding/json"
	"testing"
)

func TestDefaultDeploymentsIncludeMonitorHealthMetadata(t *testing.T) {
	rows := DefaultDeployments()
	byName := make(map[string]Deployment, len(rows))
	for _, row := range rows {
		byName[row.ServiceName] = row
	}
	if _, ok := byName["monitor"]; !ok {
		t.Fatal("existing resource monitor deployment row missing")
	}
	monitor, ok := byName["moox_monitor"]
	if !ok {
		t.Fatal("moox_monitor deployment row missing")
	}
	if healthURL(monitor.ExtraConfig) != "http://127.0.0.1:11409/healthz" {
		t.Fatalf("moox_monitor extra_config = %s", monitor.ExtraConfig)
	}
	if healthURL(byName["moox_cloudnode"].ExtraConfig) != "http://127.0.0.1:11411/healthz" {
		t.Fatalf("cloudnode extra_config = %s", byName["moox_cloudnode"].ExtraConfig)
	}
	if healthURL(byName["storage_metadata"].ExtraConfig) != "http://127.0.0.1:20210/healthz" {
		t.Fatalf("storage_metadata extra_config = %s", byName["storage_metadata"].ExtraConfig)
	}
	if healthURL(byName["storage_access"].ExtraConfig) != "http://127.0.0.1:20210/healthz" {
		t.Fatalf("storage_access extra_config = %s", byName["storage_access"].ExtraConfig)
	}
	if healthURL(byName["storage_view"].ExtraConfig) != "http://127.0.0.1:20212/healthz" {
		t.Fatalf("storage_view extra_config = %s", byName["storage_view"].ExtraConfig)
	}
	if healthURL(byName["storage_view_builder"].ExtraConfig) != "http://127.0.0.1:20211/healthz" {
		t.Fatalf("storage_view_builder extra_config = %s", byName["storage_view_builder"].ExtraConfig)
	}
	if healthURL(byName["storage_view_query"].ExtraConfig) != "http://127.0.0.1:20212/healthz" {
		t.Fatalf("storage_view_query extra_config = %s", byName["storage_view_query"].ExtraConfig)
	}
	if byName["storage_view_query"].Port != 20202 {
		t.Fatalf("storage_view_query port = %d, want DataView HTTP 20202", byName["storage_view_query"].Port)
	}
	if healthURL(byName["storage_view_index"].ExtraConfig) != "http://127.0.0.1:20213/healthz" {
		t.Fatalf("storage_view_index extra_config = %s", byName["storage_view_index"].ExtraConfig)
	}
	if byName["storage_view_index"].Port != 20104 || byName["storage_view_index"].Protocol != "trpc" {
		t.Fatalf("storage_view_index endpoint = %s/%d, want trpc/20104", byName["storage_view_index"].Protocol, byName["storage_view_index"].Port)
	}
	if healthURL(byName["trade_account"].ExtraConfig) != "" {
		t.Fatalf("trade_account should not default to local health_url: %s", byName["trade_account"].ExtraConfig)
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

func healthURL(raw string) string {
	var extra struct {
		HealthURL string `json:"health_url"`
	}
	_ = json.Unmarshal([]byte(raw), &extra)
	return extra.HealthURL
}
