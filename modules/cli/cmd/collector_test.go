package cmd

import "testing"

func TestBuildCollectorCreateNodeItemIncludesCollectorWorkloads(t *testing.T) {
	item := buildCollectorCreateNodeItem(collectorPublishOptions{
		CloudAccountID:   "account-a",
		SpaceID:          "crypto",
		ServiceAccessKey: "svc-ak",
		ServiceSecretKey: "svc-sk",
		Runtime:          "CustomRuntime",
		Handler:          "main",
		Region:           "ap-guangzhou",
		PackageName:      "moox-collector",
		BizType:          "data_collector",
		NodeType:         "scf-event",
		Config:           []string{"timeout=60"},
		Env:              []string{"MOOX_ENV=prod"},
	}, "moox-collector_dev")

	if item.CloudAccountID != "account-a" || item.Region != "ap-guangzhou" || item.PackageID != "moox-collector_dev" {
		t.Fatalf("routing fields = %#v", item)
	}
	if item.Config["timeout"] != "60" {
		t.Fatalf("config = %#v", item.Config)
	}
	if item.Environment["MOOX_ENV"] != "prod" {
		t.Fatalf("env = %#v", item.Environment)
	}
	if item.Environment["MOOX_SPACE_ID"] != "crypto" {
		t.Fatalf("space env = %#v", item.Environment)
	}
	if item.Environment["MOOX_SERVICE_AUTH_ACCESS_KEY"] != "svc-ak" || item.Environment["MOOX_SERVICE_AUTH_SECRET_KEY"] != "svc-sk" {
		t.Fatalf("service auth env = %#v", item.Environment)
	}
	if item.Environment["MOOX_SERVICE_AUTH_EXPIRE_SECONDS"] != "1800" {
		t.Fatalf("service auth expire env = %#v", item.Environment)
	}
	if item.Metadata["function_name_prefix"] != "moox-collector" {
		t.Fatalf("function_name_prefix = %#v", item.Metadata["function_name_prefix"])
	}
	if item.Metadata["function_name"] != "moox-collector" || item.Metadata["node_id"] != "moox-collector" {
		t.Fatalf("exact function identity = %#v", item.Metadata)
	}
	if item.Metadata["biz_type"] != "data_collector" {
		t.Fatalf("biz_type = %#v", item.Metadata["biz_type"])
	}
	workloads, ok := item.Metadata["supported_workloads"].([]string)
	if !ok {
		t.Fatalf("supported_workloads type = %T", item.Metadata["supported_workloads"])
	}
	if len(workloads) != 2 || workloads[0] != "collect.kline" || workloads[1] != "collect.symbol" {
		t.Fatalf("supported_workloads = %#v", workloads)
	}
}

func TestBuildCollectorCreateNodeItemDefaultsToGoRuntime(t *testing.T) {
	item := buildCollectorCreateNodeItem(collectorPublishOptions{
		CloudAccountID: "account-a",
		Region:         "ap-guangzhou",
	}, "moox-collector_dev")

	if item.Runtime != "Go1" {
		t.Fatalf("runtime = %q, want Go1", item.Runtime)
	}
}

func TestCollectorFunctionEnvironmentAllowsExplicitEnvOverride(t *testing.T) {
	item := buildCollectorCreateNodeItem(collectorPublishOptions{
		CloudAccountID:   "account-a",
		SpaceID:          "crypto",
		ServiceAccessKey: "svc-ak",
		ServiceSecretKey: "svc-sk",
		Region:           "ap-guangzhou",
		Env: []string{
			"MOOX_SPACE_ID=override-space",
			"MOOX_SERVICE_AUTH_ACCESS_KEY=override-ak",
			"MOOX_SERVICE_AUTH_SECRET_KEY=override-sk",
			"MOOX_SERVICE_AUTH_EXPIRE_SECONDS=60",
		},
	}, "moox-collector_dev")

	if item.Environment["MOOX_SPACE_ID"] != "override-space" {
		t.Fatalf("space env = %#v", item.Environment)
	}
	if item.Environment["MOOX_SERVICE_AUTH_ACCESS_KEY"] != "override-ak" || item.Environment["MOOX_SERVICE_AUTH_SECRET_KEY"] != "override-sk" {
		t.Fatalf("service auth env = %#v", item.Environment)
	}
	if item.Environment["MOOX_SERVICE_AUTH_EXPIRE_SECONDS"] != "60" {
		t.Fatalf("expire env = %#v", item.Environment)
	}
}
