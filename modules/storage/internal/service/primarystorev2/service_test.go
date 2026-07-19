package primarystorev2

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestPrimaryRoutesAndValidatesBeforeDataNode(t *testing.T) {
	node, err := datanode.NewService(datanode.Options{NodeID: "node-a", Pebble: pebble.Options{NodeID: "node-a", Path: filepath.Join(t.TempDir(), "node")}})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	svc, err := New(Options{Node: node, AuthSigner: func(_ *pb.AuthInfo) (*pb.AuthInfo, error) {
		return &pb.AuthInfo{AppId: "primary", AppKey: datanode.ServiceAuthKey("node-a", "primary")}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}
	rsp, err := svc.WriteFields(context.Background(), &pb.PrimaryWriteFieldsReq{AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "ignored"}, Rows: []*pb.RowFieldUpsert{{Key: key, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1.2}}}}}}})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("write rsp=%v err=%v", rsp, err)
	}
	bad, err := svc.WriteFields(context.Background(), &pb.PrimaryWriteFieldsReq{Rows: []*pb.RowFieldUpsert{{Key: key}}})
	if err != nil || bad.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("bad rsp=%v err=%v", bad, err)
	}
}
