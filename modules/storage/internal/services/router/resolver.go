package router

import (
	"context"
	"fmt"
	"sort"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

type RouteReader interface {
	ListStorageRoutes(ctx context.Context, spaceID string, datasetID string, subjectID string, nodeID string, page *pb.Page) ([]*pb.StorageRoute, *pb.PageResult, error)
}

type Resolver struct {
	metadata RouteReader
}

func NewResolver(store RouteReader) *Resolver {
	return &Resolver{metadata: store}
}

func (r *Resolver) Resolve(ctx context.Context, scope *pb.DataScope) (*pb.DeviceRef, error) {
	if scope == nil {
		return nil, fmt.Errorf("data scope is required")
	}
	routes, _, err := r.metadata.ListStorageRoutes(ctx, scope.GetSpaceId(), scope.GetDatasetId(), "", "", nil)
	if err != nil {
		return nil, err
	}
	var candidates []*pb.StorageRoute
	for _, route := range routes {
		if route.GetStatus() != "" && route.GetStatus() != "active" {
			continue
		}
		if route.GetSubjectId() != "" && route.GetSubjectId() != scope.GetSubjectId() {
			continue
		}
		if route.GetSubjectPattern() != "" && route.GetSubjectPattern() != "*" && route.GetSubjectPattern() != scope.GetSubjectId() {
			continue
		}
		candidates = append(candidates, route)
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("storage route not found for %s/%s/%s", scope.GetSpaceId(), scope.GetDatasetId(), scope.GetSubjectId())
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].GetPriority() == candidates[j].GetPriority() {
			return candidates[i].GetRouteId() < candidates[j].GetRouteId()
		}
		return candidates[i].GetPriority() < candidates[j].GetPriority()
	})
	chosen := candidates[0]
	return &pb.DeviceRef{
		SpaceId:   scope.GetSpaceId(),
		NodeId:    chosen.GetNodeId(),
		Engine:    "pebble",
		DatasetId: scope.GetDatasetId(),
	}, nil
}
