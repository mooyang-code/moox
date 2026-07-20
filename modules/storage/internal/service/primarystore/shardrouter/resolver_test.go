//go:build legacy_storage

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

func TestResolverRejectsUnsupportedHashRuleEvenForSingleRoute(t *testing.T) {
	metadata := &resolverMetadata{
		routes: []*pb.PrimaryStoreRoute{{SpaceId: "crypto", DatasetId: "kline", RouteId: "route-a", SubjectPattern: "*", HashRule: "mystery", NodeId: "node-a", Status: "active"}},
		nodes:  map[string]*pb.PrimaryStoreNode{"node-a": {NodeId: "node-a", Status: "active"}},
	}
	if _, err := NewResolver(metadata).Resolve(context.Background(), "crypto", "kline", "BTCUSDT"); err == nil {
		t.Fatal("Resolve accepted an unsupported hash rule")
	}
}

func TestResolverRequiresExplicitShardIdentity(t *testing.T) {
	metadata := &resolverMetadata{
		routes: []*pb.PrimaryStoreRoute{{SpaceId: "crypto", DatasetId: "kline", RouteId: "route-a", SubjectPattern: "*", HashRule: "subject_id", NodeId: "node-a", Status: "active"}},
		nodes:  map[string]*pb.PrimaryStoreNode{"node-a": {NodeId: "node-a", Status: "active"}},
	}
	metadata.deviceWithoutShard = true
	if _, err := NewResolver(metadata).Resolve(context.Background(), "crypto", "kline", "BTCUSDT"); err == nil {
		t.Fatal("Resolve accepted a topology without shard_id")
	}
}

func TestResolverUsesPerNodeGatewayTarget(t *testing.T) {
	metadata := &resolverMetadata{
		routes: []*pb.PrimaryStoreRoute{{SpaceId: "crypto", DatasetId: "kline", RouteId: "route-a", SubjectPattern: "*", HashRule: "subject_id", NodeId: "node-a", Status: "active"}},
		nodes: map[string]*pb.PrimaryStoreNode{"node-a": {
			NodeId:     "node-a",
			Status:     "active",
			Attributes: map[string]string{"gateway_target": "gateway-a:11000", "gateway_node_id": "gateway-a"},
		}},
	}
	target, err := NewResolver(metadata).Resolve(context.Background(), "crypto", "kline", "BTCUSDT")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if target.GetGatewayTarget() != "gateway-a:11000" || target.GetGatewayNodeId() != "gateway-a" {
		t.Fatalf("gateway target = %q/%q, want gateway-a:11000/gateway-a", target.GetGatewayTarget(), target.GetGatewayNodeId())
	}
}

type resolverMetadata struct {
	routes             []*pb.PrimaryStoreRoute
	nodes              map[string]*pb.PrimaryStoreNode
	deviceWithoutShard bool
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

func (m *resolverMetadata) ListDevices(_ context.Context, nodeID string, _ string, _ *pb.Page) ([]*pb.Device, *pb.PageResult, error) {
	device := &pb.Device{DeviceId: "pebble-" + nodeID, NodeId: nodeID, Engine: "pebble", Status: "active"}
	if !m.deviceWithoutShard {
		device.Attributes = map[string]string{"shard_id": "shard-" + nodeID}
	}
	return []*pb.Device{device}, &pb.PageResult{}, nil
}
