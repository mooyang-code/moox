package primarystore

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type recordingNode struct {
	write func(context.Context, *pb.WriteFieldsReq) (*pb.WriteFieldsRsp, error)
	read  func(context.Context, *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error)
}

func (n *recordingNode) WriteFields(ctx context.Context, req *pb.WriteFieldsReq) (*pb.WriteFieldsRsp, error) {
	return n.write(ctx, req)
}

func (n *recordingNode) ReadFields(ctx context.Context, req *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
	return n.read(ctx, req)
}

func (n *recordingNode) GetNodeState(context.Context, *pb.GetNodeStateReq) (*pb.GetNodeStateRsp, error) {
	return &pb.GetNodeStateRsp{}, nil
}

func (n *recordingNode) CleanupExpiredBuckets(context.Context, *pb.CleanupExpiredBucketsReq) (*pb.CleanupExpiredBucketsRsp, error) {
	return &pb.CleanupExpiredBucketsRsp{}, nil
}

func TestPrimaryRoutesAndValidatesBeforeDataNode(t *testing.T) {
	node, err := datanode.NewService(datanode.Options{NodeID: "node-a", AuthSecret: "node-secret", Pebble: pebble.Options{NodeID: "node-a", Path: filepath.Join(t.TempDir(), "node")}})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	svc, err := New(Options{Node: node, AuthSigner: func(_ *pb.AuthInfo) (*pb.AuthInfo, error) {
		return &pb.AuthInfo{AppId: "primary", AppKey: datanode.ServiceAuthKey("node-secret", "primary")}, nil
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
	if err != nil || bad.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("bad rsp=%v err=%v", bad, err)
	}
}

func TestPrimaryRoutesSameDatasetInDifferentSpacesSeparately(t *testing.T) {
	var resolved []string
	node := &recordingNode{
		write: func(_ context.Context, req *pb.WriteFieldsReq) (*pb.WriteFieldsRsp, error) {
			keys := make([]*pb.RowKey, 0, len(req.GetRows()))
			for _, row := range req.GetRows() {
				keys = append(keys, row.GetKey())
			}
			return &pb.WriteFieldsRsp{RetInfo: successRetInfo(), Keys: keys}, nil
		},
		read: func(_ context.Context, req *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
			rows := make([]*pb.RowFieldValues, 0, len(req.GetKeys()))
			for _, key := range req.GetKeys() {
				rows = append(rows, &pb.RowFieldValues{Key: key})
			}
			return &pb.ReadFieldsRsp{RetInfo: successRetInfo(), Rows: rows}, nil
		},
	}
	svc, err := New(Options{
		Resolver: func(_ context.Context, spaceID, datasetID string) (pb.DataNodeService, error) {
			resolved = append(resolved, spaceID+"/"+datasetID)
			return node, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	row := func(space, record string) *pb.RowFieldUpsert {
		return &pb.RowFieldUpsert{
			Key:    &pb.RowKey{SpaceId: space, DatasetId: "shared", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: record, Version: "1"}}},
			Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: record}}}},
		}
	}
	rsp, err := svc.WriteFields(context.Background(), &pb.PrimaryWriteFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		Rows:     []*pb.RowFieldUpsert{row("space-a", "a"), row("space-b", "b")},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("write rsp=%v err=%v", rsp, err)
	}
	if !reflect.DeepEqual(resolved, []string{"space-a/shared", "space-b/shared"}) {
		t.Fatalf("resolved=%v", resolved)
	}
}

func TestPrimaryReadPreservesRequestOrderAcrossDatasets(t *testing.T) {
	node := &recordingNode{
		write: func(context.Context, *pb.WriteFieldsReq) (*pb.WriteFieldsRsp, error) {
			return &pb.WriteFieldsRsp{RetInfo: successRetInfo()}, nil
		},
		read: func(_ context.Context, req *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
			rows := make([]*pb.RowFieldValues, 0, len(req.GetKeys()))
			for _, key := range req.GetKeys() {
				rows = append(rows, &pb.RowFieldValues{Key: key})
			}
			return &pb.ReadFieldsRsp{RetInfo: successRetInfo(), Rows: rows}, nil
		},
	}
	svc, err := New(Options{Node: node})
	if err != nil {
		t.Fatal(err)
	}
	key := func(dataset, record string) *pb.RowKey {
		return &pb.RowKey{SpaceId: "s", DatasetId: dataset, Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: record, Version: "1"}}}
	}
	keys := []*pb.RowKey{key("b", "first"), key("a", "second"), key("b", "third")}
	rsp, err := svc.ReadFields(context.Background(), &pb.PrimaryReadFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		Keys:     keys,
		FieldIds: []string{"value"},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("read rsp=%v err=%v", rsp, err)
	}
	for i, row := range rsp.GetRows() {
		if row.GetKey().GetRecord().GetRecordId() != keys[i].GetRecord().GetRecordId() {
			t.Fatalf("row %d=%v want=%v", i, row.GetKey(), keys[i])
		}
	}
}

func TestPrimaryRejectsRecordWriteWithoutVersion(t *testing.T) {
	node := &recordingNode{
		write: func(context.Context, *pb.WriteFieldsReq) (*pb.WriteFieldsRsp, error) {
			t.Fatal("write should not reach DataNode")
			return nil, nil
		},
		read: func(context.Context, *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
			return nil, nil
		},
	}
	svc, err := New(Options{Node: node})
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := svc.WriteFields(context.Background(), &pb.PrimaryWriteFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		Rows: []*pb.RowFieldUpsert{{
			Key:    &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r"}}},
			Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "x"}}}},
		}},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("rsp=%v err=%v", rsp, err)
	}
}

func TestPrimaryReadFieldsReturnsResolvedLatestRecordVersion(t *testing.T) {
	node, err := datanode.NewService(datanode.Options{
		NodeID: "node-a", AuthSecret: "node-secret",
		Pebble: pebble.Options{NodeID: "node-a", Path: filepath.Join(t.TempDir(), "node")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	svc, err := New(Options{
		Node: node,
		AuthSigner: func(*pb.AuthInfo) (*pb.AuthInfo, error) {
			return &pb.AuthInfo{AppId: "primary", AppKey: datanode.ServiceAuthKey("node-secret", "primary")}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"1", "2"} {
		key := &pb.RowKey{SpaceId: "space", DatasetId: "records", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: version}}}
		rsp, err := svc.WriteFields(context.Background(), &pb.PrimaryWriteFieldsReq{
			AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
			Rows: []*pb.RowFieldUpsert{{
				Key: key,
				Fields: []*pb.FieldValue{{
					FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: version}},
				}},
			}},
		})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("write version=%s rsp=%v err=%v", version, rsp, err)
		}
	}
	rsp, err := svc.ReadFields(context.Background(), &pb.PrimaryReadFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		Keys: []*pb.RowKey{{
			SpaceId: "space", DatasetId: "records",
			Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r"}},
		}},
		FieldIds: []string{"value"},
	})
	if err != nil || len(rsp.GetRows()) != 1 || rsp.GetRows()[0].GetKey().GetRecord().GetVersion() != "2" {
		t.Fatalf("rsp=%v err=%v", rsp, err)
	}
}

func successRetInfo() *pb.RetInfo {
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}
}
