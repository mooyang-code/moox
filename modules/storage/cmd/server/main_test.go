package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	primarystore "github.com/mooyang-code/moox/modules/storage/internal/service/primarystore"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type cleanupDatasetReader struct{}

func (cleanupDatasetReader) ListDatasets(context.Context, metadata.DatasetQuery) ([]*pb.Dataset, *pb.PageResult, error) {
	return []*pb.Dataset{{
		SpaceId: "space", DatasetId: "prices", DataNodeId: "node-a",
		DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, KeepDuration: "48h", Status: "active",
	}}, &pb.PageResult{Page: 1, Size: 1000}, nil
}

type cleanupNode struct{ request *pb.CleanupExpiredBucketsReq }

func (*cleanupNode) UpsertFields(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
	return nil, nil
}
func (*cleanupNode) ReadFields(context.Context, *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
	return nil, nil
}
func (*cleanupNode) GetNodeState(context.Context, *pb.GetNodeStateReq) (*pb.GetNodeStateRsp, error) {
	return nil, nil
}
func (n *cleanupNode) CleanupExpiredBuckets(_ context.Context, req *pb.CleanupExpiredBucketsReq) (*pb.CleanupExpiredBucketsRsp, error) {
	n.request = req
	return &pb.CleanupExpiredBucketsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}, nil
}
func TestCleanupDatasetsUsesSpaceAndKeepDuration(t *testing.T) {
	node := &cleanupNode{}
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	err := cleanupDatasets(context.Background(), cleanupDatasetReader{}, func(context.Context, string, string) (pb.DataNodeRuntimeService, error) {
		return node, nil
	}, &pb.AuthInfo{AppId: "primary", AppKey: "key"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if node.request == nil || node.request.GetSpaceId() != "space" || node.request.GetDatasetId() != "prices" ||
		node.request.GetBeforeBucketStart() != "2026-07-18T00:00:00.000000000Z" {
		t.Fatalf("request=%v", node.request)
	}
}

func TestStorageEventBusConfigLoadsCredentialFromExplicitEnv(t *testing.T) {
	credentialFile := filepath.Join(t.TempDir(), "storage-eventbus.yaml")
	if err := os.WriteFile(credentialFile, []byte("version: 1\nusername: storage-eventbus\ntoken: storage-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOOX_STORAGE_EVENTBUS_CREDENTIAL_FILE", credentialFile)
	t.Setenv("MOOX_STORAGE_CONFIG", "")
	t.Setenv("MOOX_EVENTBUS_RECONNECT_BUFFER_BYTES", "")

	got, err := storageEventBusConfig([]string{"nats://127.0.0.1:4222"}, "storage-view")
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != "storage-eventbus" || got.Password != "storage-secret" {
		t.Fatalf("credential config = username %q/password %q", got.Username, got.Password)
	}
	if got.ReconnectBufferBytes != storageEventBusReconnectBufferBytes {
		t.Fatalf("reconnect buffer = %d, want %d", got.ReconnectBufferBytes, storageEventBusReconnectBufferBytes)
	}
}

func TestStorageEventBusConfigHonorsExplicitReconnectBuffer(t *testing.T) {
	t.Setenv("MOOX_STORAGE_EVENTBUS_CREDENTIAL_FILE", "")
	t.Setenv("MOOX_STORAGE_CONFIG", "")
	t.Setenv("MOOX_EVENTBUS_RECONNECT_BUFFER_BYTES", "0")

	got, err := storageEventBusConfig([]string{"nats://127.0.0.1:4222"}, "storage-view")
	if err != nil {
		t.Fatal(err)
	}
	if got.ReconnectBufferBytes != 0 {
		t.Fatalf("reconnect buffer = %d, want explicit 0", got.ReconnectBufferBytes)
	}
}

func TestStorageEventBusConfigRejectsInvalidReconnectBuffer(t *testing.T) {
	t.Setenv("MOOX_STORAGE_EVENTBUS_CREDENTIAL_FILE", "")
	t.Setenv("MOOX_STORAGE_CONFIG", "")
	for _, value := range []string{"garbage", "-1"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("MOOX_EVENTBUS_RECONNECT_BUFFER_BYTES", value)
			if _, err := storageEventBusConfig([]string{"nats://127.0.0.1:4222"}, "storage-view"); err == nil {
				t.Fatalf("reconnect buffer %q was accepted", value)
			}
		})
	}
}

func TestStorageEventBusConfigLoadsCredentialFromStorageConfig(t *testing.T) {
	dir := t.TempDir()
	credentialFile := filepath.Join(dir, "storage-eventbus.yaml")
	if err := os.WriteFile(credentialFile, []byte("version: 1\nusername: storage-eventbus\ntoken: storage-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(dir, "storage.yaml")
	if err := os.WriteFile(configFile, []byte("storage:\n  eventbus:\n    credential_file: "+credentialFile+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOOX_STORAGE_EVENTBUS_CREDENTIAL_FILE", "")
	t.Setenv("MOOX_STORAGE_CONFIG", configFile)

	got, err := storageEventBusConfig([]string{"nats://127.0.0.1:4222"}, "storage-node")
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != "storage-eventbus" || got.Password != "storage-secret" {
		t.Fatalf("credential config = username %q/password %q", got.Username, got.Password)
	}
}

func TestStorageViewConsumerOptionsUseCodeOwnedDeliverySettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "storage.yaml")
	if err := os.WriteFile(path, []byte("storage:\n  eventbus:\n    credential_file: \"\"\n  view:\n    fetch_batch: 4\n    max_workers: 2\n    ordering: subject\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOOX_STORAGE_CONFIG", path)
	opts, err := storageViewConsumerOptions()
	if err != nil {
		t.Fatal(err)
	}
	if opts.Consumer != "storage_view" || opts.FetchBatch != 4 || opts.MaxWorkers != 2 || opts.MaxAckPending != 8 || opts.AckWaitMS != 120000 {
		t.Fatalf("consumer options = %+v", opts)
	}
}

type resolverSnapshot struct {
	dataset *pb.Dataset
	node    *pb.DataNode
}

func (s resolverSnapshot) GetDataset(spaceID, datasetID string) (*pb.Dataset, bool) {
	if s.dataset == nil || s.dataset.GetSpaceId() != spaceID || s.dataset.GetDatasetId() != datasetID {
		return nil, false
	}
	return s.dataset, true
}

func (s resolverSnapshot) GetDataNode(nodeID string) (*pb.DataNode, bool) {
	if s.node == nil || s.node.GetNodeId() != nodeID {
		return nil, false
	}
	return s.node, true
}

func (resolverSnapshot) ListDatasetColumns(string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return nil, nil, nil
}

type resolverRuntime struct {
	target string
	writes int
	reads  int
}

func (r *resolverRuntime) UpsertFields(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
	r.writes++
	return &pb.UpsertFieldsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}, nil
}
func (r *resolverRuntime) ReadFields(context.Context, *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
	r.reads++
	return &pb.ReadFieldsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}, nil
}
func (*resolverRuntime) GetNodeState(context.Context, *pb.GetNodeStateReq) (*pb.GetNodeStateRsp, error) {
	return &pb.GetNodeStateRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}, nil
}
func (*resolverRuntime) CleanupExpiredBuckets(context.Context, *pb.CleanupExpiredBucketsReq) (*pb.CleanupExpiredBucketsRsp, error) {
	return &pb.CleanupExpiredBucketsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}, nil
}

func TestResolveDataNodeUsesActiveDatasetNodeAndTargetOnly(t *testing.T) {
	base := resolverSnapshot{
		dataset: &pb.Dataset{SpaceId: "space", DatasetId: "dataset", DataNodeId: "node-a", Status: "active"},
		node:    &pb.DataNode{NodeId: "node-a", Status: "active", ServiceTarget: "ip://127.0.0.1:20107"},
	}
	resolver := newDataNodeResolver(func() metadata.RequestSnapshot { return base }, func(target string) pb.DataNodeRuntimeService { return &resolverRuntime{target: target} })
	if _, err := resolver(context.Background(), "space", "dataset"); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		dataset *pb.Dataset
		node    *pb.DataNode
	}{
		{name: "unknown dataset", dataset: nil, node: base.node},
		{name: "disabled dataset", dataset: &pb.Dataset{SpaceId: "space", DatasetId: "dataset", DataNodeId: "node-a", Status: "disabled"}, node: base.node},
		{name: "missing node", dataset: base.dataset, node: nil},
		{name: "disabled node", dataset: base.dataset, node: &pb.DataNode{NodeId: "node-a", Status: "disabled", ServiceTarget: "ip://127.0.0.1:20107"}},
		{name: "empty target", dataset: base.dataset, node: &pb.DataNode{NodeId: "node-a", Status: "active"}},
		{name: "malformed target", dataset: base.dataset, node: &pb.DataNode{NodeId: "node-a", Status: "active", ServiceTarget: "http://127.0.0.1:20107"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := resolveDataNodeFromSnapshot(resolverSnapshot{dataset: tc.dataset, node: tc.node}, "space", "dataset")
			if err == nil {
				t.Fatal("expected routing failure")
			}
		})
	}
}

func TestDataNodeResolverReplacesProxyWhenTargetChanges(t *testing.T) {
	first := resolverSnapshot{
		dataset: &pb.Dataset{SpaceId: "space", DatasetId: "dataset", DataNodeId: "node-a", Status: "active"},
		node:    &pb.DataNode{NodeId: "node-a", Status: "active", ServiceTarget: "ip://127.0.0.1:20107"},
	}
	second := resolverSnapshot{dataset: first.dataset, node: &pb.DataNode{NodeId: "node-a", Status: "active", ServiceTarget: "ip://127.0.0.1:20108"}}
	current := metadata.RequestSnapshot(first)
	var targets []string
	resolver := newDataNodeResolver(func() metadata.RequestSnapshot { return current }, func(target string) pb.DataNodeRuntimeService {
		targets = append(targets, target)
		return &resolverRuntime{target: target}
	})
	firstProxy, err := resolver(context.Background(), "space", "dataset")
	if err != nil {
		t.Fatal(err)
	}
	current = second
	secondProxy, err := resolver(context.Background(), "space", "dataset")
	if err != nil {
		t.Fatal(err)
	}
	firstRuntime := firstProxy.(*resolverRuntime)
	secondRuntime := secondProxy.(*resolverRuntime)
	if firstRuntime.target == secondRuntime.target || fmt.Sprint(targets) != "[ip://127.0.0.1:20107 ip://127.0.0.1:20108]" {
		t.Fatalf("proxy refresh failed: first=%p second=%p targets=%v", firstProxy, secondProxy, targets)
	}
}

func TestPrimaryReadWriteUsePublishedSnapshotAndFakeRuntime(t *testing.T) {
	snapshot := resolverSnapshot{
		dataset: &pb.Dataset{SpaceId: "space", DatasetId: "dataset", DataNodeId: "node-a", Status: "active"},
		node:    &pb.DataNode{NodeId: "node-a", Status: "active", ServiceTarget: "ip://127.0.0.1:20107"},
	}
	var runtime *resolverRuntime
	resolver := newDataNodeResolver(func() metadata.RequestSnapshot { return snapshot }, func(target string) pb.DataNodeRuntimeService {
		runtime = &resolverRuntime{target: target}
		return runtime
	})
	svc, err := primarystore.New(primarystore.Options{Resolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	key := &pb.RowKey{SpaceId: "space", DatasetId: "dataset", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "record", Version: "1"}}}
	write, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller"},
		Rows:     []*pb.RowFieldUpsert{{Key: key, Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "ok"}}}}}},
	})
	if err != nil || write.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("write=%v err=%v", write, err)
	}
	read, err := svc.ReadFields(context.Background(), &pb.PrimaryReadFieldsReq{AuthInfo: &pb.AuthInfo{AppId: "caller"}, Keys: []*pb.RowKey{key}})
	if err != nil || read.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("read=%v err=%v", read, err)
	}
	if runtime == nil || runtime.writes != 1 || runtime.reads != 1 || runtime.target != "ip://127.0.0.1:20107" {
		t.Fatalf("runtime=%+v", runtime)
	}
}
