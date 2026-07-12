package router

import (
	"context"
	"fmt"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestResolverUsesStableWeightedHashAcrossEqualRoutes(t *testing.T) {
	metadata := &resolverMetadata{
		routes: []*pb.PrimaryStoreRoute{
			{SpaceId: "crypto", DatasetId: "kline", RouteId: "route-a", SubjectPattern: "*", HashRule: "subject_id", NodeId: "node-a", Priority: 100, Status: "active"},
			{SpaceId: "crypto", DatasetId: "kline", RouteId: "route-b", SubjectPattern: "*", HashRule: "subject_id", NodeId: "node-b", Priority: 100, Status: "active"},
		},
		nodes: map[string]*pb.PrimaryStoreNode{
			"node-a": {NodeId: "node-a", Weight: 100, Status: "active"},
			"node-b": {NodeId: "node-b", Weight: 300, Status: "active"},
		},
	}
	resolver := NewResolver(metadata)
	counts := map[string]int{}
	for i := 0; i < 1000; i++ {
		key := fmt.Sprintf("subject-%04d", i)
		first, err := resolver.Resolve(context.Background(), "crypto", "kline", key)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", key, err)
		}
		second, err := resolver.Resolve(context.Background(), "crypto", "kline", key)
		if err != nil {
			t.Fatalf("Resolve stable(%q): %v", key, err)
		}
		if first.GetNodeId() != second.GetNodeId() {
			t.Fatalf("unstable route for %q: %s then %s", key, first.GetNodeId(), second.GetNodeId())
		}
		counts[first.GetNodeId()]++
	}
	if counts["node-a"] < 100 || counts["node-b"] < 650 {
		t.Fatalf("weighted distribution = %v, want both nodes and heavier node-b", counts)
	}
}

func TestResolverKeepsSpecificRouteAheadOfHashPool(t *testing.T) {
	metadata := &resolverMetadata{
		routes: []*pb.PrimaryStoreRoute{
			{SpaceId: "crypto", DatasetId: "kline", RouteId: "route-a", SubjectPattern: "*", HashRule: "subject_id", NodeId: "node-a", Priority: 100, Status: "active"},
			{SpaceId: "crypto", DatasetId: "kline", RouteId: "route-b", SubjectPattern: "*", HashRule: "subject_id", NodeId: "node-b", Priority: 100, Status: "active"},
			{SpaceId: "crypto", DatasetId: "kline", RouteId: "vip", SubjectId: "BTCUSDT", NodeId: "node-vip", Priority: 900, Status: "active"},
		},
		nodes: map[string]*pb.PrimaryStoreNode{
			"node-a":   {NodeId: "node-a", Weight: 100, Status: "active"},
			"node-b":   {NodeId: "node-b", Weight: 100, Status: "active"},
			"node-vip": {NodeId: "node-vip", Weight: 1, Status: "active"},
		},
	}

	target, err := NewResolver(metadata).Resolve(context.Background(), "crypto", "kline", "BTCUSDT")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if target.GetNodeId() != "node-vip" {
		t.Fatalf("node = %q, want exact-match node-vip", target.GetNodeId())
	}
}

type resolverMetadata struct {
	routes []*pb.PrimaryStoreRoute
	nodes  map[string]*pb.PrimaryStoreNode
}

func (m *resolverMetadata) ListPrimaryStoreRoutes(context.Context, string, string, string, string, *pb.Page) ([]*pb.PrimaryStoreRoute, *pb.PageResult, error) {
	return m.routes, &pb.PageResult{}, nil
}

func (m *resolverMetadata) GetPrimaryStoreNode(_ context.Context, nodeID string) (*pb.PrimaryStoreNode, error) {
	node := m.nodes[nodeID]
	if node == nil {
		return nil, fmt.Errorf("node %s not found", nodeID)
	}
	return node, nil
}

func (*resolverMetadata) ListDevices(_ context.Context, nodeID string, _ string, _ *pb.Page) ([]*pb.Device, *pb.PageResult, error) {
	return []*pb.Device{{DeviceId: "pebble-" + nodeID, NodeId: nodeID, Engine: "pebble", Status: "active"}}, &pb.PageResult{}, nil
}
