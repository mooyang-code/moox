// Package router 解析在线事实主存的水平切分路由。
//
// 注意：PrimaryStoreRoute / PrimaryStoreNode 只负责在线主存切分，
// 不路由 DuckDB/Bleve/Parquet 派生设备。
package router

import (
	"context"
	"fmt"
	"path"
	"sort"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// RouteReader 定义路由解析所需的元数据读取接口。
type RouteReader interface {
	ListPrimaryStoreRoutes(ctx context.Context, spaceID string, datasetID string, subjectID string, nodeID string, page *pb.Page) ([]*pb.PrimaryStoreRoute, *pb.PageResult, error)
	GetPrimaryStoreNode(ctx context.Context, nodeID string) (*pb.PrimaryStoreNode, error)
	ListDevices(ctx context.Context, nodeID string, engine string, page *pb.Page) ([]*pb.Device, *pb.PageResult, error)
}

// Resolver 根据元数据把写入请求解析到主存目标。
type Resolver struct {
	metadata RouteReader
}

func NewResolver(store RouteReader) *Resolver {
	return &Resolver{metadata: store}
}

func (r *Resolver) Resolve(ctx context.Context, spaceID string, datasetID string, subjectID string) (*pb.PrimaryStoreTarget, error) {
	if spaceID == "" || datasetID == "" {
		return nil, fmt.Errorf("space_id and dataset_id are required")
	}
	routes, err := r.listDatasetRoutes(ctx, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	var candidates []routeCandidate
	for _, route := range routes {
		if route.GetStatus() != "" && route.GetStatus() != "active" {
			continue
		}
		rank, ok := matchRank(route, subjectID)
		if !ok {
			continue
		}
		candidates = append(candidates, routeCandidate{route: route, rank: rank})
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("primary store route not found for %s/%s/%s", spaceID, datasetID, subjectID)
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
	return r.targetForRoute(ctx, spaceID, datasetID, candidates[0].route)
}

// ResolveDatasetTargets 返回某个 Dataset 所有 active 主存目标。
// Access 用它做全量 scan/rebuild；deriver 仍只调用 Access，不理解这些路由细节。
func (r *Resolver) ResolveDatasetTargets(ctx context.Context, spaceID string, datasetID string) ([]*pb.PrimaryStoreTarget, error) {
	if spaceID == "" || datasetID == "" {
		return nil, fmt.Errorf("space_id and dataset_id are required")
	}
	routes, err := r.listDatasetRoutes(ctx, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(routes, func(i, j int) bool {
		if routes[i].GetPriority() == routes[j].GetPriority() {
			return routes[i].GetRouteId() < routes[j].GetRouteId()
		}
		return routes[i].GetPriority() < routes[j].GetPriority()
	})
	var targets []*pb.PrimaryStoreTarget
	seen := make(map[string]bool)
	for _, route := range routes {
		if route.GetStatus() != "" && route.GetStatus() != "active" {
			continue
		}
		target, err := r.targetForRoute(ctx, spaceID, datasetID, route)
		if err != nil {
			return nil, err
		}
		key := target.GetNodeId() + "\x00" + target.GetDeviceId() + "\x00" + target.GetDatasetId()
		if seen[key] {
			continue
		}
		seen[key] = true
		targets = append(targets, target)
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("primary store route not found for %s/%s", spaceID, datasetID)
	}
	return targets, nil
}

func (r *Resolver) listDatasetRoutes(ctx context.Context, spaceID string, datasetID string) ([]*pb.PrimaryStoreRoute, error) {
	const pageSize = uint32(1000)
	var out []*pb.PrimaryStoreRoute
	for pageNo := uint32(1); ; pageNo++ {
		routes, page, err := r.metadata.ListPrimaryStoreRoutes(ctx, spaceID, datasetID, "", "", &pb.Page{Page: pageNo, Size: pageSize})
		if err != nil {
			return nil, err
		}
		out = append(out, routes...)
		if page == nil || !page.GetHasMore() {
			break
		}
	}
	return out, nil
}

func (r *Resolver) targetForRoute(ctx context.Context, spaceID string, datasetID string, route *pb.PrimaryStoreRoute) (*pb.PrimaryStoreTarget, error) {
	if route == nil {
		return nil, fmt.Errorf("primary store route is required")
	}
	node, err := r.metadata.GetPrimaryStoreNode(ctx, route.GetNodeId())
	if err != nil {
		return nil, fmt.Errorf("storage node %s not found: %w", route.GetNodeId(), err)
	}
	if node == nil {
		return nil, fmt.Errorf("storage node %s not found", route.GetNodeId())
	}
	if node.GetStatus() != "" && node.GetStatus() != "active" {
		return nil, fmt.Errorf("storage node %s is not active", route.GetNodeId())
	}
	device, err := r.resolvePrimaryDevice(ctx, route.GetNodeId())
	if err != nil {
		return nil, err
	}
	return &pb.PrimaryStoreTarget{
		SpaceId:     spaceID,
		NodeId:      node.GetNodeId(),
		DeviceId:    device.GetDeviceId(),
		Engine:      device.GetEngine(),
		DatasetId:   datasetID,
		DeviceTable: path.Join(spaceID, datasetID),
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

// routeCandidate 表示一次路由解析命中的候选主存路由。
type routeCandidate struct {
	route *pb.PrimaryStoreRoute
	rank  int
}

func matchRank(route *pb.PrimaryStoreRoute, subjectID string) (int, bool) {
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
