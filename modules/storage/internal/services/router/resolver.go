package router

import (
	"context"
	"fmt"
	"path"
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
	var candidates []routeCandidate
	for _, route := range routes {
		if route.GetStatus() != "" && route.GetStatus() != "active" {
			continue
		}
		rank, ok := matchRank(route, scope.GetSubjectId())
		if !ok {
			continue
		}
		candidates = append(candidates, routeCandidate{route: route, rank: rank})
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("storage route not found for %s/%s/%s", scope.GetSpaceId(), scope.GetDatasetId(), scope.GetSubjectId())
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].rank != candidates[j].rank {
			return candidates[i].rank > candidates[j].rank
		}
		if candidates[i].route.GetPriority() == candidates[j].route.GetPriority() {
			return candidates[i].route.GetRouteId() < candidates[j].route.GetRouteId()
		}
		return candidates[i].route.GetPriority() < candidates[j].route.GetPriority()
	})
	chosen := candidates[0].route
	return &pb.DeviceRef{
		SpaceId:   scope.GetSpaceId(),
		NodeId:    chosen.GetNodeId(),
		Engine:    "pebble",
		DatasetId: scope.GetDatasetId(),
	}, nil
}

type routeCandidate struct {
	route *pb.StorageRoute
	rank  int
}

func matchRank(route *pb.StorageRoute, subjectID string) (int, bool) {
	if route.GetSubjectId() != "" {
		if route.GetSubjectId() == subjectID {
			return 3, true
		}
		return 0, false
	}
	pattern := route.GetSubjectPattern()
	if pattern == "" || pattern == "*" {
		return 1, true
	}
	matched, err := path.Match(pattern, subjectID)
	if err != nil {
		return 0, false
	}
	if matched {
		return 2, true
	}
	return 0, false
}
