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
	meta := &fakeRouteMetadata{routes: []*pb.PrimaryStoreRoute{
		{SpaceId: "crypto", RouteId: "wildcard", DatasetId: "kline", SubjectPattern: "*", NodeId: "node-a", Priority: 100, Status: "active"},
		{SpaceId: "crypto", RouteId: "apt", DatasetId: "kline", SubjectId: "APT-USDT", NodeId: "node-b", Priority: 10, Status: "active"},
	}}
	resolver := router.NewResolver(meta)

	ref, err := resolver.Resolve(ctx, "crypto", "kline", "APT-USDT")
	require.NoError(t, err)
	require.Equal(t, "node-b", ref.GetNodeId())
}

func TestResolverPrefersExactSubjectRouteOverLowerPriorityWildcard(t *testing.T) {
	ctx := context.Background()
	meta := &fakeRouteMetadata{routes: []*pb.PrimaryStoreRoute{
		{SpaceId: "crypto", RouteId: "wildcard", DatasetId: "kline", SubjectPattern: "*", NodeId: "node-a", Priority: 1, Status: "active"},
		{SpaceId: "crypto", RouteId: "apt", DatasetId: "kline", SubjectId: "APT-USDT", NodeId: "node-b", Priority: 100, Status: "active"},
	}}
	resolver := router.NewResolver(meta)

	ref, err := resolver.Resolve(ctx, "crypto", "kline", "APT-USDT")
	require.NoError(t, err)
	require.Equal(t, "node-b", ref.GetNodeId())
}

func TestResolverSupportsSubjectGlobPatternBeforeDatasetDefault(t *testing.T) {
	ctx := context.Background()
	meta := &fakeRouteMetadata{routes: []*pb.PrimaryStoreRoute{
		{SpaceId: "crypto", RouteId: "default", DatasetId: "kline", NodeId: "node-a", Priority: 1, Status: "active"},
		{SpaceId: "crypto", RouteId: "apt-pattern", DatasetId: "kline", SubjectPattern: "APT-*", NodeId: "node-b", Priority: 100, Status: "active"},
	}}
	resolver := router.NewResolver(meta)

	ref, err := resolver.Resolve(ctx, "crypto", "kline", "APT-USDT")
	require.NoError(t, err)
	require.Equal(t, "node-b", ref.GetNodeId())
}

func TestResolverReturnsDeviceLocationForChosenNode(t *testing.T) {
	ctx := context.Background()
	meta := &fakeRouteMetadata{
		routes: []*pb.PrimaryStoreRoute{
			{SpaceId: "crypto", RouteId: "apt", DatasetId: "kline", SubjectId: "APT-USDT", NodeId: "node-b", Priority: 10, Status: "active"},
		},
		nodes: map[string]*pb.PrimaryStoreNode{
			"node-b": {NodeId: "node-b", Name: "primary-b", Endpoint: "127.0.0.1:20101", Status: "active"},
		},
		devices: map[string][]*pb.Device{
			"node-b": {
				{DeviceId: "duckdb-b", NodeId: "node-b", Name: "duckdb-b", Engine: "duckdb", Status: "active"},
				{DeviceId: "pebble-b", NodeId: "node-b", Name: "pebble-b", Engine: "pebble", Endpoint: "/data/pebble-b", Status: "active"},
			},
		},
	}
	resolver := router.NewResolver(meta)

	ref, err := resolver.Resolve(ctx, "crypto", "kline", "APT-USDT")
	require.NoError(t, err)
	require.Equal(t, "node-b", ref.GetNodeId())
	require.Equal(t, "pebble-b", ref.GetDeviceId())
	require.Equal(t, "pebble", ref.GetEngine())
	require.Equal(t, "kline", ref.GetDatasetId())
	require.Equal(t, "crypto/kline", ref.GetDeviceTable())
	require.Equal(t, "127.0.0.1:20101", ref.GetEndpoint())
}

func TestResolverReturnsDatasetTargetsOrderedAndDeduplicated(t *testing.T) {
	ctx := context.Background()
	meta := &fakeRouteMetadata{routes: []*pb.PrimaryStoreRoute{
		{SpaceId: "crypto", RouteId: "route-b", DatasetId: "kline", SubjectId: "ETH-USDT", NodeId: "node-b", Priority: 20, Status: "active"},
		{SpaceId: "crypto", RouteId: "route-a-duplicate", DatasetId: "kline", SubjectId: "BTC-USDT", NodeId: "node-a", Priority: 10, Status: "active"},
		{SpaceId: "crypto", RouteId: "route-a", DatasetId: "kline", SubjectPattern: "BTC-*", NodeId: "node-a", Priority: 5, Status: "active"},
		{SpaceId: "crypto", RouteId: "route-disabled", DatasetId: "kline", SubjectPattern: "*", NodeId: "node-c", Priority: 1, Status: "disabled"},
	}}
	resolver := router.NewResolver(meta)

	targets, err := resolver.ResolveDatasetTargets(ctx, "crypto", "kline")

	require.NoError(t, err)
	require.Len(t, targets, 2)
	require.Equal(t, "node-a", targets[0].GetNodeId())
	require.Equal(t, "node-b", targets[1].GetNodeId())
}

func TestResolverReturnsDatasetTargetsAcrossRoutePages(t *testing.T) {
	ctx := context.Background()
	routes := make([]*pb.PrimaryStoreRoute, 0, 1001)
	for idx := 0; idx < 1001; idx++ {
		nodeID := "node-a"
		if idx == 1000 {
			nodeID = "node-z"
		}
		routes = append(routes, &pb.PrimaryStoreRoute{SpaceId: "crypto", RouteId: "route-" + nodeID, DatasetId: "kline", SubjectId: nodeID, NodeId: nodeID, Priority: uint32(idx), Status: "active"})
	}
	meta := &fakeRouteMetadata{routes: routes}
	resolver := router.NewResolver(meta)

	targets, err := resolver.ResolveDatasetTargets(ctx, "crypto", "kline")

	require.NoError(t, err)
	require.Len(t, targets, 2)
	require.Equal(t, "node-a", targets[0].GetNodeId())
	require.Equal(t, "node-z", targets[1].GetNodeId())
}

func TestResolverResolvesSubjectRouteAcrossRoutePages(t *testing.T) {
	ctx := context.Background()
	routes := make([]*pb.PrimaryStoreRoute, 0, 1001)
	for idx := 0; idx < 1000; idx++ {
		routes = append(routes, &pb.PrimaryStoreRoute{SpaceId: "crypto", RouteId: "route-a", DatasetId: "kline", SubjectId: "SUBJECT-A", NodeId: "node-a", Priority: uint32(idx), Status: "active"})
	}
	routes = append(routes, &pb.PrimaryStoreRoute{SpaceId: "crypto", RouteId: "route-z", DatasetId: "kline", SubjectId: "TARGET-Z", NodeId: "node-z", Priority: 1000, Status: "active"})
	meta := &fakeRouteMetadata{routes: routes}
	resolver := router.NewResolver(meta)

	target, err := resolver.Resolve(ctx, "crypto", "kline", "TARGET-Z")

	require.NoError(t, err)
	require.Equal(t, "node-z", target.GetNodeId())
}

// fakeRouteMetadata 是路由解析测试使用的元数据桩。
type fakeRouteMetadata struct {
	routes  []*pb.PrimaryStoreRoute
	nodes   map[string]*pb.PrimaryStoreNode
	devices map[string][]*pb.Device
}

func (f *fakeRouteMetadata) ListPrimaryStoreRoutes(ctx context.Context, spaceID string, datasetID string, subjectID string, nodeID string, page *pb.Page) ([]*pb.PrimaryStoreRoute, *pb.PageResult, error) {
	var out []*pb.PrimaryStoreRoute
	for _, route := range f.routes {
		if route.GetSpaceId() == spaceID && route.GetDatasetId() == datasetID {
			out = append(out, route)
		}
	}
	pageNo := uint32(1)
	size := uint32(1000)
	if page != nil {
		if page.GetPage() > 0 {
			pageNo = page.GetPage()
		}
		if page.GetSize() > 0 {
			size = page.GetSize()
		}
	}
	start := int((pageNo - 1) * size)
	if start > len(out) {
		start = len(out)
	}
	end := start + int(size)
	if end > len(out) {
		end = len(out)
	}
	return out[start:end], &pb.PageResult{Page: pageNo, Size: size, Total: uint32(len(out)), HasMore: end < len(out)}, nil
}

func (f *fakeRouteMetadata) GetPrimaryStoreNode(ctx context.Context, nodeID string) (*pb.PrimaryStoreNode, error) {
	if f.nodes == nil {
		return &pb.PrimaryStoreNode{NodeId: nodeID, Name: nodeID, Status: "active"}, nil
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
	return out, &pb.PageResult{Total: uint32(len(out))}, nil
}
