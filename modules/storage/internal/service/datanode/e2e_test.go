package datanode_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestTwoDataNodesHostIndependentDatasets(t *testing.T) {
	ctx := context.Background()
	nodeA, err := datanode.NewService(datanode.Options{NodeID: "node-a", Pebble: pebble.Options{Path: filepath.Join(t.TempDir(), "a"), NodeID: "node-a"}})
	if err != nil {
		t.Fatal(err)
	}
	defer nodeA.Close()
	nodeB, err := datanode.NewService(datanode.Options{NodeID: "node-b", Pebble: pebble.Options{Path: filepath.Join(t.TempDir(), "b"), NodeID: "node-b"}})
	if err != nil {
		t.Fatal(err)
	}
	defer nodeB.Close()
	row := func(dataset, field, value string) *pb.RowFieldUpsert {
		return &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: "quant", DatasetId: dataset, Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}, Fields: []*pb.FieldValue{{FieldId: field, Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: value}}}}}
	}
	auth := &pb.AuthInfo{AppId: "storage-primary", AppKey: "e2e"}
	if rsp, _ := nodeA.WriteFields(ctx, &pb.WriteFieldsReq{AuthInfo: auth, NodeId: "node-a", Rows: []*pb.RowFieldUpsert{row("prices", "close", "100")}}); rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("node A write: %v", rsp.GetRetInfo())
	}
	if rsp, _ := nodeB.WriteFields(ctx, &pb.WriteFieldsReq{AuthInfo: auth, NodeId: "node-b", Rows: []*pb.RowFieldUpsert{row("factors", "momentum", "1.2")}}); rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("node B write: %v", rsp.GetRetInfo())
	}
	read, err := nodeA.ReadFields(ctx, &pb.ReadFieldsReq{AuthInfo: auth, NodeId: "node-a", DatasetId: "prices", Keys: []*pb.RowKey{row("prices", "close", "100").GetKey()}, FieldIds: []string{"close"}})
	if err != nil || len(read.GetRows()) != 1 || len(read.GetRows()[0].GetFields()) != 1 {
		t.Fatalf("read node A rows=%v err=%v", read, err)
	}
}
