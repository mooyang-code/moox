package datanode

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestWriteFactorRowsChecksNodeIdentityAndInternalAuth(t *testing.T) {
	service, err := NewService(Options{
		NodeID: "node-owner", AuthSecret: "node-secret",
		Pebble: pebble.Options{NodeID: "node-owner", Path: filepath.Join(t.TempDir(), "node")},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	row := &pb.RowFieldUpsert{
		Key:    &pb.RowKey{SpaceId: "space", DatasetId: "result", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "row-1", Version: "v1"}}},
		Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "ok"}}}},
	}
	req := &pb.WriteFactorRowsReq{
		AuthInfo: &pb.AuthInfo{AppId: "primary", AppKey: ServiceAuthKey("node-secret", "primary")},
		NodeId:   "other-node", SpaceId: "space", DatasetId: "result", CommitId: "commit-1", Rows: []*pb.RowFieldUpsert{row},
	}
	wrongNode, err := service.WriteFactorRows(context.Background(), req)
	if err != nil || wrongNode.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("wrong-node response=%v err=%v", wrongNode, err)
	}
	req.NodeId = "node-owner"
	req.AuthInfo.AppKey = "invalid"
	unauthorized, err := service.WriteFactorRows(context.Background(), req)
	if err != nil || unauthorized.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("unauthorized response=%v err=%v", unauthorized, err)
	}
	req.AuthInfo.AppKey = ServiceAuthKey("node-secret", "primary")
	written, err := service.WriteFactorRows(context.Background(), req)
	if err != nil || written.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || written.GetRowsWritten() != 1 {
		t.Fatalf("write response=%v err=%v", written, err)
	}
}
