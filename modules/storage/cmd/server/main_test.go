package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type cleanupDatasetReader struct{}

func (cleanupDatasetReader) ListDatasets(context.Context, string, string, pb.DataKind, string, *pb.Page) ([]*pb.Dataset, *pb.PageResult, error) {
	return []*pb.Dataset{{
		SpaceId: "space", DatasetId: "prices", DataNodeId: "node-a",
		DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, KeepDuration: "48h", Status: "active",
	}}, &pb.PageResult{Page: 1, Size: 1000}, nil
}

type cleanupNode struct{ request *pb.CleanupExpiredBucketsReq }

func (*cleanupNode) WriteFields(context.Context, *pb.WriteFieldsReq) (*pb.WriteFieldsRsp, error) {
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
	err := cleanupDatasets(context.Background(), cleanupDatasetReader{}, func(context.Context, string, string) (pb.DataNodeService, error) {
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

	got, err := storageEventBusConfig([]string{"nats://127.0.0.1:4222"}, "storage-view")
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != "storage-eventbus" || got.Password != "storage-secret" {
		t.Fatalf("credential config = username %q/password %q", got.Username, got.Password)
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

func TestStorageViewConsumerOptionsUseConfiguredTopology(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "storage.yaml")
	if err := os.WriteFile(path, []byte("storage:\n  eventbus:\n    stream_name: MOOX_STORAGE_CUSTOM\n    consumer_name: storage_view_custom\n    max_ack_pending: 8\n  view:\n    fetch_batch: 4\n    max_workers: 2\n    ordering: subject\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOOX_STORAGE_CONFIG", path)
	opts, err := storageViewConsumerOptions()
	if err != nil {
		t.Fatal(err)
	}
	if opts.Stream != "MOOX_STORAGE_CUSTOM" || opts.Durable != "storage_view_custom" || opts.FetchBatch != 4 || opts.MaxWorkers != 2 || opts.MaxAckPending != 8 {
		t.Fatalf("consumer options = %+v", opts)
	}
}
