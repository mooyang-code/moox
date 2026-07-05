package rpc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	tencentscf "github.com/mooyang-code/moox/modules/cloudnode/internal/providers/tencent-scf"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/repository"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/spacecontext"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	cloudnodeschema "github.com/mooyang-code/moox/modules/cloudnode/schema"
	"google.golang.org/protobuf/types/known/structpb"
	"gorm.io/gorm"
)

func TestBatchCreateNodesCreatesTencentSCFFunctionFromPackage(t *testing.T) {
	db := newNodeSCFTestDB(t)
	catalog := repository.NewCatalogRepository(db)
	seedSCFAccountAndPackage(t, catalog)
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{
		{err: errors.New("ResourceNotFound.FunctionName")},
		{info: &tencentscf.FunctionInfo{
			Status:      "Active",
			ClsLogsetID: "logset-created",
			ClsTopicID:  "topic-created",
		}},
	}}
	svc := &Service{
		catalog: catalog,
		scfClientFactory: func(account repository.CloudAccount) scfProvisioner {
			if account.SecretID != "secret-id" || account.SecretKey != "secret-key" {
				t.Fatalf("account credentials were not passed to provider")
			}
			return fake
		},
	}
	metadata, err := structpb.NewStruct(map[string]any{
		"function_name_prefix": "moox-collector",
		"supported_workloads":  []any{"collect.kline", "collect.symbol"},
	})
	if err != nil {
		t.Fatalf("metadata: %v", err)
	}

	rsp, err := svc.BatchCreateNodes(spacecontext.WithSpaceID(context.Background(), "crypto"), &pb.BatchCreateNodesReq{
		Nodes: []*pb.NodeCreateItem{{
			CloudAccountId: "account-a",
			NodeType:       "scf-event",
			Runtime:        "CustomRuntime",
			Handler:        "main",
			Config: map[string]string{
				"timeout":       "60",
				"memory_size":   "256",
				"cls_logset_id": "logset-config",
				"cls_topic_id":  "topic-config",
			},
			Environment: map[string]string{"MOOX_ENV": "prod"},
			Region:      "ap-guangzhou",
			Namespace:   "collector",
			PackageId:   "moox-collector_dev",
			Metadata:    metadata,
		}},
	})
	if err != nil {
		t.Fatalf("BatchCreateNodes transport error = %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("ret = %v %s", rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	}
	if len(fake.created) != 1 {
		t.Fatalf("created calls = %d, want 1", len(fake.created))
	}
	create := fake.created[0]
	if create.FunctionName != "moox-collector-ap-guangzhou-0" || create.Namespace != "collector" || create.Region != "ap-guangzhou" {
		t.Fatalf("function ref = %#v", create.FunctionRef)
	}
	if create.COSBucket != "moox-scf-1255382561" || create.COSRegion != "ap-guangzhou" || create.COSObject != "moox/cloud-packages/collector/moox-collector/dev/collector-scf.zip" {
		t.Fatalf("cos package = bucket:%q region:%q object:%q", create.COSBucket, create.COSRegion, create.COSObject)
	}
	if create.Runtime != "CustomRuntime" || create.Handler != "main" || create.Timeout != 60 || create.MemorySize != 256 {
		t.Fatalf("runtime config = %#v", create)
	}
	if create.ClsLogsetID != "logset-config" || create.ClsTopicID != "topic-config" {
		t.Fatalf("cls config = %q/%q", create.ClsLogsetID, create.ClsTopicID)
	}
	if create.Environment["MOOX_ENV"] != "prod" {
		t.Fatalf("env = %#v", create.Environment)
	}

	node, err := catalog.GetNode(context.Background(), "crypto", "moox-collector-ap-guangzhou-0")
	if err != nil {
		t.Fatalf("load node: %v", err)
	}
	if node == nil {
		t.Fatalf("node was not persisted")
	}
	if got := parseStringSliceJSON(node.SupportedWorkloads); strings.Join(got, ",") != "collect.kline,collect.symbol" {
		t.Fatalf("supported workloads = %#v", got)
	}
	nodeMetadata := parseJSONMap(node.Metadata)
	if got := metadataString(nodeMetadata, "cls_logset_id"); got != "logset-created" {
		t.Fatalf("metadata cls_logset_id = %q, want logset-created", got)
	}
	if got := metadataString(nodeMetadata, "cls_topic_id"); got != "topic-created" {
		t.Fatalf("metadata cls_topic_id = %q, want topic-created", got)
	}
	listRsp, err := svc.GetNodeList(spacecontext.WithSpaceID(context.Background(), "crypto"), &pb.GetNodeListReq{})
	if err != nil {
		t.Fatalf("GetNodeList transport error = %v", err)
	}
	if len(listRsp.GetItems()) != 1 || listRsp.GetItems()[0].GetClsTopicId() != "topic-created" {
		t.Fatalf("list cls_topic_id = %#v", listRsp.GetItems())
	}
}

func TestBatchDeployNodesUpdatesTencentSCFFunctionCodeFromPackage(t *testing.T) {
	db := newNodeSCFTestDB(t)
	catalog := repository.NewCatalogRepository(db)
	seedSCFAccountAndPackage(t, catalog)
	if err := catalog.UpsertNode(context.Background(), repository.CloudNode{
		SpaceID:        "crypto",
		NodeID:         "node-a",
		CloudAccountID: "account-a",
		PackageID:      "old-package",
		NodeType:       "scf-event",
		Provider:       "tencent-scf",
		Region:         "ap-guangzhou",
		Namespace:      "collector",
		FunctionName:   "moox-collector-ap-guangzhou-0",
		Metadata:       `{"handler":"main"}`,
		Status:         "unknown",
	}); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	fake := &fakeSCFClient{}
	svc := &Service{
		catalog: catalog,
		scfClientFactory: func(account repository.CloudAccount) scfProvisioner {
			return fake
		},
	}

	rsp, err := svc.BatchDeployNodes(spacecontext.WithSpaceID(context.Background(), "crypto"), &pb.BatchDeployNodesReq{
		Deployments: []*pb.NodeDeployItem{{
			NodeId:    "node-a",
			PackageId: "moox-collector_dev",
		}},
	})
	if err != nil {
		t.Fatalf("BatchDeployNodes transport error = %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("ret = %v %s", rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	}
	if len(fake.updated) != 1 {
		t.Fatalf("updated calls = %d, want 1", len(fake.updated))
	}
	update := fake.updated[0]
	if update.FunctionName != "moox-collector-ap-guangzhou-0" || update.Namespace != "collector" || update.Region != "ap-guangzhou" {
		t.Fatalf("function ref = %#v", update.FunctionRef)
	}
	if update.COSBucket != "moox-scf-1255382561" || update.COSRegion != "ap-guangzhou" || update.COSObject != "moox/cloud-packages/collector/moox-collector/dev/collector-scf.zip" {
		t.Fatalf("cos package = bucket:%q region:%q object:%q", update.COSBucket, update.COSRegion, update.COSObject)
	}
	if update.Handler != "main" {
		t.Fatalf("handler = %q", update.Handler)
	}
}

type fakeSCFClient struct {
	getErr     error
	getResults []fakeSCFGetResult
	created    []tencentscf.CreateFunctionRequest
	updated    []tencentscf.UpdateFunctionCodeRequest
}

type fakeSCFGetResult struct {
	info *tencentscf.FunctionInfo
	err  error
}

func (f *fakeSCFClient) GetFunction(context.Context, tencentscf.FunctionRef) (*tencentscf.FunctionInfo, error) {
	if len(f.getResults) > 0 {
		result := f.getResults[0]
		f.getResults = f.getResults[1:]
		if result.err != nil {
			return nil, result.err
		}
		return result.info, nil
	}
	if f.getErr != nil {
		return nil, f.getErr
	}
	return &tencentscf.FunctionInfo{Status: "Active"}, nil
}

func (f *fakeSCFClient) CreateFunction(_ context.Context, req tencentscf.CreateFunctionRequest) (*tencentscf.CreateFunctionResponse, error) {
	f.created = append(f.created, req)
	return &tencentscf.CreateFunctionResponse{RequestID: "create-req"}, nil
}

func (f *fakeSCFClient) UpdateFunctionCode(_ context.Context, req tencentscf.UpdateFunctionCodeRequest) (*tencentscf.UpdateFunctionCodeResponse, error) {
	f.updated = append(f.updated, req)
	return &tencentscf.UpdateFunctionCodeResponse{RequestID: "update-req"}, nil
}

func newNodeSCFTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Exec(cloudnodeschema.AllSQL()).Error; err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return db
}

func seedSCFAccountAndPackage(t *testing.T, catalog *repository.CatalogRepository) {
	t.Helper()
	now := time.Now().UTC()
	if err := catalog.UpsertAccount(context.Background(), repository.CloudAccount{
		AccountID:   "account-a",
		AccountName: "test-account",
		Provider:    "tencent",
		SecretID:    "secret-id",
		SecretKey:   "secret-key",
		AppID:       "1255382561",
		COSRegion:   "ap-guangzhou",
		COSBucket:   "moox-scf-1255382561",
		CreateTime:  now,
	}); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if err := catalog.UpsertPackage(context.Background(), repository.FunctionPackage{
		SpaceID:        "crypto",
		PackageID:      "moox-collector_dev",
		PackageName:    "moox-collector",
		Version:        "dev",
		Runtime:        "CustomRuntime",
		PackageType:    "collector",
		WorkloadType:   "data_collector",
		CloudAccountID: "account-a",
		COSRegion:      "ap-guangzhou",
		COSBucket:      "moox-scf-1255382561",
		COSPath:        "moox/cloud-packages/collector/moox-collector/dev/collector-scf.zip",
		Status:         "available",
		CreateTime:     now,
	}); err != nil {
		t.Fatalf("seed package: %v", err)
	}
}
