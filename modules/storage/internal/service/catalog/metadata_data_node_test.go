package catalog

import (
	"context"
	"fmt"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	metacache "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/cache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

type dataNodeMetadataStore struct {
	metadata.Store
	nodes          []*pb.DataNode
	datasets       []*pb.Dataset
	registerCalls  int
	datasetQueries []metadata.DatasetQuery
	dataNodePages  []*pb.Page
}

func (s *dataNodeMetadataStore) RegisterDataNode(context.Context, string, string, string) (*pb.DataNode, error) {
	s.registerCalls++
	return s.nodes[0], nil
}

func (s *dataNodeMetadataStore) ListDataNodes(_ context.Context, page *pb.Page) ([]*pb.DataNode, *pb.PageResult, error) {
	s.dataNodePages = append(s.dataNodePages, page)
	items, result := pageSlice(s.nodes, page)
	return items, result, nil
}

func (s *dataNodeMetadataStore) GetDataNode(_ context.Context, nodeID string) (*pb.DataNode, error) {
	for _, node := range s.nodes {
		if node.GetNodeId() == nodeID {
			return node, nil
		}
	}
	return nil, nil
}

func (s *dataNodeMetadataStore) UpdateDataNode(_ context.Context, nodeID, name, status string) (*pb.DataNode, error) {
	for _, node := range s.nodes {
		if node.GetNodeId() == nodeID {
			node.Name = name
			node.Status = status
			return node, nil
		}
	}
	return nil, nil
}

func (s *dataNodeMetadataStore) DeleteDataNode(_ context.Context, nodeID string) error {
	for i, node := range s.nodes {
		if node.GetNodeId() == nodeID {
			s.nodes = append(s.nodes[:i], s.nodes[i+1:]...)
			return nil
		}
	}
	return nil
}

func (s *dataNodeMetadataStore) RebindDatasetDataNode(_ context.Context, spaceID, datasetID, dataNodeID string, expectedRevision uint64) (*pb.Dataset, error) {
	return &pb.Dataset{
		SpaceId: spaceID, DatasetId: datasetID, DataNodeId: dataNodeID,
		Revision: expectedRevision + 1, Status: "disabled",
	}, nil
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
	svc, err := NewMetadataService(store, nil, Options{AuthSecret: "secret"})
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

func TestListDataNodesFiltersStatusBeforePagination(t *testing.T) {
	store := &dataNodeMetadataStore{nodes: []*pb.DataNode{
		{NodeId: "node-a", Name: "A", Status: "active"},
		{NodeId: "node-b", Name: "B", Status: "disabled"},
		{NodeId: "node-c", Name: "C", Status: "active"},
	}}
	svc, err := NewMetadataService(store, nil, Options{AuthSecret: "secret"})
	require.NoError(t, err)

	pageOne, err := svc.ListDataNodes(context.Background(), &pb.ListDataNodesReq{
		Status: "active", Page: &pb.Page{Page: 1, Size: 1},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, pageOne.GetRetInfo().GetCode())
	require.Equal(t, []string{"node-a"}, dataNodeIDs(pageOne.GetItems()))
	require.Equal(t, uint32(2), pageOne.GetPageResult().GetTotal())
	require.True(t, pageOne.GetPageResult().GetHasMore())

	pageTwo, err := svc.ListDataNodes(context.Background(), &pb.ListDataNodesReq{
		Status: "active", Page: &pb.Page{Page: 2, Size: 1},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"node-c"}, dataNodeIDs(pageTwo.GetItems()))
	require.False(t, pageTwo.GetPageResult().GetHasMore())

	disabled, err := svc.ListDataNodes(context.Background(), &pb.ListDataNodesReq{
		Status: "disabled", Page: &pb.Page{Page: 1, Size: 10},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"node-b"}, dataNodeIDs(disabled.GetItems()))
	require.Equal(t, uint32(1), disabled.GetPageResult().GetTotal())
}

func TestListDataNodesFetchesAllUnderlyingPagesBeforeFiltering(t *testing.T) {
	nodes := make([]*pb.DataNode, 0, 1001)
	for i := 0; i < 1000; i++ {
		nodes = append(nodes, &pb.DataNode{NodeId: fmt.Sprintf("node-%04d", i), Status: "disabled"})
	}
	nodes = append(nodes, &pb.DataNode{NodeId: "node-1000", Status: "active"})
	store := &dataNodeMetadataStore{nodes: nodes}
	svc, err := NewMetadataService(store, nil, Options{AuthSecret: "secret"})
	require.NoError(t, err)

	rsp, err := svc.ListDataNodes(context.Background(), &pb.ListDataNodesReq{
		Status: "active", Page: &pb.Page{Page: 1, Size: 10},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, []string{"node-1000"}, dataNodeIDs(rsp.GetItems()))
	require.Equal(t, uint32(1), rsp.GetPageResult().GetTotal())
	require.False(t, rsp.GetPageResult().GetHasMore())
	require.Len(t, store.dataNodePages, 2)
	require.Nil(t, store.dataNodePages[0])
	require.Equal(t, &pb.Page{Page: 2, Size: 1000}, store.dataNodePages[1])
}

func dataNodeIDs(items []*pb.DataNodeListItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.GetNode().GetNodeId())
	}
	return ids
}

func TestDataNodeMutationSucceedsWhenCacheRefreshFailsAfterCommit(t *testing.T) {
	store := &dataNodeMetadataStore{nodes: []*pb.DataNode{{NodeId: "node-a", Name: "A", Status: "active"}}}
	// An uninitialized cache represents a refresh failure without requiring a
	// second metadata implementation just for this post-commit behavior.
	svc, err := NewMetadataService(store, &metacache.Store{}, Options{AuthSecret: "secret"})
	require.NoError(t, err)

	updated, err := svc.UpdateDataNode(context.Background(), &pb.UpdateDataNodeReq{
		NodeId: "node-a", Name: "A2", Status: "disabled",
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, updated.GetRetInfo().GetCode())
	require.Equal(t, "A2", updated.GetNode().GetName())

	deleted, err := svc.DeleteDataNode(context.Background(), &pb.DeleteDataNodeReq{NodeId: "node-a"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, deleted.GetRetInfo().GetCode())
}

func TestRebindDatasetSucceedsWhenCacheRefreshFailsAfterCommit(t *testing.T) {
	store := &dataNodeMetadataStore{nodes: []*pb.DataNode{{NodeId: "node-a", Status: "active"}}}
	svc, err := NewMetadataService(store, &metacache.Store{}, Options{AuthSecret: "secret"})
	require.NoError(t, err)

	rsp, err := svc.RebindDatasetDataNode(context.Background(), &pb.RebindDatasetDataNodeReq{
		SpaceId: "space-a", DatasetId: "dataset-a", DataNodeId: "node-a", ExpectedRevision: 1,
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, "node-a", rsp.GetDataset().GetDataNodeId())
}

func TestRegisterDataNodeRequiresDeploymentHMAC(t *testing.T) {
	store := &dataNodeMetadataStore{nodes: []*pb.DataNode{{NodeId: "node-a"}}}
	svc, err := NewMetadataService(store, nil, Options{AuthSecret: "secret"})
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
