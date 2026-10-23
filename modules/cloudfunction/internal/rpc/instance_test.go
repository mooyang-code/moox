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
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/cloudcredential"
	tencentscf "github.com/mooyang-code/moox/modules/cloudfunction/internal/providers/tencentscf"
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/spacecontext"
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudfunction/proto/cloudfunctiongen"
	cloudfunctionschema "github.com/mooyang-code/moox/modules/cloudfunction/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
	"gorm.io/gorm"
)

func TestListFunctionInstancesRefreshesTimerTriggerReadback(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	require.NoError(t, catalog.UpsertInstance(context.Background(), store.FunctionInstance{
		SpaceID: "crypto", InstanceID: "timer-instance", CloudAccountID: "account-a", PackageID: "pkg", DeploymentID: "deployment-1",
		FunctionType: "scf-event", TriggerType: "timer", Region: "ap-guangzhou", FunctionName: "timer-instance",
		Metadata: `{"deployment_ready":true,"timer_enabled":true,"timer_cron":"0 * * * * * *","timer_available_status":"Available"}`,
	}))
	fake := &fakeSCFClient{timerInfoSet: true, timerInfo: &tencentscf.TimerTriggerInfo{Name: timerTriggerName, Type: "timer", Cron: "0 */5 * * * * *", Enabled: false, AvailableStatus: "Available", Qualifier: timerTriggerQualifier, Message: timerTriggerMessage}}
	svc := &Service{catalog: catalog, credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}}, scfClientFactory: func(cloudcredential.TencentCredential) scfProvisioner { return fake }}
	rsp, err := svc.ListFunctionInstances(spacecontext.WithSpaceID(context.Background(), "crypto"), &pb.ListFunctionInstancesReq{TriggerType: "timer"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetItems(), 1)
	metadata := rsp.GetItems()[0].GetMetadata().AsMap()
	assert.Equal(t, false, metadata["timer_actual_enabled"])
	assert.Equal(t, "0 */5 * * * * *", metadata["timer_actual_cron"])
	assert.Equal(t, "Available", metadata["timer_available_status"])
	stored, err := catalog.GetInstance(context.Background(), "crypto", "timer-instance")
	require.NoError(t, err)
	assert.Contains(t, stored.Metadata, "timer_actual_enabled")
}

func TestListFunctionInstancesSkipsTimerReadbackForCollectorSnapshot(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	require.NoError(t, catalog.UpsertInstance(context.Background(), store.FunctionInstance{
		SpaceID: "crypto", InstanceID: "timer-instance", CloudAccountID: "account-a", PackageID: "pkg", DeploymentID: "deployment-1",
		FunctionType: "scf-event", TriggerType: "timer", Region: "ap-guangzhou", FunctionName: "timer-instance",
		Metadata: `{"workload_type":"market_fetcher","deployment_ready":true,"timer_enabled":true}`,
	}))
	fake := &fakeSCFClient{timerInfoSet: true, timerInfo: &tencentscf.TimerTriggerInfo{Name: timerTriggerName, Type: "timer", Cron: "0 */5 * * * * *", Enabled: false, AvailableStatus: "Available"}}
	svc := &Service{catalog: catalog, credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}}, scfClientFactory: func(cloudcredential.TencentCredential) scfProvisioner { return fake }}

	rsp, err := svc.ListFunctionInstances(spacecontext.WithSpaceID(context.Background(), "crypto"), &pb.ListFunctionInstancesReq{WorkloadType: "market_fetcher", TriggerType: "timer"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetItems(), 1)
	assert.Equal(t, 0, fake.getCalls, "collector catalog snapshots must not wait for Tencent SCF readback")
}

func TestExecuteCreateInstanceItemCreatesShortLivedFunction(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
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
	metadata, err := structpb.NewStruct(map[string]any{"workload_type": "market_fetcher", "function_name_prefix": "moox-fetcher-crypto-market"})
	require.NoError(t, err)
	_, err = svc.executeCreateInstanceItem(context.Background(), "crypto", &pb.FunctionInstanceCreateItem{
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

func TestExecuteCreatePersistsDiscoverableReservationBeforeSCFAndNeverMarksFailedRowReady(t *testing.T) {
	db := newInstanceSCFTestDB(t)
	catalog := store.NewCatalogRepository(db)
	seedSCFAccountAndPackage(t, catalog)
	require.NoError(t, db.Exec(`CREATE TRIGGER fail_ready_cloud_instance BEFORE INSERT ON t_cloud_function_instances
		WHEN json_extract(NEW.c_metadata, '$.deployment_ready') = 1
		BEGIN SELECT RAISE(ABORT, 'ready persistence failed'); END;
		CREATE TRIGGER fail_ready_cloud_instance_update BEFORE UPDATE ON t_cloud_function_instances
		WHEN json_extract(NEW.c_metadata, '$.deployment_ready') = 1
		BEGIN SELECT RAISE(ABORT, 'ready persistence failed'); END`).Error)
	createSawReservation := false
	fake := &fakeSCFClient{
		getResults: []fakeSCFGetResult{
			{err: errors.New("ResourceNotFound.FunctionName")},
			{info: &tencentscf.FunctionInfo{Status: "Active", MemorySize: 64, Timeout: 15, MaxInstanceConcurrency: 1, Environment: completeInvokeEnvironment("15")}},
		},
		onCreate: func(req tencentscf.CreateFunctionRequest) {
			row, err := catalog.GetInstance(context.Background(), "crypto", req.FunctionName)
			if err == nil && row != nil {
				createSawReservation = !metadataBool(parseJSONMap(row.Metadata), "deployment_ready")
			}
		},
	}
	service := &Service{
		catalog:            catalog,
		credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}},
		scfClientFactory:   func(cloudcredential.TencentCredential) scfProvisioner { return fake },
	}
	metadata, err := structpb.NewStruct(map[string]any{"workload_type": "market_fetcher", "function_name_prefix": "moox-fetcher-crypto-reservation"})
	require.NoError(t, err)
	_, err = service.executeCreateInstanceItem(context.Background(), "crypto", &pb.FunctionInstanceCreateItem{
		CloudAccountId: "account-a", Region: "ap-singapore", Namespace: "collector", PackageId: "moox-collector_dev", Runtime: "CustomRuntime", Handler: "main",
		Config: map[string]string{"memory_size": "64", "timeout": "15"}, Environment: completeInvokeEnvironment("15"), Metadata: metadata,
	}, 0)
	require.ErrorContains(t, err, "ready persistence failed")
	require.Len(t, fake.created, 1)
	require.True(t, createSawReservation, "catalog identity must be durable and not-ready before the SCF create side effect")
	row, err := catalog.GetInstance(context.Background(), "crypto", fake.created[0].FunctionName)
	require.NoError(t, err)
	require.NotNil(t, row, "failed final persistence must leave a discoverable cleanup reservation")
	require.False(t, metadataBool(parseJSONMap(row.Metadata), "deployment_ready"))
	require.True(t, metadataBool(parseJSONMap(row.Metadata), "collector_publish_fenced"))
	fake.currentEnvironment = map[string]string{
		"MOOX_CODE_PACKAGE_ID":       "moox-collector_dev",
		"MOOX_CREATE_RESERVATION_ID": row.LifecycleID,
	}
	_, err = service.executeDeleteInstanceItem(context.Background(), "crypto", instanceDeleteItemForTest(t, catalog, "crypto", row.InstanceID))
	require.NoError(t, err, "the exact reservation must be cleanable after provider ownership is verified")
	require.Len(t, fake.deletedFunctions, 1)
	deletedRow, err := catalog.GetInstanceIncludingDeleted(context.Background(), "crypto", row.InstanceID)
	require.NoError(t, err)
	require.True(t, deletedRow.IsDeleted)
}

func TestExecuteCreateDoesNotCallSCFWhenReservationCannotBePersisted(t *testing.T) {
	db := newInstanceSCFTestDB(t)
	catalog := store.NewCatalogRepository(db)
	seedSCFAccountAndPackage(t, catalog)
	require.NoError(t, db.Exec(`CREATE TRIGGER reject_create_reservation BEFORE INSERT ON t_cloud_function_instances
		BEGIN SELECT RAISE(ABORT, 'reservation unavailable'); END`).Error)
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{{err: errors.New("ResourceNotFound.FunctionName")}}}
	service := &Service{
		catalog:            catalog,
		credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}},
		scfClientFactory:   func(cloudcredential.TencentCredential) scfProvisioner { return fake },
	}
	metadata, err := structpb.NewStruct(map[string]any{"workload_type": "market_fetcher", "function_name_prefix": "moox-fetcher-crypto-reservation-failure"})
	require.NoError(t, err)
	_, err = service.executeCreateInstanceItem(context.Background(), "crypto", &pb.FunctionInstanceCreateItem{
		CloudAccountId: "account-a", Region: "ap-singapore", PackageId: "moox-collector_dev", Runtime: "CustomRuntime", Handler: "main",
		Config: map[string]string{"memory_size": "64", "timeout": "15"}, Environment: completeInvokeEnvironment("15"), Metadata: metadata,
	}, 0)
	require.ErrorContains(t, err, "reservation unavailable")
	require.Empty(t, fake.created, "the external SCF side effect must follow a durable reservation")
}

func TestExecuteCreateInvokeMarketFetcherPreservesConfiguredTimeout(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
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
	metadata, err := structpb.NewStruct(map[string]any{"workload_type": "market_fetcher", "function_name_prefix": "moox-fetcher-crypto-market"})
	require.NoError(t, err)
	_, err = svc.executeCreateInstanceItem(context.Background(), "crypto", &pb.FunctionInstanceCreateItem{
		CloudAccountId: "account-a", Region: "ap-singapore", PackageId: "moox-collector_dev", Runtime: "CustomRuntime", Handler: "main",
		TriggerType: "invoke", Config: map[string]string{"memory_size": "64", "timeout": "60"},
		Environment: environment, Metadata: metadata,
	}, 0)
	require.NoError(t, err)
	require.Len(t, fake.created, 1)
	assert.Equal(t, int64(60), fake.created[0].Timeout)
}

func TestExecuteCreateTimerMarketFetcherKeepsAuthoritativeTimeout(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
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
	metadata, err := structpb.NewStruct(map[string]any{"workload_type": "market_fetcher", "function_name_prefix": "moox-fetcher-crypto-market"})
	require.NoError(t, err)
	_, err = svc.executeCreateInstanceItem(context.Background(), "crypto", &pb.FunctionInstanceCreateItem{
		CloudAccountId: "account-a", Region: "ap-singapore", PackageId: "moox-collector_dev", Runtime: "CustomRuntime", Handler: "main",
		TriggerType: "timer", Config: map[string]string{"memory_size": "64", "timeout": "60"},
		Environment: environment, Metadata: metadata,
	}, 0)
	require.NoError(t, err)
	require.Len(t, fake.created, 1)
	assert.Equal(t, int64(60), fake.created[0].Timeout)
}

func TestExecuteCreateDurableTimerRejectsRuntimeTimeoutMismatchBeforeCloudAPI(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	factoryCalls := 0
	svc := &Service{catalog: catalog,
		credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}},
		scfClientFactory:   func(cloudcredential.TencentCredential) scfProvisioner { factoryCalls++; return &fakeSCFClient{} },
	}
	metadata, err := structpb.NewStruct(map[string]any{"workload_type": "market_fetcher", "function_mode": "kline"})
	require.NoError(t, err)
	_, err = svc.executeCreateInstanceItem(context.Background(), "crypto", &pb.FunctionInstanceCreateItem{
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
			instance := &store.FunctionInstance{TriggerType: "timer", Metadata: fmt.Sprintf(`{"workload_type":"market_fetcher","function_mode":%q}`, test.mode)}
			err := validateMarketFetchTimerTimeout(instance, store.FunctionPackage{WorkloadType: "market_fetcher"}, map[string]string{"MOOX_FETCH_TIMEOUT_SECONDS": test.runtime}, configInt64(map[string]string{"timeout": test.outer}, "timeout", 60))
			if test.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestExecuteCreateLegacyInstrumentModeUsesTimerTimeout(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
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
	metadata, err := structpb.NewStruct(map[string]any{"workload_type": "market_fetcher", "function_mode": "instrument_snapshot", "function_name_prefix": "moox-fetcher-stockcn-instrument"})
	require.NoError(t, err)
	_, err = svc.executeCreateInstanceItem(context.Background(), "stockcn", &pb.FunctionInstanceCreateItem{
		CloudAccountId: "account-a", Region: "ap-chengdu", PackageId: "moox-collector_dev", Runtime: "CustomRuntime", Handler: "main",
		TriggerType: "timer", Config: map[string]string{"memory_size": "64", "timeout": "15"},
		Environment: map[string]string{"MOOX_SPACE_ID": "stockcn"}, Metadata: metadata,
	}, 0)
	require.NoError(t, err)
	require.Len(t, fake.created, 1)
	assert.Equal(t, int64(15), fake.created[0].Timeout)
}

func TestExecuteCreateInstanceItemRejectsEnvironmentOverLimitBeforeCreate(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{{err: errors.New("ResourceNotFound.FunctionName")}}}
	svc := &Service{
		catalog:            catalog,
		credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}},
		scfClientFactory:   func(cloudcredential.TencentCredential) scfProvisioner { return fake },
	}
	metadata, err := structpb.NewStruct(map[string]any{"workload_type": "market_fetcher", "function_name_prefix": "moox-fetcher-crypto-market"})
	require.NoError(t, err)
	_, err = svc.executeCreateInstanceItem(context.Background(), "crypto", &pb.FunctionInstanceCreateItem{
		CloudAccountId: "account-a", Region: "ap-singapore", PackageId: "moox-collector_dev", Runtime: "CustomRuntime", Handler: "main",
		TriggerType: "timer", Config: map[string]string{"memory_size": "64", "timeout": "60"},
		Environment: map[string]string{"MOOX_SPACE_ID": strings.Repeat("x", maxSCFEnvironmentBytes)}, Metadata: metadata,
	}, 0)
	require.ErrorContains(t, err, "environment is")
	assert.Empty(t, fake.created)
}

func TestExecuteCreateExistingTimerInstanceEnsuresTrigger(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	environment := completeTimerEnvironment()
	environment["MOOX_CODE_PACKAGE_ID"] = "moox-collector_dev"
	environment["MOOX_CREATE_RESERVATION_ID"] = "timer-create-reservation"
	// 函数已存在但还没有定时器：补建一个停用状态的初始定时器。
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{{info: &tencentscf.FunctionInfo{Status: "Active", MemorySize: 64, Timeout: 60, Environment: environment}}}, timerInfoSet: true}
	svc := &Service{
		catalog:            catalog,
		credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}},
		scfClientFactory:   func(cloudcredential.TencentCredential) scfProvisioner { return fake },
	}
	metadata, err := structpb.NewStruct(map[string]any{"workload_type": "market_fetcher", "function_name_prefix": "moox-fetcher-crypto-market"})
	require.NoError(t, err)
	_, err = svc.executeCreateInstanceItem(context.Background(), "crypto", &pb.FunctionInstanceCreateItem{
		CloudAccountId: "account-a", Region: "ap-singapore", Namespace: "collector", PackageId: "moox-collector_dev", Runtime: "CustomRuntime", Handler: "main",
		TriggerType: "timer", Config: map[string]string{"memory_size": "64", "timeout": "60"}, Environment: environment, Metadata: metadata,
		CreateReservationId: "timer-create-reservation",
	}, 0)
	require.NoError(t, err)
	require.Equal(t, 1, fake.timerEnsures)
}

func TestExecuteCreateExistingTimerInstanceKeepsAnExistingTrigger(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	environment := completeTimerEnvironment()
	environment["MOOX_CODE_PACKAGE_ID"] = "moox-collector_dev"
	environment["MOOX_CREATE_RESERVATION_ID"] = "timer-create-reservation"
	// 默认的假客户端返回一个已启用的定时器：部署或续建不能把它重置成停用。
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{{info: &tencentscf.FunctionInfo{Status: "Active", MemorySize: 64, Timeout: 60, Environment: environment}}}}
	svc := &Service{
		catalog:            catalog,
		credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}},
		scfClientFactory:   func(cloudcredential.TencentCredential) scfProvisioner { return fake },
	}
	metadata, err := structpb.NewStruct(map[string]any{"workload_type": "market_fetcher", "function_name_prefix": "moox-fetcher-crypto-market"})
	require.NoError(t, err)
	_, err = svc.executeCreateInstanceItem(context.Background(), "crypto", &pb.FunctionInstanceCreateItem{
		CloudAccountId: "account-a", Region: "ap-singapore", Namespace: "collector", PackageId: "moox-collector_dev", Runtime: "CustomRuntime", Handler: "main",
		TriggerType: "timer", Config: map[string]string{"memory_size": "64", "timeout": "60"}, Environment: environment, Metadata: metadata,
		CreateReservationId: "timer-create-reservation",
	}, 0)
	require.NoError(t, err)
	require.Zero(t, fake.timerEnsures)
}

func TestExecuteDeployInstanceItemUpdatesConfiguration(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	require.NoError(t, catalog.UpsertInstance(context.Background(), store.FunctionInstance{
		SpaceID: "crypto", InstanceID: "instance-a", CloudAccountID: "account-a", PackageID: "old-package", FunctionType: "scf-event", Provider: "tencent-scf",
		Region: "ap-singapore", Namespace: "collector", FunctionName: "fetcher-0", Metadata: `{"workload_type":"market_fetcher","handler":"main"}`,
	}))
	remote := completeInvokeEnvironment("15")
	fake := &fakeSCFClient{currentEnvironment: remote}
	svc := &Service{
		catalog:            catalog,
		credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}},
		scfClientFactory:   func(cloudcredential.TencentCredential) scfProvisioner { return fake },
	}
	_, err := svc.executeDeployInstanceItem(context.Background(), "crypto", &pb.FunctionInstanceDeployItem{
		InstanceId: "instance-a", PackageId: "moox-collector_dev", Config: map[string]string{"memory_size": "64", "timeout": "15"},
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

func TestExecuteDeployInstanceItemRejectsMergedTimerEnvironmentOverLimit(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	require.NoError(t, catalog.UpsertInstance(context.Background(), store.FunctionInstance{
		SpaceID: "crypto", InstanceID: "timer-instance", CloudAccountID: "account-a", PackageID: "old-package", FunctionType: "scf-event", TriggerType: "timer", Provider: "tencent-scf",
		Region: "ap-singapore", Namespace: "collector", FunctionName: "fetcher-timer", Metadata: `{"workload_type":"market_fetcher","handler":"main"}`,
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
	_, err := svc.executeDeployInstanceItem(context.Background(), "crypto", &pb.FunctionInstanceDeployItem{
		InstanceId: "timer-instance", PackageId: "moox-collector_dev", Environment: map[string]string{"MOOX_SPACE_ID": "crypto"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "environment is")
	assert.Empty(t, fake.updated)
	assert.Empty(t, fake.configured)
}

func TestExecuteRuntimeConfigSkipsUnchangedEnvironmentUpdate(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	require.NoError(t, catalog.UpsertInstance(context.Background(), store.FunctionInstance{
		SpaceID: "crypto", InstanceID: "timer-instance", CloudAccountID: "account-a", PackageID: "pkg", FunctionType: "scf-event", TriggerType: "timer", Provider: "tencent-scf",
		Region: "ap-singapore", Namespace: "collector", FunctionName: "fetcher-timer", Metadata: `{"workload_type":"market_fetcher"}`,
	}))
	environment := completeTimerEnvironment()
	environment["MOOX_CODE_PACKAGE_ID"] = "pkg"
	environment["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"] = "assignment"
	environment["MOOX_MARKET_FETCH_SUBJECT_COUNT"] = "1"
	environment["MOOX_MARKET_FETCH_DNS_HASH"] = "dns"
	environment["MOOX_MARKET_FETCH_DNS_UPDATED_AT"] = "2026-08-04T01:00:00Z"
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
	_, err := svc.executeRuntimeConfigItem(context.Background(), "crypto", &pb.FunctionInstanceRuntimeConfigPatch{
		InstanceId: "timer-instance", ManagedEnvironment: environment, TimerCron: "0 * * * * * *", TimerEnabled: true,
	})
	require.NoError(t, err)
	require.Empty(t, fake.configured, "unchanged managed environment must not call UpdateFunctionConfiguration")
}

func TestExecuteRuntimeConfigDoesNotEnableTimerWithIncompleteDependencies(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	require.NoError(t, catalog.UpsertInstance(context.Background(), store.FunctionInstance{
		SpaceID: "crypto", InstanceID: "timer-instance", CloudAccountID: "account-a", PackageID: "pkg", FunctionType: "scf-event", TriggerType: "timer", Provider: "tencent-scf",
		Region: "ap-singapore", Namespace: "collector", FunctionName: "fetcher-timer", Metadata: `{"workload_type":"market_fetcher"}`,
	}))
	environment := completeTimerEnvironment()
	delete(environment, "MOOX_ACCESS_ADDRESS")
	fake := &fakeSCFClient{currentEnvironment: environment}
	svc := &Service{
		catalog:            catalog,
		credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}},
		scfClientFactory:   func(cloudcredential.TencentCredential) scfProvisioner { return fake },
	}
	_, err := svc.executeRuntimeConfigItem(context.Background(), "crypto", &pb.FunctionInstanceRuntimeConfigPatch{
		InstanceId: "timer-instance", ManagedEnvironment: map[string]string{"MOOX_MARKET_FETCH_BINDING_HASH": "binding"},
		TimerCron: "0 * * * * * *", TimerEnabled: true,
	})
	require.ErrorContains(t, err, "MOOX_ACCESS_ADDRESS")
	require.Empty(t, fake.configured, "invalid runtime dependencies must be rejected before updating configuration")
	require.Zero(t, fake.timerEnsures, "invalid runtime dependencies must not enable the Timer trigger")
}

func TestExecuteRuntimeConfigAppliesClaimRoutingAndTimeout(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	require.NoError(t, catalog.UpsertInstance(context.Background(), store.FunctionInstance{
		SpaceID: "stockcn", InstanceID: "timer-instance", CloudAccountID: "account-a", FunctionType: "scf-event", TriggerType: "timer",
		Region: "ap-guangzhou", Namespace: "collector", FunctionName: "fetcher-timer", Metadata: `{"workload_type":"market_fetcher"}`,
	}))
	environment := completeTimerEnvironment()
	environment["MOOX_SPACE_ID"] = "stockcn"
	environment["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"] = "assignment"
	environment["MOOX_MARKET_FETCH_SUBJECT_COUNT"] = "80"
	environment["MOOX_EVENTBUS_NATS_PASSWORD"] = "keep-password"
	fake := &fakeSCFClient{currentEnvironment: environment}
	svc := &Service{catalog: catalog, credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}}, scfClientFactory: func(cloudcredential.TencentCredential) scfProvisioner { return fake }}
	patch := &pb.FunctionInstanceRuntimeConfigPatch{InstanceId: "timer-instance", TimerCron: "5 * * * * * *", TimerEnabled: true, ManagedEnvironment: map[string]string{
		"MOOX_MARKET_FETCH_ASSIGNMENT_HASH": "assignment", "MOOX_MARKET_FETCH_SUBJECT_COUNT": "80", "MOOX_MARKET_FETCH_BINDING_HASH": "binding", "MOOX_ACCESS_ADDRESS": "access.example:11004", "MOOX_ACCESS_ID": "access@control-a", "MOOX_CALLER": "scf-collector", "MOOX_CALLER_KEY_ID": "assigned-scf-key-18", "MOOX_CALLER_KEY": strings.Repeat("b", 64), "MOOX_FETCH_TIMEOUT_SECONDS": "60",
	}}
	require.Nil(t, svc.preflightRuntimeConfig(context.Background(), "stockcn", patch))
	_, err := svc.executeRuntimeConfigItem(context.Background(), "stockcn", patch)
	require.NoError(t, err)
	require.Len(t, fake.configured, 1)
	require.EqualValues(t, 60, fake.configured[0].Timeout)
	require.Equal(t, "keep-password", fake.currentEnvironment["MOOX_EVENTBUS_NATS_PASSWORD"])
	instance, err := catalog.GetInstance(context.Background(), "stockcn", "timer-instance")
	require.NoError(t, err)
	require.Contains(t, instance.Metadata, `"access_address":"access.example:11004"`)
	require.Contains(t, instance.Metadata, `"access_key_id":"assigned-scf-key-18"`)
	require.NotContains(t, instance.Metadata, strings.Repeat("b", 64))
	require.Contains(t, instance.Metadata, `"timeout_seconds":60`)
	require.Contains(t, instance.Metadata, `"assignment_count":80`, "the assignment readback must persist the verified subject count")
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
	onCreate             func(tencentscf.CreateFunctionRequest)
	created              []tencentscf.CreateFunctionRequest
	deletedFunctions     []tencentscf.FunctionRef
	deleteErr            error
	functionDeleted      bool
	updated              []tencentscf.UpdateFunctionCodeRequest
	configured           []tencentscf.UpdateFunctionConfigurationRequest
	configurationErr     error
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
	if f.functionDeleted {
		return nil, errors.New("ResourceNotFound.FunctionName")
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
	if f.onCreate != nil {
		f.onCreate(req)
	}
	if f.createWaitForContext {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &tencentscf.CreateFunctionResponse{}, nil
}
func (f *fakeSCFClient) DeleteFunction(_ context.Context, ref tencentscf.FunctionRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletedFunctions = append(f.deletedFunctions, ref)
	if f.deleteErr == nil || isSCFNotFound(f.deleteErr) {
		f.functionDeleted = true
	}
	return f.deleteErr
}
func (f *fakeSCFClient) UpdateFunctionCode(_ context.Context, req tencentscf.UpdateFunctionCodeRequest) (*tencentscf.UpdateFunctionCodeResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updated = append(f.updated, req)
	return &tencentscf.UpdateFunctionCodeResponse{}, nil
}
func (f *fakeSCFClient) UpdateFunctionConfiguration(_ context.Context, req tencentscf.UpdateFunctionConfigurationRequest) (*tencentscf.UpdateFunctionConfigurationResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configured = append(f.configured, req)
	if f.configurationErr != nil {
		return nil, f.configurationErr
	}
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
	f.mu.Lock()
	defer f.mu.Unlock()
	f.timerEnsures++
	if f.timerErr != nil {
		return nil, f.timerErr
	}
	return &tencentscf.TimerTriggerInfo{Name: req.Name, Type: "timer", Cron: req.Cron, Enabled: req.Enabled, AvailableStatus: "Available", Qualifier: req.Qualifier, Message: req.Message}, nil
}

func (f *fakeSCFClient) GetTimerTrigger(_ context.Context, _ tencentscf.FunctionRef, _ string) (*tencentscf.TimerTriggerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.timerInfoSet {
		return f.timerInfo, nil
	}
	if f.timerErr != nil {
		return nil, f.timerErr
	}
	return &tencentscf.TimerTriggerInfo{Name: timerTriggerName, Type: "timer", Cron: "0 * * * * * *", Enabled: true, AvailableStatus: "Available", Qualifier: timerTriggerQualifier, Message: timerTriggerMessage}, nil
}
func (f *fakeSCFClient) DeleteTimerTrigger(context.Context, tencentscf.TimerTriggerRequest) error {
	return nil
}

func newInstanceSCFTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(cloudfunctionschema.AllSQL()).Error)
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
