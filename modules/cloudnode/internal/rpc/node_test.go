package rpc

import (
	"context"
	"testing"
	"time"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobstate"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"google.golang.org/protobuf/types/known/structpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"errors"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/spacecontext"
	"strings"
	"github.com/glebarez/sqlite"
	tencentscf "github.com/mooyang-code/moox/modules/cloudnode/internal/providers/tencent-scf"
	cloudnodeschema "github.com/mooyang-code/moox/modules/cloudnode/schema"
	"gorm.io/gorm"
)

func TestReportHeartbeatEnqueuesHeartbeatSink(t *testing.T) {
	sink := &fakeHeartbeatSink{}
	svc := &Service{heartbeatSink: sink}

	rsp, err := svc.ReportHeartbeat(context.Background(), &pb.ReportHeartbeatReq{
		SpaceId:        "crypto",
		NodeId:         "node-1",
		NodeType:       "scf-event",
		RunningVersion: "v1",
	})
	if err != nil {
		t.Fatalf("ReportHeartbeat transport error = %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("ret = %+v", rsp.GetRetInfo())
	}
	if len(sink.items) != 1 || sink.items[0].GetNodeId() != "node-1" {
		t.Fatalf("sink items = %+v", sink.items)
	}
}

func TestReportHeartbeatReturnsCancelDirectiveFromActiveKV(t *testing.T) {
	ctx := context.Background()
	rt, _ := startRPCQueueRuntime(t)
	kv, err := rt.JetStream().KeyValue(config.Default().JobItem.ActiveKVBucket)
	if err != nil {
		t.Fatalf("KeyValue() error = %v", err)
	}
	stateStore := jobstate.NewKVStore(kv, jobstate.Options{
		RecoverAfterMillis: int64(time.Minute / time.Millisecond),
		DefaultMaxAttempts: 3,
	})
	params, _ := structpb.NewStruct(map[string]any{"symbol": "BTCUSDT"})
	if _, err := stateStore.CreatePending(ctx, &pb.JobItem{
		SpaceId:       "crypto",
		JobId:         "job-1",
		JobItemId:     "ji-kv-directive",
		JobType:       "collect.kline",
		CodePackageId: "collector-scf",
		Params:        params,
	}, jobstate.QueueMeta{}); err != nil {
		t.Fatalf("CreatePending() error = %v", err)
	}
	if ok, _, err := stateStore.TryMarkRunning(ctx, jobstate.RunningRequest{
		SpaceID:    "crypto",
		JobItemID:  "ji-kv-directive",
		NodeID:     "node-1",
		AckSubject: "$JS.ACK.directive",
		StreamSeq:  1,
	}); err != nil || !ok {
		t.Fatalf("TryMarkRunning() ok=%v err=%v", ok, err)
	}
	if err := stateStore.MarkCanceled(ctx, "crypto", "ji-kv-directive", "user canceled"); err != nil {
		t.Fatalf("MarkCanceled() error = %v", err)
	}
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	svc := &Service{catalog: catalog, jobState: stateStore}

	rsp, err := svc.ReportHeartbeat(ctx, &pb.ReportHeartbeatReq{
		SpaceId: "crypto",
		NodeId:  "node-1",
	})
	if err != nil {
		t.Fatalf("ReportHeartbeat transport error = %v", err)
	}
	if len(rsp.GetDirectives()) != 1 || rsp.GetDirectives()[0].GetType() != pb.ControlDirectiveType_CONTROL_DIRECTIVE_CANCEL {
		t.Fatalf("directives = %+v", rsp.GetDirectives())
	}
	if rsp.GetDirectives()[0].GetJobItemId() != "ji-kv-directive" || rsp.GetDirectives()[0].GetAttemptNo() != 1 {
		t.Fatalf("directive = %+v", rsp.GetDirectives()[0])
	}
}

func TestReportHeartbeatWithSinkSkipsCancelDirectiveScan(t *testing.T) {
	ctx := context.Background()
	rt, _ := startRPCQueueRuntime(t)
	kv, err := rt.JetStream().KeyValue(config.Default().JobItem.ActiveKVBucket)
	if err != nil {
		t.Fatalf("KeyValue() error = %v", err)
	}
	stateStore := jobstate.NewKVStore(kv, jobstate.Options{
		RecoverAfterMillis: int64(time.Minute / time.Millisecond),
		DefaultMaxAttempts: 3,
	})
	params, _ := structpb.NewStruct(map[string]any{"symbol": "BTCUSDT"})
	if _, err := stateStore.CreatePending(ctx, &pb.JobItem{
		SpaceId:       "crypto",
		JobId:         "job-1",
		JobItemId:     "ji-kv-directive-sink",
		JobType:       "collect.kline",
		CodePackageId: "collector-scf",
		Params:        params,
	}, jobstate.QueueMeta{}); err != nil {
		t.Fatalf("CreatePending() error = %v", err)
	}
	if ok, _, err := stateStore.TryMarkRunning(ctx, jobstate.RunningRequest{
		SpaceID:    "crypto",
		JobItemID:  "ji-kv-directive-sink",
		NodeID:     "node-1",
		AckSubject: "$JS.ACK.directive",
		StreamSeq:  1,
	}); err != nil || !ok {
		t.Fatalf("TryMarkRunning() ok=%v err=%v", ok, err)
	}
	if err := stateStore.MarkCanceled(ctx, "crypto", "ji-kv-directive-sink", "user canceled"); err != nil {
		t.Fatalf("MarkCanceled() error = %v", err)
	}
	svc := &Service{jobState: stateStore, heartbeatSink: &fakeHeartbeatSink{}}

	rsp, err := svc.ReportHeartbeat(ctx, &pb.ReportHeartbeatReq{
		SpaceId: "crypto",
		NodeId:  "node-1",
	})
	if err != nil {
		t.Fatalf("ReportHeartbeat transport error = %v", err)
	}
	if len(rsp.GetDirectives()) != 0 {
		t.Fatalf("directives = %+v, want none when heartbeat sink is enabled", rsp.GetDirectives())
	}
}

type fakeHeartbeatSink struct {
	items []*pb.ReportHeartbeatReq
}

func (s *fakeHeartbeatSink) Enqueue(req *pb.ReportHeartbeatReq) error {
	s.items = append(s.items, req)
	return nil
}

func (s *fakeHeartbeatSink) Flush(context.Context) error { return nil }

func (s *fakeHeartbeatSink) Close(context.Context) error { return nil }

func TestNodeMetadataAndStatusBranches(t *testing.T) {
	meta, err := structpb.NewStruct(map[string]any{"existing": "yes"})
	require.NoError(t, err)
	node := &pb.CloudNode{
		Metadata:           meta,
		BizType:            "collect.kline",
		Tag:                "prod",
		IpAddress:          "10.0.0.1",
		TimeoutThreshold:   30,
		HeartbeatInterval:  15,
		ProbeEnabled:       true,
		ProbeUrl:           "https://probe",
		ClsTopicId:         "topic-1",
		SupportedWorkloads: []string{"collect.kline"},
	}
	got := nodeMetadataFromPB(node)
	assert.Equal(t, "yes", got["existing"])
	assert.Equal(t, "collect.kline", got["biz_type"])
	assert.Equal(t, true, got["probe_enabled"])
	assert.Equal(t, "topic-1", got["cls_topic_id"])
	assert.Empty(t, nodeMetadataFromPB(nil))

	assert.Equal(t, pb.NodeStatusCode_NODE_STATUS_TIMEOUT, nodeStatusToPB(" timeout "))
	assert.Equal(t, pb.NodeStatusCode_NODE_STATUS_ABNORMAL, nodeStatusToPB("abnormal"))
	assert.Equal(t, pb.NodeStatusCode_NODE_STATUS_OFFLINE, nodeStatusToPB("unknown"))
	assert.Equal(t, pb.NodeStatusCode_NODE_STATUS_UNSPECIFIED, nodeStatusToPB("starting"))

	assert.Equal(t, "timeout", nodeStatusToDB(pb.NodeStatusCode_NODE_STATUS_TIMEOUT))
	assert.Equal(t, "abnormal", nodeStatusToDB(pb.NodeStatusCode_NODE_STATUS_ABNORMAL))
	assert.Equal(t, "offline", nodeStatusToDB(pb.NodeStatusCode_NODE_STATUS_OFFLINE))
	assert.Equal(t, "unknown", nodeStatusToDB(pb.NodeStatusCode_NODE_STATUS_UNSPECIFIED))
}

func TestNodeConversionHelpersCoverDefaults(t *testing.T) {
	assert.Nil(t, parseStringSliceJSON(""))
	assert.Nil(t, parseStringSliceJSON("[]"))
	assert.Nil(t, parseStringSliceJSON("{bad"))

	copied := copyStringMap(map[string]string{"a": "1"})
	copied["a"] = "2"
	assert.Equal(t, "1", map[string]string{"a": "1"}["a"])
	assert.Nil(t, copyStringMap(nil))

	meta, err := structpb.NewStruct(map[string]any{
		"function_name":       "fn-meta",
		"supported_workloads": []any{"collect.symbol"},
	})
	require.NoError(t, err)
	node := fromPBNode("space", &pb.CloudNode{
		NodeId:   "node-1",
		Metadata: meta,
		Status:   pb.NodeStatusCode_NODE_STATUS_ABNORMAL,
	})
	assert.Equal(t, "scf-event", node.NodeType)
	assert.Equal(t, "tencent-scf", node.Provider)
	assert.Equal(t, "fn-meta", node.FunctionName)
	assert.Contains(t, node.SupportedWorkloads, "collect.symbol")
	assert.Equal(t, "abnormal", node.Status)
}

func TestMergeNodeUpdateCoversOptionalFields(t *testing.T) {
	meta, err := structpb.NewStruct(map[string]any{"new": "value"})
	require.NoError(t, err)
	existing := store.CloudNode{
		SpaceID: "space", NodeID: "node-1", CloudAccountID: "old-acct",
		Metadata: `{"old":"value"}`, Status: "online",
	}
	next := mergeNodeUpdate(existing, &pb.CloudNode{
		CloudAccountId:     "new-acct",
		PackageId:          "pkg-1",
		PackageVersion:     "v2",
		DeploymentId:       "dep-1",
		NodeType:           "worker",
		Provider:           "tencent-scf",
		Region:             "ap-guangzhou",
		Namespace:          "default",
		FunctionName:       "fn",
		RunningVersion:     "rv1",
		SupportedWorkloads: []string{"collect.kline"},
		Metadata:           meta,
		Status:             pb.NodeStatusCode_NODE_STATUS_OFFLINE,
		IsDeleted:          true,
	})
	assert.Equal(t, "new-acct", next.CloudAccountID)
	assert.Equal(t, "pkg-1", next.PackageID)
	assert.Equal(t, "v2", next.PackageVersion)
	assert.Equal(t, "dep-1", next.DeploymentID)
	assert.Equal(t, "worker", next.NodeType)
	assert.Equal(t, "tencent-scf", next.Provider)
	assert.Equal(t, "ap-guangzhou", next.Region)
	assert.Equal(t, "default", next.Namespace)
	assert.Equal(t, "fn", next.FunctionName)
	assert.Equal(t, "rv1", next.RunningVersion)
	assert.Contains(t, next.SupportedWorkloads, "collect.kline")
	assert.Contains(t, next.Metadata, `"old":"value"`)
	assert.Contains(t, next.Metadata, `"new":"value"`)
	assert.Equal(t, "offline", next.Status)
	assert.True(t, next.IsDeleted)
}

func TestGetNodeListAndUpdateNode(t *testing.T) {
	catalog := newCatalogForAccountTests(t)
	svc := &Service{catalog: catalog}
	ctx := spacecontext.WithSpaceID(context.Background(), "crypto")
	require.NoError(t, catalog.UpsertNode(ctx, store.CloudNode{
		SpaceID: "crypto", NodeID: "node-a", CloudAccountID: "acct-1",
		Region: "ap-guangzhou", Status: "online",
	}))

	listRsp, err := svc.GetNodeList(ctx, &pb.GetNodeListReq{Page: &pb.Page{Page: 1, Size: 10}})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, listRsp.GetRetInfo().GetCode())
	require.Len(t, listRsp.GetItems(), 1)
	assert.Equal(t, "node-a", listRsp.GetItems()[0].GetNodeId())

	updateRsp, err := svc.UpdateNode(ctx, &pb.UpdateNodeReq{Node: &pb.CloudNode{
		NodeId: "node-a", Region: "ap-shanghai", Status: pb.NodeStatusCode_NODE_STATUS_ONLINE,
	}})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, updateRsp.GetRetInfo().GetCode())
}

func TestBatchDeleteNodes_ShouldSoftDelete(t *testing.T) {
	catalog := newCatalogForAccountTests(t)
	svc := &Service{catalog: catalog}
	ctx := spacecontext.WithSpaceID(context.Background(), "crypto")
	require.NoError(t, catalog.UpsertNode(ctx, store.CloudNode{
		SpaceID: "crypto", NodeID: "node-del", Region: "ap-guangzhou",
	}))

	rsp, err := svc.BatchDeleteNodes(ctx, &pb.BatchDeleteNodesReq{NodeIds: []string{"node-del"}})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, int32(1), rsp.GetProcessedCount())
}

func TestReportHeartbeatWithoutSink_ShouldUpsertCatalog(t *testing.T) {
	catalog := newCatalogForAccountTests(t)
	svc := &Service{catalog: catalog}
	meta, err := structpb.NewStruct(map[string]any{"probe_enabled": true})
	require.NoError(t, err)

	rsp, err := svc.ReportHeartbeat(context.Background(), &pb.ReportHeartbeatReq{
		SpaceId: "crypto", NodeId: "node-hb", NodeType: "scf-event",
		RunningVersion: "v1", SupportedWorkloads: []string{"collect.kline"}, Metadata: meta,
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())

	node, err := catalog.GetNode(context.Background(), "crypto", "node-hb")
	require.NoError(t, err)
	require.NotNil(t, node)
	assert.Equal(t, "online", node.Status)
	assert.Equal(t, "v1", node.RunningVersion)
}

func TestNodeConversionHelpers(t *testing.T) {
	assert.True(t, isTencentProvider("tencent-scf"))
	assert.False(t, isTencentProvider("aws"))
	assert.True(t, isSCFNotFound(errors.New("ResourceNotFound.FunctionName")))
	assert.Equal(t, int64(30), configInt64(map[string]string{"timeout": "30"}, "timeout", 10))
	assert.Equal(t, int64(10), configInt64(map[string]string{"timeout": "bad"}, "timeout", 10))

	now := time.Now().UTC()
	pbNode := toPBNode(store.CloudNode{
		SpaceID: "crypto", NodeID: "n1", Region: "ap-guangzhou",
		Status: "online", Metadata: `{"biz_type":"collect.kline"}`,
		LastHeartbeatAt: &now, SupportedWorkloads: `["collect.kline"]`,
	})
	assert.Equal(t, pb.NodeStatusCode_NODE_STATUS_ONLINE, pbNode.GetStatus())
	assert.Equal(t, "collect.kline", pbNode.GetBizType())
	assert.Equal(t, []string{"collect.kline"}, parseStringSliceJSON(`["collect.kline"]`))

	assert.Equal(t, "online", nodeStatusToDB(pb.NodeStatusCode_NODE_STATUS_ONLINE))
	assert.Equal(t, pb.NodeStatusCode_NODE_STATUS_OFFLINE, nodeStatusToPB("deleted"))

	item := cloudNodeFromCreateItem("crypto", &pb.NodeCreateItem{
		CloudAccountId: "acct-1", Region: "ap-guangzhou", PackageId: "pkg-1",
	}, 2)
	assert.Contains(t, item.NodeID, "moox-cloudnode")
	assert.Equal(t, "acct-1", item.CloudAccountID)
}

func TestBatchCreateNodesCreatesTencentSCFFunctionFromPackage(t *testing.T) {
	db := newNodeSCFTestDB(t)
	catalog := store.NewCatalogRepository(db)
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
		scfClientFactory: func(account store.CloudAccount) scfProvisioner {
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
	catalog := store.NewCatalogRepository(db)
	seedSCFAccountAndPackage(t, catalog)
	if err := catalog.UpsertNode(context.Background(), store.CloudNode{
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
		scfClientFactory: func(account store.CloudAccount) scfProvisioner {
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

func seedSCFAccountAndPackage(t *testing.T, catalog *store.CatalogRepository) {
	t.Helper()
	now := time.Now().UTC()
	if err := catalog.UpsertAccount(context.Background(), store.CloudAccount{
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
	if err := catalog.UpsertPackage(context.Background(), store.FunctionPackage{
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
