// Package router 解析在线事实主存的水平切分路由。
//
// 注意：PrimaryStoreRoute / PrimaryStoreNode 只负责在线主存切分，
// 不路由 DuckDB/Bleve/Parquet 派生设备。
package router

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"path"
	"sort"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
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
	topRank := candidates[0].rank
	topPriority := candidates[0].route.GetPriority()
	pool := candidates[:0]
	for _, candidate := range candidates {
		if candidate.rank != topRank || candidate.route.GetPriority() != topPriority {
			break
		}
		pool = append(pool, candidate)
	}
	selected, err := r.selectHashedRoute(ctx, spaceID, datasetID, subjectID, pool)
	if err != nil {
		return nil, err
	}
	return r.targetForRoute(ctx, spaceID, datasetID, selected)
}

func (r *Resolver) selectHashedRoute(ctx context.Context, spaceID string, datasetID string, partitionKey string, candidates []routeCandidate) (*pb.PrimaryStoreRoute, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("primary store route candidates are required")
	}
	if len(candidates) == 1 || strings.TrimSpace(partitionKey) == "" {
		return candidates[0].route, nil
	}
	for _, candidate := range candidates {
		if !supportedHashRule(candidate.route.GetHashRule()) {
			return candidates[0].route, nil
		}
	}

	type weightedRoute struct {
		route  *pb.PrimaryStoreRoute
		weight uint32
	}
	byNode := make(map[string]weightedRoute, len(candidates))
	var choices []weightedRoute
	for _, candidate := range candidates {
		nodeID := candidate.route.GetNodeId()
		if _, exists := byNode[nodeID]; exists {
			continue
		}
		node, err := r.metadata.GetPrimaryStoreNode(ctx, nodeID)
		if err != nil {
			return nil, fmt.Errorf("storage node %s not found: %w", nodeID, err)
		}
		if node == nil || (node.GetStatus() != "" && node.GetStatus() != "active") {
			continue
		}
		weight := node.GetWeight()
		if weight == 0 {
			weight = 1
		}
		choice := weightedRoute{route: candidate.route, weight: weight}
		byNode[nodeID] = choice
		choices = append(choices, choice)
	}
	if len(choices) == 0 {
		return nil, fmt.Errorf("no active primary store nodes for %s/%s", spaceID, datasetID)
	}

	selected := choices[0]
	bestScore := weightedRendezvousScore(spaceID, datasetID, partitionKey, selected.route.GetNodeId(), selected.weight)
	for _, choice := range choices[1:] {
		score := weightedRendezvousScore(spaceID, datasetID, partitionKey, choice.route.GetNodeId(), choice.weight)
		if score < bestScore || (score == bestScore && choice.route.GetRouteId() < selected.route.GetRouteId()) {
			selected = choice
			bestScore = score
		}
	}
	return selected.route, nil
}

func supportedHashRule(rule string) bool {
	switch strings.ToLower(strings.TrimSpace(rule)) {
	case "subject_id", "record_id", "key":
		return true
	default:
		return false
	}
}

func weightedRendezvousScore(spaceID string, datasetID string, partitionKey string, nodeID string, weight uint32) float64 {
	sum := sha256.Sum256([]byte(spaceID + "\x00" + datasetID + "\x00" + partitionKey + "\x00" + nodeID))
	// The high 53 bits map exactly to float64, avoiding platform-dependent rounding.
	value := binary.BigEndian.Uint64(sum[:8]) >> 11
	unit := (float64(value) + 1) / (float64(uint64(1)<<53) + 1)
	return -math.Log(unit) / float64(weight)
}

// ResolveDatasetTargets 返回某个 Dataset 所有 active 主存目标。
// Access 用它做全量 scan/rebuild；ViewBuilder 仍只调用 Access，不理解这些路由细节。
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
		ShardId:     shardIdentity(node, device),
		DeviceId:    device.GetDeviceId(),
		Engine:      device.GetEngine(),
		DatasetId:   datasetID,
		DeviceTable: path.Join(spaceID, datasetID),
		Endpoint:    node.GetEndpoint(),
	}, nil
}

func shardIdentity(node *pb.PrimaryStoreNode, device *pb.Device) string {
	if device != nil && device.GetAttributes() != nil && strings.TrimSpace(device.GetAttributes()["shard_id"]) != "" {
		return strings.TrimSpace(device.GetAttributes()["shard_id"])
	}
	if node != nil && node.GetAttributes() != nil && strings.TrimSpace(node.GetAttributes()["shard_id"]) != "" {
		return strings.TrimSpace(node.GetAttributes()["shard_id"])
	}
	return ""
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
