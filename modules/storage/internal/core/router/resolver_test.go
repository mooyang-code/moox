package router_test

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/core/router"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestResolverSelectsExactSubjectRouteBeforeWildcard(t *testing.T) {
	ctx := context.Background()
	meta := &fakeRouteMetadata{routes: []*pb.StorageRoute{
		{SpaceId: "crypto", RouteId: "wildcard", DatasetId: "kline", SubjectPattern: "*", NodeId: "node-a", Priority: 100, Status: "active"},
		{SpaceId: "crypto", RouteId: "apt", DatasetId: "kline", SubjectId: "APT-USDT", NodeId: "node-b", Priority: 10, Status: "active"},
	}}
	resolver := router.NewResolver(meta)

	ref, err := resolver.Resolve(ctx, &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT"})
	require.NoError(t, err)
	require.Equal(t, "node-b", ref.GetNodeId())
}

func TestResolverPrefersExactSubjectRouteOverLowerPriorityWildcard(t *testing.T) {
	ctx := context.Background()
	meta := &fakeRouteMetadata{routes: []*pb.StorageRoute{
		{SpaceId: "crypto", RouteId: "wildcard", DatasetId: "kline", SubjectPattern: "*", NodeId: "node-a", Priority: 1, Status: "active"},
		{SpaceId: "crypto", RouteId: "apt", DatasetId: "kline", SubjectId: "APT-USDT", NodeId: "node-b", Priority: 100, Status: "active"},
	}}
	resolver := router.NewResolver(meta)

	ref, err := resolver.Resolve(ctx, &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT"})
	require.NoError(t, err)
	require.Equal(t, "node-b", ref.GetNodeId())
}

func TestResolverSupportsSubjectGlobPatternBeforeDatasetDefault(t *testing.T) {
	ctx := context.Background()
	meta := &fakeRouteMetadata{routes: []*pb.StorageRoute{
		{SpaceId: "crypto", RouteId: "default", DatasetId: "kline", NodeId: "node-a", Priority: 1, Status: "active"},
		{SpaceId: "crypto", RouteId: "apt-pattern", DatasetId: "kline", SubjectPattern: "APT-*", NodeId: "node-b", Priority: 100, Status: "active"},
	}}
	resolver := router.NewResolver(meta)

	ref, err := resolver.Resolve(ctx, &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT"})
	require.NoError(t, err)
	require.Equal(t, "node-b", ref.GetNodeId())
}

func TestResolverReturnsDeviceLocationForChosenNode(t *testing.T) {
	ctx := context.Background()
	meta := &fakeRouteMetadata{
		routes: []*pb.StorageRoute{
			{SpaceId: "crypto", RouteId: "apt", DatasetId: "kline", SubjectId: "APT-USDT", NodeId: "node-b", Priority: 10, Status: "active"},
		},
		nodes: map[string]*pb.StorageNode{
			"node-b": {NodeId: "node-b", Name: "primary-b", Endpoint: "127.0.0.1:18101", Status: "active"},
		},
		devices: map[string][]*pb.Device{
			"node-b": {
				{DeviceId: "duckdb-b", NodeId: "node-b", Name: "duckdb-b", Engine: "duckdb", Status: "active"},
				{DeviceId: "pebble-b", NodeId: "node-b", Name: "pebble-b", Engine: "pebble", Endpoint: "/data/pebble-b", Status: "active"},
			},
		},
	}
	resolver := router.NewResolver(meta)

	ref, err := resolver.Resolve(ctx, &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT"})
	require.NoError(t, err)
	require.Equal(t, "node-b", ref.GetNodeId())
	require.Equal(t, "pebble-b", ref.GetDeviceId())
	require.Equal(t, "pebble", ref.GetEngine())
	require.Equal(t, "kline", ref.GetDatasetId())
	require.Equal(t, "crypto/kline", ref.GetDeviceTable())
	require.Equal(t, "127.0.0.1:18101", ref.GetEndpoint())
}

type fakeRouteMetadata struct {
	routes  []*pb.StorageRoute
	nodes   map[string]*pb.StorageNode
	devices map[string][]*pb.Device
}

func (f *fakeRouteMetadata) ListStorageRoutes(ctx context.Context, spaceID string, datasetID string, subjectID string, nodeID string, page *pb.Page) ([]*pb.StorageRoute, *pb.PageResult, error) {
	var out []*pb.StorageRoute
	for _, route := range f.routes {
		if route.GetSpaceId() == spaceID && route.GetDatasetId() == datasetID {
			out = append(out, route)
		}
	}
	return out, &pb.PageResult{Total: uint64(len(out))}, nil
}

func (f *fakeRouteMetadata) GetStorageNode(ctx context.Context, nodeID string) (*pb.StorageNode, error) {
	if f.nodes == nil {
		return &pb.StorageNode{NodeId: nodeID, Name: nodeID, Status: "active"}, nil
	}
	return f.nodes[nodeID], nil
}

func (f *fakeRouteMetadata) ListDevices(ctx context.Context, nodeID string, engine string, page *pb.Page) ([]*pb.Device, *pb.PageResult, error) {
	var out []*pb.Device
	for _, device := range f.devices[nodeID] {
		if engine == "" || device.GetEngine() == engine {
			out = append(out, device)
		}
	}
	if len(out) == 0 {
		out = append(out, &pb.Device{DeviceId: nodeID + "-pebble", NodeId: nodeID, Name: "pebble", Engine: "pebble", Status: "active"})
	}
	return out, &pb.PageResult{Total: uint64(len(out))}, nil
}
