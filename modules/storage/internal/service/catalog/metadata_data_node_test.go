package catalog

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

type dataNodeMetadataStore struct {
	metadata.Store
	nodes          []*pb.DataNode
	datasets       []*pb.Dataset
	registerCalls  int
	datasetQueries []metadata.DatasetQuery
}

func (s *dataNodeMetadataStore) RegisterDataNode(context.Context, string, string, string) (*pb.DataNode, error) {
	s.registerCalls++
	return s.nodes[0], nil
}

func (s *dataNodeMetadataStore) ListDataNodes(context.Context, *pb.Page) ([]*pb.DataNode, *pb.PageResult, error) {
	return s.nodes, &pb.PageResult{Page: 1, Size: 100, Total: uint32(len(s.nodes))}, nil
}

func (s *dataNodeMetadataStore) ListDatasets(_ context.Context, query metadata.DatasetQuery) ([]*pb.Dataset, *pb.PageResult, error) {
	s.datasetQueries = append(s.datasetQueries, query)
	return s.datasets, nil, nil
}

func TestListDataNodesAggregatesDatasetsWithOneUnpaginatedQuery(t *testing.T) {
	store := &dataNodeMetadataStore{
		nodes: []*pb.DataNode{
			{NodeId: "node-a", Name: "A", Status: "active"},
			{NodeId: "node-b", Name: "B", Status: "disabled"},
		},
		datasets: []*pb.Dataset{
			{SpaceId: "space-a", DatasetId: "dataset-a", DataNodeId: "node-a", Name: "A Dataset", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, KeepDuration: "24h", Status: "active"},
			{SpaceId: "space-b", DatasetId: "dataset-b", DataNodeId: "node-a", Name: "B Dataset", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "disabled"},
		},
	}
	svc, err := NewMetadataService(store, nil, "secret")
	require.NoError(t, err)

	rsp, err := svc.ListDataNodes(context.Background(), &pb.ListDataNodesReq{Page: &pb.Page{Page: 1, Size: 10}})
	require.NoError(t, err)
	require.Len(t, rsp.GetItems(), 2)
	require.Len(t, rsp.GetItems()[0].GetDatasets(), 2)
	require.Empty(t, rsp.GetItems()[1].GetDatasets())
	require.Len(t, store.datasetQueries, 1)
	query := store.datasetQueries[0]
	require.Nil(t, query.Page)
	require.ElementsMatch(t, []string{"node-a", "node-b"}, query.DataNodeIDs)
}

func TestRegisterDataNodeRequiresDeploymentHMAC(t *testing.T) {
	store := &dataNodeMetadataStore{nodes: []*pb.DataNode{{NodeId: "node-a"}}}
	svc, err := NewMetadataService(store, nil, "secret")
	require.NoError(t, err)

	bad, err := svc.RegisterDataNode(context.Background(), &pb.RegisterDataNodeReq{
		AuthInfo: &pb.AuthInfo{AppId: dataNodeRegistrationAppID, AppKey: "bad"},
		NodeId:   "node-a", ServiceTarget: "ip://127.0.0.1:1",
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NO_PERMISSION, bad.GetRetInfo().GetCode())
	require.Zero(t, store.registerCalls)

	good, err := svc.RegisterDataNode(context.Background(), &pb.RegisterDataNodeReq{
		AuthInfo: &pb.AuthInfo{AppId: dataNodeRegistrationAppID, AppKey: serviceAuthKey("secret", dataNodeRegistrationAppID)},
		NodeId:   "node-a", ServiceTarget: "ip://127.0.0.1:1",
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, good.GetRetInfo().GetCode())
	require.Equal(t, 1, store.registerCalls)
}
