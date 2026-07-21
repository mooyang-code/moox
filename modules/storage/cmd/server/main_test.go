package main

import (
	"context"
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
