package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
		TimeoutThreshold: 30, ProbeEnabled: true, ProbeUrl: "https://probe",
	})
	assert.Equal(t, "market_fetcher", got["biz_type"])
	assert.Equal(t, true, got["probe_enabled"])
	assert.Empty(t, nodeMetadataFromPB(nil))
}

func TestUpdateNodeAndConversion(t *testing.T) {
	catalog := newCatalogForAccountTests(t)
	svc := &Service{catalog: catalog}
	ctx := spacecontext.WithSpaceID(context.Background(), "crypto_market")
	require.NoError(t, catalog.UpsertNode(ctx, store.CloudNode{
		SpaceID: "crypto_market", NodeID: "node-a", CloudAccountID: "acct-1", Region: "ap-guangzhou",
	}))
	rsp, err := svc.UpdateNode(ctx, &pb.UpdateNodeReq{Node: &pb.CloudNode{
		NodeId: "node-a", Region: "ap-shanghai",
	}})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	node, err := catalog.GetNode(ctx, "crypto_market", "node-a")
	require.NoError(t, err)
	assert.Equal(t, "ap-shanghai", node.Region)

	pbNode := toPBNode(store.CloudNode{SpaceID: "crypto_market", NodeID: "n1", Metadata: `{"biz_type":"market_fetcher"}`})
	assert.Equal(t, "market_fetcher", pbNode.GetBizType())
}

func TestExecuteCreateNodeItemCreatesShortLivedFunction(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{
		{err: errors.New("ResourceNotFound.FunctionName")},
		{info: &tencentscf.FunctionInfo{Status: "Active", MemorySize: 64, Timeout: 15, Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "moox-collector_dev", "MOOX_SPACE_ID": "crypto_market"}}},
	}}
	svc := &Service{
		catalog:            catalog,
		credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}},
		scfClientFactory:   func(cloudcredential.TencentCredential) scfProvisioner { return fake },
	}
	metadata, err := structpb.NewStruct(map[string]any{"biz_type": "market_fetcher", "function_name_prefix": "moox-fetcher-crypto-market"})
	require.NoError(t, err)
	_, err = svc.executeCreateNodeItem(context.Background(), "crypto_market", &pb.NodeCreateItem{
		CloudAccountId: "account-a", Region: "ap-singapore", PackageId: "moox-collector_dev", Runtime: "CustomRuntime", Handler: "main",
		Config:      map[string]string{"memory_size": "64", "timeout": "15"},
		Environment: map[string]string{"MOOX_SPACE_ID": "crypto_market"}, Metadata: metadata,
	}, 0)
	require.NoError(t, err)
	require.Len(t, fake.created, 1)
	assert.Equal(t, int64(64), fake.created[0].MemorySize)
	assert.Equal(t, int64(15), fake.created[0].Timeout)
	assert.NotContains(t, fake.created[0].Environment, "MOOX_MONITOR_READY_URL")
}

func TestExecuteDeployNodeItemUpdatesConfiguration(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{
		SpaceID: "crypto_market", NodeID: "node-a", CloudAccountID: "account-a", PackageID: "old-package", NodeType: "scf-event", Provider: "tencent-scf",
		Region: "ap-singapore", Namespace: "collector", FunctionName: "fetcher-0", Metadata: `{"biz_type":"market_fetcher","handler":"main"}`,
	}))
	fake := &fakeSCFClient{currentEnvironment: map[string]string{"MOOX_CODE_PACKAGE_ID": "old-package"}}
	svc := &Service{
		catalog:            catalog,
		credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}},
		scfClientFactory:   func(cloudcredential.TencentCredential) scfProvisioner { return fake },
	}
	_, err := svc.executeDeployNodeItem(context.Background(), "crypto_market", &pb.NodeDeployItem{
		NodeId: "node-a", PackageId: "moox-collector_dev", Config: map[string]string{"memory_size": "64", "timeout": "15"},
		Environment: map[string]string{"MOOX_SPACE_ID": "crypto_market"},
	})
	require.NoError(t, err)
	require.Len(t, fake.updated, 1)
	require.Len(t, fake.configured, 1)
	assert.Equal(t, int64(64), fake.configured[0].MemorySize)
	assert.Equal(t, int64(15), fake.configured[0].Timeout)
	assert.Equal(t, "moox-collector_dev", fake.configured[0].Environment["MOOX_CODE_PACKAGE_ID"])
}

func TestExecuteDeployNodeItemRejectsMergedTimerEnvironmentOverLimit(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{
		SpaceID: "crypto_market", NodeID: "timer-node", CloudAccountID: "account-a", PackageID: "old-package", NodeType: "scf-event", TriggerType: "timer", Provider: "tencent-scf",
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
	_, err := svc.executeDeployNodeItem(context.Background(), "crypto_market", &pb.NodeDeployItem{
		NodeId: "timer-node", PackageId: "moox-collector_dev", Environment: map[string]string{"MOOX_SPACE_ID": "crypto_market"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "environment is")
	assert.Empty(t, fake.updated)
	assert.Empty(t, fake.configured)
}

type fakeSCFClient struct {
	getResults           []fakeSCFGetResult
	getCalls             int
	respectContext       bool
	createErr            error
	createWaitForContext bool
	created              []tencentscf.CreateFunctionRequest
	updated              []tencentscf.UpdateFunctionCodeRequest
	configured           []tencentscf.UpdateFunctionConfigurationRequest
	currentEnvironment   map[string]string
}

type fakeSCFGetResult struct {
	info *tencentscf.FunctionInfo
	err  error
}

func (f *fakeSCFClient) GetFunction(ctx context.Context, _ tencentscf.FunctionRef) (*tencentscf.FunctionInfo, error) {
	f.getCalls++
	if f.respectContext && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if len(f.getResults) > 0 {
		result := f.getResults[0]
		f.getResults = f.getResults[1:]
		return result.info, result.err
	}
	return &tencentscf.FunctionInfo{Status: "Active", Environment: f.currentEnvironment, MemorySize: 64, Timeout: 15}, nil
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
	return &tencentscf.UpdateFunctionConfigurationResponse{}, nil
}
func (f *fakeSCFClient) InvokeFunction(context.Context, tencentscf.InvokeFunctionRequest) (*tencentscf.InvokeFunctionResponse, error) {
	return &tencentscf.InvokeFunctionResponse{}, nil
}
func (f *fakeSCFClient) EnsureTimerTrigger(_ context.Context, req tencentscf.TimerTriggerRequest) (*tencentscf.TimerTriggerInfo, error) {
	return &tencentscf.TimerTriggerInfo{Name: req.Name, Cron: req.Cron, Enabled: req.Enabled, AvailableStatus: "Available", Qualifier: req.Qualifier, Message: req.Message}, nil
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
	for _, spaceID := range []string{"crypto", "crypto_market"} {
		require.NoError(t, catalog.UpsertPackage(context.Background(), store.FunctionPackage{SpaceID: spaceID, PackageID: "moox-collector_dev", PackageName: "collector", Version: "dev", Runtime: "CustomRuntime", PackageType: "collector", WorkloadType: "market_fetcher", CloudAccountID: "account-a", COSRegion: "ap-guangzhou", COSBucket: "bucket", COSPath: "packages/collector.zip", Status: "available", CreateTime: now}))
	}
}
