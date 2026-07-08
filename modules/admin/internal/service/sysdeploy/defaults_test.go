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
}

func healthURL(raw string) string {
	var extra struct {
		HealthURL string `json:"health_url"`
	}
	_ = json.Unmarshal([]byte(raw), &extra)
	return extra.HealthURL
}
