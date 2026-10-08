package servicecatalog

import "testing"

func TestAllowed(t *testing.T) {
	catalog := Default()
	cases := []struct {
		caller, path, method string
		want                 bool
	}{
		{"console", "trpc.moox.cloudnode.CloudNodeMgr", "CollectGarbage", false},
		{"admin", "trpc.moox.cloudnode.CloudNodeMgr", "CollectGarbage", true},
		{"console", "trpc.moox.cloudnode.CloudNodeMgr", "GetNodeList", true},
		{"console", "trpc.moox.ops.SecretMgr", "GetSecretValue", false},
		{"cloudnode", "trpc.moox.ops.SecretMgr", "GetSecretValue", true},
		{"trade", "trpc.moox.ops.SecretMgr", "GetSecretValue", true},
		{"console", "trpc.moox.admin.CollectorPublishLease", "ValidateCollectorPublishLease", false},
		{"console", "trpc.moox.admin.CollectorPublishLease", "BeginCollectorPublishOperation", false},
		{"console", "trpc.moox.admin.CollectorPublishLease", "RenewCollectorPublishOperation", false},
		{"console", "trpc.moox.admin.CollectorPublishLease", "EndCollectorPublishOperation", false},
		{"console", "trpc.moox.admin.CollectorPublishLease", "AcquireCollectorPublishLease", true},
		{"host-gateway@storage", "trpc.moox.admin.GatewayControl", "PullSnapshot", true},
		{"host-gateway", "trpc.moox.admin.GatewayControl", "PullSnapshot", false},
		{"host-gateway@", "trpc.moox.admin.GatewayControl", "PullSnapshot", false},
		{"console", "trpc.moox.admin.GatewayControl", "PullSnapshot", false},
		{"access", "trpc.moox.storage.PrimaryStore", "CommitTimeSeriesBatch", true},
		{"access", "trpc.moox.storage.PrimaryStore", "DeleteDatasetRows", false},
		{"access", "trpc.moox.collector.MarketFetchRuntime", "ClaimTimerBatch", true},
		{"collector", "trpc.moox.collector.MarketFetchRuntime", "ClaimTimerBatch", false},
		{"access", "trpc.moox.factor.FactorEngine", "PullRecalcJob", true},
		{"console", "trpc.moox.trade.TradeConsoleService", "ClaimLogicalAccountOwner", false},
		{"strategy", "trpc.moox.trade.TradeConsoleService", "ClaimLogicalAccountOwner", true},
		{"storage-view", "trpc.moox.storage.Metadata", "ClaimViewIndexBuild", true},
		{"collector", "trpc.moox.storage.Metadata", "ClaimViewIndexBuild", false},
		{"console", "trpc.moox.storage.Metadata", "RegisterDataNode", false},
		{"console", "trpc.moox.storage.Metadata", "NoSuchMethod", false},
		{"console", "trpc.moox.no.Service", "ListDatasets", false},
	}
	for _, tc := range cases {
		if got := catalog.Allowed(tc.caller, tc.path, tc.method); got != tc.want {
			t.Errorf("Allowed(%s, %s, %s) = %v, want %v", tc.caller, tc.path, tc.method, got, tc.want)
		}
	}
}

func TestPrincipalAllowed(t *testing.T) {
	catalog := Default()
	if !catalog.PrincipalAllowed("scf-collector", "trpc.moox.storage.PrimaryStore", "EnsureDatasetPeriod") {
		t.Fatal("scf-collector 应当能调用 EnsureDatasetPeriod")
	}
	if catalog.PrincipalAllowed("scf-collector", "trpc.moox.storage.Metadata", "CreateDataset") {
		t.Fatal("scf-collector 不再有建表权限")
	}
	if catalog.PrincipalAllowed("moox-skill", "trpc.moox.storage.PrimaryStore", "WriteFactorRows") {
		t.Fatal("moox-skill 只能读")
	}
	if catalog.PrincipalAllowed("nobody", "trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows") {
		t.Fatal("未知外部调用方不能放行")
	}
}

func TestKnownCaller(t *testing.T) {
	catalog := Default()
	for _, caller := range []string{"console", "moox-cli", "admin", "collector", "access", "host-gateway@compute-1"} {
		if !catalog.KnownCaller(caller) {
			t.Errorf("%s 应当是已知调用方", caller)
		}
	}
	for _, caller := range []string{"host-gateway", "scf-collector", "admin-gateway", "host-gateway@Compute"} {
		if catalog.KnownCaller(caller) {
			t.Errorf("%s 不应当是已知调用方", caller)
		}
	}
}
