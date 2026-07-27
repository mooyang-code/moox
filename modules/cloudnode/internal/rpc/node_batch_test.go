package rpc

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/spacecontext"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestSubmitCreateNodesReturnsBeforeTencentCall(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	fake := &fakeSCFClient{}
	svc := newNodeItemTestService(catalog, fake)

	rsp, err := svc.SubmitCreateNodes(nodeBatchContext("crypto"), &pb.BatchCreateNodesReq{
		Nodes: []*pb.NodeCreateItem{validCreateNodeItem(t, "node-000")},
	})

	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	assert.True(t, strings.HasPrefix(rsp.GetJobId(), "node-batch-"))
	assert.Equal(t, int32(1), rsp.GetTotalCount())
	assert.Zero(t, fake.getCalls)
	assert.Empty(t, fake.created)
}

func TestSubmitDeployNodesReturnsBeforeTencentCall(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	seedNodeForDeploy(t, catalog)
	fake := &fakeSCFClient{}
	svc := newNodeItemTestService(catalog, fake)

	rsp, err := svc.SubmitDeployNodes(nodeBatchContext("crypto"), &pb.BatchDeployNodesReq{
		Deployments: []*pb.NodeDeployItem{{NodeId: "node-a", PackageId: "moox-collector_dev"}},
	})

	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	assert.Equal(t, pb.NodeBatchOperation_NODE_BATCH_OPERATION_DEPLOY_NODES, rsp.GetOperation())
	assert.Zero(t, fake.getCalls)
	assert.Empty(t, fake.updated)
}

func TestSubmitCreateNodesRejectsEmptyItems(t *testing.T) {
	svc := &Service{catalog: store.NewCatalogRepository(newNodeSCFTestDB(t))}
	rsp, err := svc.SubmitCreateNodes(nodeBatchContext("crypto"), &pb.BatchCreateNodesReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestSubmitCreateNodesRejectsMoreThanOneHundredItems(t *testing.T) {
	svc := &Service{catalog: store.NewCatalogRepository(newNodeSCFTestDB(t))}
	items := make([]*pb.NodeCreateItem, 101)
	rsp, err := svc.SubmitCreateNodes(nodeBatchContext("crypto"), &pb.BatchCreateNodesReq{Nodes: items})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestSubmitDeployNodesPreflightsEveryNodeAndPackage(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	seedNodeForDeploy(t, catalog)
	svc := &Service{catalog: catalog}

	rsp, err := svc.SubmitDeployNodes(nodeBatchContext("crypto"), &pb.BatchDeployNodesReq{
		Deployments: []*pb.NodeDeployItem{
			{NodeId: "node-a", PackageId: "moox-collector_dev"},
			{NodeId: "missing", PackageId: "moox-collector_dev"},
		},
	})

	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_NOT_FOUND, rsp.GetRetInfo().GetCode())
	assert.Empty(t, rsp.GetJobId())
}

func TestGetNodeBatchChangeReturnsRealAggregate(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	require.NoError(t, catalog.CreateNodeBatch(context.Background(), store.NodeBatchCreate{
		SpaceID: "crypto", JobID: "node-batch-test", Operation: "create_nodes",
		Items: []store.NodeBatchItemCreate{
			{ItemID: "item-0", ItemIndex: 0, NodeID: "node-0", RequestJSON: `{}`},
			{ItemID: "item-1", ItemIndex: 1, NodeID: "node-1", RequestJSON: `{}`},
		},
	}))
	taken, err := catalog.TakePendingNodeBatchItems(context.Background(), 2)
	require.NoError(t, err)
	require.Len(t, taken, 2)
	require.NoError(t, catalog.CompleteNodeBatchItem(context.Background(), "crypto", "node-batch-test", "item-0", "created function node-0", nil))
	svc := &Service{catalog: catalog}

	rsp, err := svc.GetNodeBatchChange(nodeBatchContext("crypto"), &pb.GetNodeBatchChangeReq{JobId: "node-batch-test"})

	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, pb.NodeBatchStatus_NODE_BATCH_STATUS_RUNNING, rsp.GetJob().GetStatus())
	assert.Equal(t, int32(50), rsp.GetJob().GetProgressPercent())
	assert.Equal(t, int32(1), rsp.GetJob().GetRunningCount())
	assert.Equal(t, int32(1), rsp.GetJob().GetSuccessCount())
	require.Len(t, rsp.GetItems(), 2)
	assert.Equal(t, "item-0", rsp.GetItems()[0].GetItemId())
}

func TestGetNodeBatchChangeDoesNotExposeRequestPayload(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	item := &pb.NodeDeployItem{
		NodeId: "node-a", PackageId: "pkg",
		Environment: map[string]string{"SECRET": "must-not-leak"},
	}
	raw, err := protojson.Marshal(item)
	require.NoError(t, err)
	require.NoError(t, catalog.CreateNodeBatch(context.Background(), store.NodeBatchCreate{
		SpaceID: "crypto", JobID: "node-batch-secret", Operation: "deploy_nodes",
		Items: []store.NodeBatchItemCreate{{ItemID: "item-0", NodeID: "node-a", RequestJSON: string(raw)}},
	}))
	svc := &Service{catalog: catalog}

	rsp, err := svc.GetNodeBatchChange(nodeBatchContext("crypto"), &pb.GetNodeBatchChangeReq{JobId: "node-batch-secret"})

	require.NoError(t, err)
	out := fmt.Sprintf("%v", rsp)
	assert.NotContains(t, out, "must-not-leak")
	assert.NotContains(t, out, "SECRET")
}

func TestGetNodeBatchChangeRejectsOtherSpace(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	require.NoError(t, catalog.CreateNodeBatch(context.Background(), store.NodeBatchCreate{
		SpaceID: "space-a", JobID: "node-batch-private", Operation: "create_nodes",
		Items: []store.NodeBatchItemCreate{{ItemID: "item-0", NodeID: "node-a", RequestJSON: `{}`}},
	}))
	svc := &Service{catalog: catalog}

	rsp, err := svc.GetNodeBatchChange(nodeBatchContext("space-b"), &pb.GetNodeBatchChangeReq{JobId: "node-batch-private"})

	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_NOT_FOUND, rsp.GetRetInfo().GetCode())
}

func validCreateNodeItem(t *testing.T, nodeID string) *pb.NodeCreateItem {
	t.Helper()
	metadata, err := structpb.NewStruct(map[string]any{"node_id": nodeID, "function_name": nodeID})
	require.NoError(t, err)
	return &pb.NodeCreateItem{
		CloudAccountId: "account-a",
		Region:         "ap-guangzhou",
		PackageId:      "moox-collector_dev",
		Metadata:       metadata,
	}
}

func nodeBatchContext(spaceID string) context.Context {
	return spacecontext.WithSpaceID(context.Background(), spaceID)
}
