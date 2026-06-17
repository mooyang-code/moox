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
	GetStorageNode(ctx context.Context, nodeID string) (*pb.StorageNode, error)
	ListDevices(ctx context.Context, nodeID string, engine string, page *pb.Page) ([]*pb.Device, *pb.PageResult, error)
}

type Resolver struct {
	metadata RouteReader
}

func NewResolver(store RouteReader) *Resolver {
	return &Resolver{metadata: store}
}

func (r *Resolver) Resolve(ctx context.Context, scope *pb.DataScope) (*pb.PrimaryTarget, error) {
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
	node, err := r.metadata.GetStorageNode(ctx, chosen.GetNodeId())
	if err != nil {
		return nil, fmt.Errorf("storage node %s not found: %w", chosen.GetNodeId(), err)
	}
	if node == nil {
		return nil, fmt.Errorf("storage node %s not found", chosen.GetNodeId())
	}
	if node.GetStatus() != "" && node.GetStatus() != "active" {
		return nil, fmt.Errorf("storage node %s is not active", chosen.GetNodeId())
	}
	device, err := r.resolvePrimaryDevice(ctx, chosen.GetNodeId())
	if err != nil {
		return nil, err
	}
	return &pb.PrimaryTarget{
		SpaceId:     scope.GetSpaceId(),
		NodeId:      node.GetNodeId(),
		DeviceId:    device.GetDeviceId(),
		Engine:      device.GetEngine(),
		DatasetId:   scope.GetDatasetId(),
		DeviceTable: path.Join(scope.GetSpaceId(), scope.GetDatasetId()),
		Endpoint:    node.GetEndpoint(),
	}, nil
}

func (r *Resolver) resolvePrimaryDevice(ctx context.Context, nodeID string) (*pb.Device, error) {
	devices, _, err := r.metadata.ListDevices(ctx, nodeID, "pebble", nil)
	if err != nil {
		return nil, err
	}
	for _, device := range devices {
		if device == nil {
			continue
		}
		if device.GetStatus() == "" || device.GetStatus() == "active" {
			return device, nil
		}
	}
	return nil, fmt.Errorf("active pebble device not found for storage node %s", nodeID)
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
