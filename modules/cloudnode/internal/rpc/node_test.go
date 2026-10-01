package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/cloudcredential"
	tencentscf "github.com/mooyang-code/moox/modules/cloudnode/internal/providers/tencentscf"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/spacecontext"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	cloudnodeschema "github.com/mooyang-code/moox/modules/cloudnode/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
	"gorm.io/gorm"
)

func TestNodeMetadataBranches(t *testing.T) {
	metadata, err := structpb.NewStruct(map[string]any{"existing": "yes"})
	require.NoError(t, err)
	got := nodeMetadataFromPB(&pb.CloudNode{
		Metadata: metadata, BizType: "market_fetcher", Tag: "prod", IpAddress: "10.0.0.1",
	})
	assert.Equal(t, "market_fetcher", got["biz_type"])
	assert.NotContains(t, got, "timeout_threshold")
	assert.NotContains(t, got, "probe_enabled")
	assert.NotContains(t, got, "probe_url")
	assert.Empty(t, nodeMetadataFromPB(nil))
}

func TestUpdateNodeAndConversion(t *testing.T) {
	catalog := newCatalogForAccountTests(t)
	svc := &Service{catalog: catalog}
	ctx := spacecontext.WithSpaceID(context.Background(), "crypto")
	require.NoError(t, catalog.UpsertNode(ctx, store.CloudNode{
		SpaceID: "crypto", NodeID: "node-a", CloudAccountID: "acct-1", Region: "ap-guangzhou",
	}))
	rsp, err := svc.UpdateNode(ctx, &pb.UpdateNodeReq{Node: &pb.CloudNode{
		NodeId: "node-a", Region: "ap-shanghai",
	}})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	node, err := catalog.GetNode(ctx, "crypto", "node-a")
	require.NoError(t, err)
	assert.Equal(t, "ap-shanghai", node.Region)

	require.NoError(t, catalog.UpsertNode(ctx, store.CloudNode{
		SpaceID: "crypto", NodeID: "timer-node", NodeType: "scf-event", TriggerType: "timer", Region: "ap-shanghai",
	}))
	bad, err := svc.UpdateNode(ctx, &pb.UpdateNodeReq{Node: &pb.CloudNode{NodeId: "timer-node", TriggerType: "timre"}})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, bad.GetRetInfo().GetCode())
	assert.Contains(t, bad.GetRetInfo().GetMsg(), "trigger_type must be invoke or timer")
	unchanged, err := catalog.GetNode(ctx, "crypto", "timer-node")
	require.NoError(t, err)
	assert.Equal(t, "timer", unchanged.TriggerType)

	pbNode := toPBNode(store.CloudNode{SpaceID: "crypto", NodeID: "n1", Metadata: `{"biz_type":"market_fetcher"}`})
	assert.Equal(t, "market_fetcher", pbNode.GetBizType())
}

func TestGetNodeListRefreshesTimerTriggerReadback(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{
		SpaceID: "crypto", NodeID: "timer-node", CloudAccountID: "account-a", PackageID: "pkg", DeploymentID: "deployment-1",
		NodeType: "scf-event", TriggerType: "timer", Region: "ap-guangzhou", FunctionName: "timer-node",
		Metadata: `{"deployment_ready":true,"timer_enabled":true,"timer_cron":"0 * * * * * *","timer_available_status":"Available"}`,
	}))
	fake := &fakeSCFClient{timerInfoSet: true, timerInfo: &tencentscf.TimerTriggerInfo{Name: timerTriggerName, Type: "timer", Cron: "0 */5 * * * * *", Enabled: false, AvailableStatus: "Available", Qualifier: timerTriggerQualifier, Message: timerTriggerMessage}}
	svc := &Service{catalog: catalog, credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}}, scfClientFactory: func(cloudcredential.TencentCredential) scfProvisioner { return fake }}
	rsp, err := svc.GetNodeList(spacecontext.WithSpaceID(context.Background(), "crypto"), &pb.GetNodeListReq{TriggerType: "timer"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetItems(), 1)
	metadata := rsp.GetItems()[0].GetMetadata().AsMap()
	assert.Equal(t, false, metadata["timer_actual_enabled"])
	assert.Equal(t, "0 */5 * * * * *", metadata["timer_actual_cron"])
	assert.Equal(t, "Available", metadata["timer_available_status"])
	stored, err := catalog.GetNode(context.Background(), "crypto", "timer-node")
	require.NoError(t, err)
	assert.Contains(t, stored.Metadata, "timer_actual_enabled")
}

func TestGetNodeListSkipsTimerReadbackForCollectorSnapshot(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{
		SpaceID: "crypto", NodeID: "timer-node", CloudAccountID: "account-a", PackageID: "pkg", DeploymentID: "deployment-1",
		NodeType: "scf-event", TriggerType: "timer", Region: "ap-guangzhou", FunctionName: "timer-node",
		Metadata: `{"biz_type":"market_fetcher","deployment_ready":true,"timer_enabled":true}`,
	}))
	fake := &fakeSCFClient{timerInfoSet: true, timerInfo: &tencentscf.TimerTriggerInfo{Name: timerTriggerName, Type: "timer", Cron: "0 */5 * * * * *", Enabled: false, AvailableStatus: "Available"}}
	svc := &Service{catalog: catalog, credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}}, scfClientFactory: func(cloudcredential.TencentCredential) scfProvisioner { return fake }}

	rsp, err := svc.GetNodeList(spacecontext.WithSpaceID(context.Background(), "crypto"), &pb.GetNodeListReq{BizType: "market_fetcher", TriggerType: "timer"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetItems(), 1)
	assert.Equal(t, 0, fake.getCalls, "collector catalog snapshots must not wait for Tencent SCF readback")
}

func TestExecuteCreateNodeItemCreatesShortLivedFunction(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	environment := completeInvokeEnvironment("15")
	environment["MOOX_CODE_PACKAGE_ID"] = "moox-collector_dev"
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{
		{err: errors.New("ResourceNotFound.FunctionName")},
		{info: &tencentscf.FunctionInfo{Status: "Active", MemorySize: 64, Timeout: 15, Environment: environment}},
	}}
	svc := &Service{
		catalog:            catalog,
		credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}},
		scfClientFactory:   func(cloudcredential.TencentCredential) scfProvisioner { return fake },
	}
	metadata, err := structpb.NewStruct(map[string]any{"biz_type": "market_fetcher", "function_name_prefix": "moox-fetcher-crypto-market"})
	require.NoError(t, err)
	_, err = svc.executeCreateNodeItem(context.Background(), "crypto", &pb.NodeCreateItem{
		CloudAccountId: "account-a", Region: "ap-singapore", PackageId: "moox-collector_dev", Runtime: "CustomRuntime", Handler: "main",
		Config:      map[string]string{"memory_size": "64", "timeout": "15"},
		Environment: environment, Metadata: metadata,
	}, 0)
	require.NoError(t, err)
	require.Len(t, fake.created, 1)
	assert.Equal(t, int64(64), fake.created[0].MemorySize)
	assert.Equal(t, int64(15), fake.created[0].Timeout)
	assert.NotContains(t, fake.created[0].Environment, "MOOX_MONITOR_READY_URL")
}

func TestExecuteCreateInvokeMarketFetcherPreservesConfiguredTimeout(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	environment := completeInvokeEnvironment("60")
	environment["MOOX_CODE_PACKAGE_ID"] = "moox-collector_dev"
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{
		{err: errors.New("ResourceNotFound.FunctionName")},
		{info: &tencentscf.FunctionInfo{Status: "Active", MemorySize: 64, Timeout: 60, Environment: environment}},
	}}
	svc := &Service{
		catalog:            catalog,
		credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}},
		scfClientFactory:   func(cloudcredential.TencentCredential) scfProvisioner { return fake },
	}
	metadata, err := structpb.NewStruct(map[string]any{"biz_type": "market_fetcher", "function_name_prefix": "moox-fetcher-crypto-market"})
	require.NoError(t, err)
	_, err = svc.executeCreateNodeItem(context.Background(), "crypto", &pb.NodeCreateItem{
		CloudAccountId: "account-a", Region: "ap-singapore", PackageId: "moox-collector_dev", Runtime: "CustomRuntime", Handler: "main",
		TriggerType: "invoke", Config: map[string]string{"memory_size": "64", "timeout": "60"},
		Environment: environment, Metadata: metadata,
	}, 0)
	require.NoError(t, err)
	require.Len(t, fake.created, 1)
	assert.Equal(t, int64(60), fake.created[0].Timeout)
}

func TestExecuteCreateTimerMarketFetcherKeepsAuthoritativeTimeout(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	environment := completeTimerEnvironment()
	environment["MOOX_CODE_PACKAGE_ID"] = "moox-collector_dev"
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{
		{err: errors.New("ResourceNotFound.FunctionName")},
		{info: &tencentscf.FunctionInfo{Status: "Active", MemorySize: 64, Timeout: 60, Environment: environment}},
	}}
	svc := &Service{
		catalog:            catalog,
		credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}},
		scfClientFactory:   func(cloudcredential.TencentCredential) scfProvisioner { return fake },
	}
	metadata, err := structpb.NewStruct(map[string]any{"biz_type": "market_fetcher", "function_name_prefix": "moox-fetcher-crypto-market"})
	require.NoError(t, err)
	_, err = svc.executeCreateNodeItem(context.Background(), "crypto", &pb.NodeCreateItem{
		CloudAccountId: "account-a", Region: "ap-singapore", PackageId: "moox-collector_dev", Runtime: "CustomRuntime", Handler: "main",
		TriggerType: "timer", Config: map[string]string{"memory_size": "64", "timeout": "60"},
		Environment: environment, Metadata: metadata,
	}, 0)
	require.NoError(t, err)
	require.Len(t, fake.created, 1)
	assert.Equal(t, int64(60), fake.created[0].Timeout)
}

func TestExecuteCreateDurableTimerRejectsRuntimeTimeoutMismatchBeforeCloudAPI(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	factoryCalls := 0
	svc := &Service{catalog: catalog,
		credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}},
		scfClientFactory:   func(cloudcredential.TencentCredential) scfProvisioner { factoryCalls++; return &fakeSCFClient{} },
	}
	metadata, err := structpb.NewStruct(map[string]any{"biz_type": "market_fetcher", "function_mode": "kline"})
	require.NoError(t, err)
	_, err = svc.executeCreateNodeItem(context.Background(), "crypto", &pb.NodeCreateItem{
		CloudAccountId: "account-a", Region: "ap-singapore", PackageId: "moox-collector_dev", Runtime: "CustomRuntime", Handler: "main",
		TriggerType: "timer", Config: map[string]string{"memory_size": "64", "timeout": "15"},
		Environment: map[string]string{"MOOX_SPACE_ID": "crypto", "MOOX_FETCH_TIMEOUT_SECONDS": "60"}, Metadata: metadata,
	}, 0)
	require.ErrorContains(t, err, "Timer runtime timeout")
	require.Zero(t, factoryCalls)
}

func TestMarketFetchTimerTimeoutContract(t *testing.T) {
	for _, test := range []struct {
		name, mode, outer, runtime string
		wantError                  bool
	}{
		{"matching durable", "kline", "60", "60", false},
		{"missing durable runtime defaults safely", "kline", "60", "", false},
		{"malformed durable runtime", "kline", "60", "invalid", true},
		{"runtime marked durable", "", "15", "60", true},
		{"legacy instrument snapshot", "instrument_snapshot", "15", "60", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			node := &store.CloudNode{TriggerType: "timer", Metadata: fmt.Sprintf(`{"biz_type":"market_fetcher","function_mode":%q}`, test.mode)}
			err := validateMarketFetchTimerTimeout(node, store.FunctionPackage{WorkloadType: "market_fetcher"}, map[string]string{"MOOX_FETCH_TIMEOUT_SECONDS": test.runtime}, configInt64(map[string]string{"timeout": test.outer}, "timeout", 60))
			if test.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestExecuteCreateLegacyInstrumentModeUsesTimerTimeout(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	require.NoError(t, catalog.UpsertPackage(context.Background(), store.FunctionPackage{
		SpaceID: "stockcn", PackageID: "moox-collector_dev", PackageName: "collector", Version: "dev",
		Runtime: "CustomRuntime", PackageType: "collector", WorkloadType: "market_fetcher", CloudAccountID: "account-a",
		COSRegion: "ap-guangzhou", COSBucket: "bucket", COSPath: "packages/collector.zip", Status: "available", CreateTime: time.Now().UTC(),
	}))
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{
		{err: errors.New("ResourceNotFound.FunctionName")},
		{info: &tencentscf.FunctionInfo{Status: "Active", MemorySize: 64, Timeout: 15, Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "moox-collector_dev", "MOOX_SPACE_ID": "stockcn"}}},
	}}
	svc := &Service{
		catalog:            catalog,
		credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}},
		scfClientFactory:   func(cloudcredential.TencentCredential) scfProvisioner { return fake },
	}
	metadata, err := structpb.NewStruct(map[string]any{"biz_type": "market_fetcher", "function_mode": "instrument_snapshot", "function_name_prefix": "moox-fetcher-stockcn-instrument"})
	require.NoError(t, err)
	_, err = svc.executeCreateNodeItem(context.Background(), "stockcn", &pb.NodeCreateItem{
		CloudAccountId: "account-a", Region: "ap-chengdu", PackageId: "moox-collector_dev", Runtime: "CustomRuntime", Handler: "main",
		TriggerType: "timer", Config: map[string]string{"memory_size": "64", "timeout": "15"},
		Environment: map[string]string{"MOOX_SPACE_ID": "stockcn"}, Metadata: metadata,
	}, 0)
	require.NoError(t, err)
	require.Len(t, fake.created, 1)
	assert.Equal(t, int64(15), fake.created[0].Timeout)
}

func TestExecuteCreateNodeItemRejectsEnvironmentOverLimitBeforeCreate(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{{err: errors.New("ResourceNotFound.FunctionName")}}}
	svc := &Service{
		catalog:            catalog,
		credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}},
		scfClientFactory:   func(cloudcredential.TencentCredential) scfProvisioner { return fake },
	}
	metadata, err := structpb.NewStruct(map[string]any{"biz_type": "market_fetcher", "function_name_prefix": "moox-fetcher-crypto-market"})
	require.NoError(t, err)
	_, err = svc.executeCreateNodeItem(context.Background(), "crypto", &pb.NodeCreateItem{
		CloudAccountId: "account-a", Region: "ap-singapore", PackageId: "moox-collector_dev", Runtime: "CustomRuntime", Handler: "main",
		TriggerType: "timer", Config: map[string]string{"memory_size": "64", "timeout": "60"},
		Environment: map[string]string{"MOOX_SPACE_ID": strings.Repeat("x", maxSCFEnvironmentBytes)}, Metadata: metadata,
	}, 0)
	require.ErrorContains(t, err, "environment is")
	assert.Empty(t, fake.created)
}

func TestExecuteCreateExistingTimerNodeEnsuresTrigger(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	environment := completeTimerEnvironment()
	environment["MOOX_CODE_PACKAGE_ID"] = "moox-collector_dev"
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{{info: &tencentscf.FunctionInfo{Status: "Active", MemorySize: 64, Timeout: 60, Environment: environment}}}}
	svc := &Service{
		catalog:            catalog,
		credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}},
		scfClientFactory:   func(cloudcredential.TencentCredential) scfProvisioner { return fake },
	}
	metadata, err := structpb.NewStruct(map[string]any{"biz_type": "market_fetcher", "function_name_prefix": "moox-fetcher-crypto-market"})
	require.NoError(t, err)
	_, err = svc.executeCreateNodeItem(context.Background(), "crypto", &pb.NodeCreateItem{
		CloudAccountId: "account-a", Region: "ap-singapore", Namespace: "collector", PackageId: "moox-collector_dev", Runtime: "CustomRuntime", Handler: "main",
		TriggerType: "timer", Config: map[string]string{"memory_size": "64", "timeout": "60"}, Environment: environment, Metadata: metadata,
	}, 0)
	require.NoError(t, err)
	require.Equal(t, 1, fake.timerEnsures)
}

func TestExecuteDeployNodeItemUpdatesConfiguration(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{
		SpaceID: "crypto", NodeID: "node-a", CloudAccountID: "account-a", PackageID: "old-package", NodeType: "scf-event", Provider: "tencent-scf",
		Region: "ap-singapore", Namespace: "collector", FunctionName: "fetcher-0", Metadata: `{"biz_type":"market_fetcher","handler":"main"}`,
	}))
	remote := completeInvokeEnvironment("15")
	remote["MOOX_COLLECTOR_RPC_GATEWAY_TARGET"] = "ip://collector.example:11004"
	remote["MOOX_COLLECTOR_NODE_ID"] = "collector-node"
	remote["MOOX_RPC_SERVICE_ID"] = "moox-collector"
	remote["MOOX_RPC_SERVICE_SECRET"] = "private-test-secret"
	fake := &fakeSCFClient{currentEnvironment: remote}
	svc := &Service{
		catalog:            catalog,
		credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}},
		scfClientFactory:   func(cloudcredential.TencentCredential) scfProvisioner { return fake },
	}
	_, err := svc.executeDeployNodeItem(context.Background(), "crypto", &pb.NodeDeployItem{
		NodeId: "node-a", PackageId: "moox-collector_dev", Config: map[string]string{"memory_size": "64", "timeout": "15"},
		Environment: map[string]string{"MOOX_SPACE_ID": "crypto"},
	})
	require.NoError(t, err)
	require.Len(t, fake.updated, 1)
	require.Len(t, fake.configured, 1)
	assert.Equal(t, int64(64), fake.configured[0].MemorySize)
	assert.Equal(t, int64(15), fake.configured[0].Timeout)
	assert.Equal(t, "moox-collector_dev", fake.configured[0].Environment["MOOX_CODE_PACKAGE_ID"])
	for key, value := range remote {
		if key != "MOOX_CODE_PACKAGE_ID" {
			assert.Equal(t, value, fake.configured[0].Environment[key], "partial deploy preserves managed runtime route and credentials")
		}
	}
}

func TestExecuteDeployNodeItemRejectsMergedTimerEnvironmentOverLimit(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{
		SpaceID: "crypto", NodeID: "timer-node", CloudAccountID: "account-a", PackageID: "old-package", NodeType: "scf-event", TriggerType: "timer", Provider: "tencent-scf",
		Region: "ap-singapore", Namespace: "collector", FunctionName: "fetcher-timer", Metadata: `{"biz_type":"market_fetcher","handler":"main"}`,
	}))
	large := map[string]string{"MOOX_CODE_PACKAGE_ID": "old-package"}
	for index := 0; index < 80; index++ {
		large[fmt.Sprintf("MOOX_EXTRA_%03d", index)] = strings.Repeat("x", 64)
	}
	fake := &fakeSCFClient{currentEnvironment: large}
	svc := &Service{
		catalog:            catalog,
		credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}},
		scfClientFactory:   func(cloudcredential.TencentCredential) scfProvisioner { return fake },
	}
	_, err := svc.executeDeployNodeItem(context.Background(), "crypto", &pb.NodeDeployItem{
		NodeId: "timer-node", PackageId: "moox-collector_dev", Environment: map[string]string{"MOOX_SPACE_ID": "crypto"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "environment is")
	assert.Empty(t, fake.updated)
	assert.Empty(t, fake.configured)
}

func TestExecuteRuntimeConfigSkipsUnchangedEnvironmentUpdate(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{
		SpaceID: "crypto", NodeID: "timer-node", CloudAccountID: "account-a", PackageID: "pkg", NodeType: "scf-event", TriggerType: "timer", Provider: "tencent-scf",
		Region: "ap-singapore", Namespace: "collector", FunctionName: "fetcher-timer", Metadata: `{"biz_type":"market_fetcher"}`,
	}))
	environment := completeTimerEnvironment()
	environment["MOOX_CODE_PACKAGE_ID"] = "pkg"
	environment["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"] = "assignment"
	environment["MOOX_MARKET_FETCH_DNS_HASH"] = "dns"
	environment["MOOX_MARKET_FETCH_DNS_UPDATED_AT"] = "2026-08-04T01:00:00Z"
	environment["MOOX_MARKET_FETCH_SUBJECTS"] = "BTC-USDT"
	environment["MOOX_MARKET_FETCH_SYMBOLS_JSON"] = `{"BTC-USDT":"BTCUSDT"}`
	environment["MOOX_MARKET_FETCH_PROVIDER"] = "binance"
	environment["MOOX_MARKET_FETCH_MARKET_TYPE"] = "spot"
	environment["MOOX_MARKET_FETCH_DATASET_ID"] = "bars"
	environment["MOOX_MARKET_FETCH_FREQUENCY"] = "1m"
	environment["MOOX_MARKET_FETCH_DNS_ROUTES_JSON"] = `{"api.binance.com":["203.0.113.1"]}`
	fake := &fakeSCFClient{currentEnvironment: environment, currentTimeout: 60}
	svc := &Service{
		catalog:            catalog,
		credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}},
		scfClientFactory:   func(cloudcredential.TencentCredential) scfProvisioner { return fake },
	}
	_, err := svc.executeRuntimeConfigItem(context.Background(), "crypto", &pb.NodeRuntimeConfigPatch{
		NodeId: "timer-node", ManagedEnvironment: environment, TimerCron: "0 * * * * * *", TimerEnabled: true,
	})
	require.NoError(t, err)
	require.Empty(t, fake.configured, "unchanged managed environment must not call UpdateFunctionConfiguration")
}

func TestExecuteRuntimeConfigDoesNotEnableTimerWithIncompleteDependencies(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{
		SpaceID: "crypto", NodeID: "timer-node", CloudAccountID: "account-a", PackageID: "pkg", NodeType: "scf-event", TriggerType: "timer", Provider: "tencent-scf",
		Region: "ap-singapore", Namespace: "collector", FunctionName: "fetcher-timer", Metadata: `{"biz_type":"market_fetcher"}`,
	}))
	environment := completeTimerEnvironment()
	delete(environment, "MOOX_STORAGE_RPC_GATEWAY_TARGET")
	fake := &fakeSCFClient{currentEnvironment: environment}
	svc := &Service{
		catalog:            catalog,
		credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}},
		scfClientFactory:   func(cloudcredential.TencentCredential) scfProvisioner { return fake },
	}
	_, err := svc.executeRuntimeConfigItem(context.Background(), "crypto", &pb.NodeRuntimeConfigPatch{
		NodeId: "timer-node", ManagedEnvironment: map[string]string{"MOOX_MARKET_FETCH_BINDING_HASH": "binding"},
		TimerCron: "0 * * * * * *", TimerEnabled: true,
	})
	require.ErrorContains(t, err, "MOOX_STORAGE_RPC_GATEWAY_TARGET")
	require.Empty(t, fake.configured, "invalid runtime dependencies must be rejected before updating configuration")
	require.Zero(t, fake.timerEnsures, "invalid runtime dependencies must not enable the Timer trigger")
}

func TestExecuteRuntimeConfigUpgradesClaimRoutingAndTimeout(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{
		SpaceID: "stockcn", NodeID: "timer-node", CloudAccountID: "account-a", NodeType: "scf-event", TriggerType: "timer",
		Region: "ap-guangzhou", Namespace: "collector", FunctionName: "fetcher-timer", Metadata: `{"biz_type":"market_fetcher"}`,
	}))
	environment := completeTimerEnvironment()
	environment["MOOX_SPACE_ID"] = "stockcn"
	environment["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"] = "assignment"
	environment["MOOX_MARKET_FETCH_SUBJECTS"] = strings.Repeat("old-subject|", 80)
	environment["MOOX_EVENTBUS_NATS_PASSWORD"] = "keep-password"
	environment["MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64"] = "cGVt"
	fake := &fakeSCFClient{currentEnvironment: environment}
	svc := &Service{catalog: catalog, credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}}, scfClientFactory: func(cloudcredential.TencentCredential) scfProvisioner { return fake }}
	patch := &pb.NodeRuntimeConfigPatch{NodeId: "timer-node", TimerCron: "5 * * * * * *", TimerEnabled: true, ManagedEnvironment: map[string]string{
		"MOOX_MARKET_FETCH_ASSIGNMENT_HASH": "assignment", "MOOX_MARKET_FETCH_BINDING_HASH": "binding", "MOOX_COLLECTOR_RPC_GATEWAY_TARGET": "ip://collector.example:11002", "MOOX_COLLECTOR_GATEWAY_TARGET_NODE": "control-a", "MOOX_FETCH_TIMEOUT_SECONDS": "60",
	}}
	require.Nil(t, svc.preflightRuntimeConfig(context.Background(), "stockcn", patch))
	_, err := svc.executeRuntimeConfigItem(context.Background(), "stockcn", patch)
	require.NoError(t, err)
	require.Len(t, fake.configured, 1)
	require.EqualValues(t, 60, fake.configured[0].Timeout)
	require.Equal(t, "keep-password", fake.currentEnvironment["MOOX_EVENTBUS_NATS_PASSWORD"])
	require.NotContains(t, fake.currentEnvironment, "MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64")
	require.NotContains(t, fake.configured[0].Environment, "MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64")
	require.NotContains(t, fake.currentEnvironment, "MOOX_MARKET_FETCH_SUBJECTS")
	node, err := catalog.GetNode(context.Background(), "stockcn", "timer-node")
	require.NoError(t, err)
	require.Contains(t, node.Metadata, "collector_rpc_gateway_target")
	require.Contains(t, node.Metadata, `"timeout_seconds":60`, "later redeploys must not restore the legacy catalog timeout")
}

func TestReconcileSCFPublicNetworkClearsPreviousVPC(t *testing.T) {
	fake := &fakeSCFClient{}
	info := &tencentscf.FunctionInfo{Status: "Active", VpcID: "vpc-old", SubnetID: "subnet-old", Environment: map[string]string{}}
	err := reconcileSCFPublicNetwork(context.Background(), fake, tencentscf.FunctionRef{
		Region: "ap-hongkong", Namespace: "collector", FunctionName: "collector-ap-hongkong-0",
	}, info, map[string]string{"clear_vpc": "true", "public_net_status": "ENABLE"})
	require.NoError(t, err)
	require.Len(t, fake.configured, 1)
	assert.True(t, fake.configured[0].ClearVPC)
	assert.Empty(t, fake.configured[0].VpcID)
	assert.Empty(t, fake.configured[0].SubnetID)
}

type fakeSCFClient struct {
	mu                   sync.Mutex
	getResults           []fakeSCFGetResult
	getCalls             int
	respectContext       bool
	createErr            error
	createWaitForContext bool
	created              []tencentscf.CreateFunctionRequest
	updated              []tencentscf.UpdateFunctionCodeRequest
	configured           []tencentscf.UpdateFunctionConfigurationRequest
	currentEnvironment   map[string]string
	currentTimeout       int64
	timerEnsures         int
	timerInfoSet         bool
	timerInfo            *tencentscf.TimerTriggerInfo
	timerErr             error
	inventory            []tencentscf.DiscoveryFunction
	inventoryRegion      string
}

func (f *fakeSCFClient) EnsureNamespace(context.Context, string, string) error { return nil }

func (f *fakeSCFClient) ListFunctionInventory(_ context.Context, region string) ([]tencentscf.DiscoveryFunction, error) {
	if f.inventoryRegion != "" && f.inventoryRegion != region {
		return nil, nil
	}
	return f.inventory, nil
}

type fakeSCFGetResult struct {
	info *tencentscf.FunctionInfo
	err  error
}

func (f *fakeSCFClient) GetFunction(ctx context.Context, _ tencentscf.FunctionRef) (*tencentscf.FunctionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	if f.respectContext && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if len(f.getResults) > 0 {
		result := f.getResults[0]
		f.getResults = f.getResults[1:]
		if result.info != nil && result.info.MaxInstanceConcurrency == 0 {
			// Timer fixtures model the SCF readback required by the production
			// fail-closed verifier.
			result.info.MaxInstanceConcurrency = 1
		}
		return result.info, result.err
	}
	timeout := f.currentTimeout
	if timeout == 0 {
		timeout = 15
	}
	return &tencentscf.FunctionInfo{Status: "Active", Environment: f.currentEnvironment, MemorySize: 64, Timeout: timeout, MaxInstanceConcurrency: 1}, nil
}
func (f *fakeSCFClient) CreateFunction(ctx context.Context, req tencentscf.CreateFunctionRequest) (*tencentscf.CreateFunctionResponse, error) {
	f.created = append(f.created, req)
	if f.createWaitForContext {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &tencentscf.CreateFunctionResponse{}, nil
}
func (f *fakeSCFClient) DeleteFunction(context.Context, tencentscf.FunctionRef) error { return nil }
func (f *fakeSCFClient) UpdateFunctionCode(_ context.Context, req tencentscf.UpdateFunctionCodeRequest) (*tencentscf.UpdateFunctionCodeResponse, error) {
	f.updated = append(f.updated, req)
	return &tencentscf.UpdateFunctionCodeResponse{}, nil
}
func (f *fakeSCFClient) UpdateFunctionConfiguration(_ context.Context, req tencentscf.UpdateFunctionConfigurationRequest) (*tencentscf.UpdateFunctionConfigurationResponse, error) {
	f.configured = append(f.configured, req)
	f.currentEnvironment = req.Environment
	if req.Timeout > 0 {
		f.currentTimeout = req.Timeout
	}
	return &tencentscf.UpdateFunctionConfigurationResponse{}, nil
}
func (f *fakeSCFClient) InvokeFunction(context.Context, tencentscf.InvokeFunctionRequest) (*tencentscf.InvokeFunctionResponse, error) {
	return &tencentscf.InvokeFunctionResponse{}, nil
}
func (f *fakeSCFClient) EnsureTimerTrigger(_ context.Context, req tencentscf.TimerTriggerRequest) (*tencentscf.TimerTriggerInfo, error) {
	f.timerEnsures++
	return &tencentscf.TimerTriggerInfo{Name: req.Name, Type: "timer", Cron: req.Cron, Enabled: req.Enabled, AvailableStatus: "Available", Qualifier: req.Qualifier, Message: req.Message}, nil
}

func (f *fakeSCFClient) GetTimerTrigger(_ context.Context, _ tencentscf.FunctionRef, _ string) (*tencentscf.TimerTriggerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.timerErr != nil {
		return nil, f.timerErr
	}
	if f.timerInfoSet {
		return f.timerInfo, nil
	}
	return &tencentscf.TimerTriggerInfo{Name: timerTriggerName, Type: "timer", Cron: "0 * * * * * *", Enabled: true, AvailableStatus: "Available", Qualifier: timerTriggerQualifier, Message: timerTriggerMessage}, nil
}
func (f *fakeSCFClient) DeleteTimerTrigger(context.Context, tencentscf.TimerTriggerRequest) error {
	return nil
}

func newNodeSCFTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(cloudnodeschema.AllSQL()).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	return db
}

func seedSCFAccountAndPackage(t *testing.T, catalog *store.CatalogRepository) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, catalog.UpsertAccount(context.Background(), store.CloudAccount{AccountID: "account-a", Provider: "tencent", CredentialSecretID: "secret", COSRegion: "ap-guangzhou", COSBucket: "bucket", CreateTime: now}))
	for _, spaceID := range []string{"crypto", "crypto"} {
		require.NoError(t, catalog.UpsertPackage(context.Background(), store.FunctionPackage{SpaceID: spaceID, PackageID: "moox-collector_dev", PackageName: "collector", Version: "dev", Runtime: "CustomRuntime", PackageType: "collector", WorkloadType: "market_fetcher", CloudAccountID: "account-a", COSRegion: "ap-guangzhou", COSBucket: "bucket", COSPath: "packages/collector.zip", Status: "available", CreateTime: now}))
	}
}
