package router_test

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/services/router"
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

type fakeRouteMetadata struct {
	routes []*pb.StorageRoute
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
