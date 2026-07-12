package rpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/spacecontext"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

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
