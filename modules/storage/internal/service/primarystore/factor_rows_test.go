package primarystore

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type factorRowsNode struct {
	write func(context.Context, *pb.WriteFactorRowsReq) (*pb.WriteFactorRowsRsp, error)
}

func (n *factorRowsNode) UpsertFields(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
	return nil, nil
}

func (n *factorRowsNode) ReadFields(context.Context, *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
	return nil, nil
}

func (n *factorRowsNode) WriteFactorRows(ctx context.Context, req *pb.WriteFactorRowsReq) (*pb.WriteFactorRowsRsp, error) {
	return n.write(ctx, req)
}

type factorRowsSnapshot struct {
	dataset *pb.Dataset
	columns []*pb.DatasetColumn
}

func (s factorRowsSnapshot) GetDataset(string, string) (*pb.Dataset, bool) {
	return s.dataset, s.dataset != nil
}

func (factorRowsSnapshot) GetDataNode(string) (*pb.DataNode, bool) { return nil, false }

func (s factorRowsSnapshot) ListDatasetColumns(string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return s.columns, &pb.PageResult{Total: uint32(len(s.columns))}, nil
}

func TestWriteFactorRowsRejectsNonFactorResultDataset(t *testing.T) {
	resolved := false
	svc, err := New(Options{
		Resolver: func(context.Context, string, string) (DataNodeClient, error) {
			resolved = true
			return &factorRowsNode{}, nil
		},
		Snapshot: func() metadata.RequestSnapshot {
			return factorRowsSnapshot{dataset: &pb.Dataset{SpaceId: "space", DatasetId: "result", Attributes: map[string]string{"dataset_role": "raw_collection"}}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := svc.WriteFactorRows(context.Background(), &pb.PrimaryWriteFactorRowsReq{
		AuthInfo: &pb.AuthInfo{AppId: "factor"}, SpaceId: "space", DatasetId: "result", CommitId: "commit-1", Rows: []*pb.RowFieldUpsert{factorRowsRecord("value")},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("response=%v err=%v", rsp, err)
	}
	if resolved {
		t.Fatal("non-factor-result Dataset reached DataNode resolver")
	}
}

func TestWriteFactorRowsRejectsNonFactorCaller(t *testing.T) {
	resolved := false
	svc, err := New(Options{
		Resolver: func(context.Context, string, string) (DataNodeClient, error) {
			resolved = true
			return &factorRowsNode{}, nil
		},
		Snapshot: factorRowsMetadata,
	})
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := svc.WriteFactorRows(context.Background(), &pb.PrimaryWriteFactorRowsReq{
		AuthInfo: &pb.AuthInfo{AppId: "collector"}, SpaceId: "space", DatasetId: "result", CommitId: "commit-1", Rows: []*pb.RowFieldUpsert{factorRowsRecord("value")},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("response=%v err=%v", rsp, err)
	}
	if resolved {
		t.Fatal("non-Factor caller reached DataNode resolver")
	}
}

func TestWriteFactorRowsRoutesToOwnerDataNode(t *testing.T) {
	resolved := 0
	written := 0
	owner := &factorRowsNode{write: func(_ context.Context, req *pb.WriteFactorRowsReq) (*pb.WriteFactorRowsRsp, error) {
		written++
		if req.GetSpaceId() != "space" || req.GetDatasetId() != "result" || req.GetNodeId() != "node-owner" {
			t.Fatalf("routed request=%v", req)
		}
		return &pb.WriteFactorRowsRsp{RetInfo: successRetInfo(), RowsWritten: uint64(len(req.GetRows()))}, nil
	}}
	svc, err := New(Options{
		Resolver: func(_ context.Context, spaceID, datasetID string) (DataNodeClient, error) {
			resolved++
			if spaceID != "space" || datasetID != "result" {
				t.Fatalf("resolved unexpected Dataset %s/%s", spaceID, datasetID)
			}
			return owner, nil
		},
		Snapshot: factorRowsMetadata,
		AuthSigner: func(*pb.AuthInfo) (*pb.AuthInfo, error) {
			return &pb.AuthInfo{AppId: "factor", AppKey: "signed"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := svc.WriteFactorRows(context.Background(), &pb.PrimaryWriteFactorRowsReq{
		AuthInfo: &pb.AuthInfo{AppId: "factor"}, SpaceId: "space", DatasetId: "result", CommitId: "commit-1", Rows: []*pb.RowFieldUpsert{factorRowsRecord("value")},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("response=%v err=%v", rsp, err)
	}
	if resolved != 1 || written != 1 || rsp.GetRowsWritten() != 1 {
		t.Fatalf("resolved=%d written=%d response=%v", resolved, written, rsp)
	}
}

func TestWriteFactorRowsRejectsUnknownColumns(t *testing.T) {
	resolved := false
	svc, err := New(Options{
		Resolver: func(context.Context, string, string) (DataNodeClient, error) {
			resolved = true
			return &factorRowsNode{}, nil
		},
		Snapshot: factorRowsMetadata,
	})
	if err != nil {
		t.Fatal(err)
	}
	row := factorRowsRecord("unregistered")
	rsp, err := svc.WriteFactorRows(context.Background(), &pb.PrimaryWriteFactorRowsReq{
		AuthInfo: &pb.AuthInfo{AppId: "factor"}, SpaceId: "space", DatasetId: "result", CommitId: "commit-1", Rows: []*pb.RowFieldUpsert{row},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("response=%v err=%v", rsp, err)
	}
	if resolved {
		t.Fatal("unknown column reached DataNode resolver")
	}
}

func factorRowsMetadata() metadata.RequestSnapshot {
	return factorRowsSnapshot{
		dataset: &pb.Dataset{SpaceId: "space", DatasetId: "result", DataNodeId: "node-owner", Status: "active", Attributes: map[string]string{"dataset_role": "factor_result"}},
		columns: []*pb.DatasetColumn{{ColumnName: "value", OriginId: "value", Status: "active"}},
	}
}

func factorRowsRecord(field string) *pb.RowFieldUpsert {
	return &pb.RowFieldUpsert{
		Key:    &pb.RowKey{SpaceId: "space", DatasetId: "result", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "row-1", Version: "v1"}}},
		Fields: []*pb.FieldValue{{FieldId: field, Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "value"}}}},
	}
}
