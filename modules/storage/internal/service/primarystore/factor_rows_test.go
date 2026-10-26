package primarystore

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

type factorRowsNode struct {
	write func(context.Context, *pb.WriteFactorRowsReq) (*pb.WriteFactorRowsRsp, error)
}

func (n *factorRowsNode) UpsertFields(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
	return nil, nil
}

func (n *factorRowsNode) ReadRows(context.Context, *pb.ReadRowsReq) (*pb.ReadRowsRsp, error) {
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

func TestWriteFactorRowsRejectsMismatchedDatasetOwner(t *testing.T) {
	for _, attrs := range []map[string]string{
		{"owner_module": "collector", "dataset_role": "factor_result", "write_owner": "factor"},
		{"owner_module": "factor", "dataset_role": "raw_collection", "write_owner": "factor"},
		{"owner_module": "factor", "dataset_role": "factor_result", "write_owner": "collector"},
	} {
		resolved := false
		svc, err := New(Options{
			Resolver: func(context.Context, string, string) (DataNodeClient, error) {
				resolved = true
				return &factorRowsNode{}, nil
			},
			Snapshot: func() metadata.RequestSnapshot {
				return factorRowsSnapshot{dataset: &pb.Dataset{
					SpaceId: "space", DatasetId: "result", Attributes: attrs,
				}}
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		rsp, err := svc.WriteFactorRows(context.Background(), &pb.PrimaryWriteFactorRowsReq{
			AuthInfo: &pb.AuthInfo{AppId: "factor"}, SpaceId: "space", DatasetId: "result", CommitId: "commit-1",
			Rows: []*pb.RowFieldUpsert{factorRowsRecord("value")},
		})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
			t.Fatalf("attrs=%v response=%v err=%v", attrs, rsp, err)
		}
		if resolved {
			t.Fatalf("mismatched Dataset owner reached DataNode: attrs=%v", attrs)
		}
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

func TestWriteFactorRowsDoesNotTreatOriginIDAsColumnName(t *testing.T) {
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
		AuthInfo: &pb.AuthInfo{AppId: "factor"}, SpaceId: "space", DatasetId: "result", CommitId: "commit-origin-id",
		Rows: []*pb.RowFieldUpsert{factorRowsRecord("factor-id")},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("response=%v err=%v", rsp, err)
	}
	if resolved {
		t.Fatal("origin_id was treated as a writable column name")
	}
}

func factorRowsMetadata() metadata.RequestSnapshot {
	return factorRowsSnapshot{
		dataset: &pb.Dataset{SpaceId: "space", DatasetId: "result", DataNodeId: "node-owner", Status: "active", Attributes: map[string]string{"owner_module": "factor", "dataset_role": "factor_result", "write_owner": "factor"}},
		columns: []*pb.DatasetColumn{{ColumnName: "value", OriginId: "factor-id", Status: "active"}},
	}
}

func factorRowsRecord(field string) *pb.RowFieldUpsert {
	return &pb.RowFieldUpsert{
		Key:    &pb.RowKey{SpaceId: "space", DatasetId: "result", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "row-1", Version: "v1"}}},
		Fields: []*pb.FieldValue{{FieldId: field, Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "value"}}}},
	}
}

func TestWriteFactorRowsRejectsInactiveDatasetAndRowAttributes(t *testing.T) {
	cases := []struct {
		name     string
		snapshot func() metadata.RequestSnapshot
		row      *pb.RowFieldUpsert
	}{
		{
			name: "inactive Dataset",
			snapshot: func() metadata.RequestSnapshot {
				snapshot := factorRowsMetadata().(factorRowsSnapshot)
				snapshot.dataset = proto.Clone(snapshot.dataset).(*pb.Dataset)
				snapshot.dataset.Status = "disabled"
				return snapshot
			},
			row: factorRowsRecord("value"),
		},
		{
			name:     "row attributes",
			snapshot: factorRowsMetadata,
			row: func() *pb.RowFieldUpsert {
				row := factorRowsRecord("value")
				row.Attributes = map[string]*pb.TypedValue{"note": {Value: &pb.TypedValue_StringValue{StringValue: "x"}}}
				return row
			}(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolved := false
			svc, err := New(Options{
				Resolver: func(context.Context, string, string) (DataNodeClient, error) {
					resolved = true
					return &factorRowsNode{}, nil
				},
				Snapshot: tc.snapshot,
			})
			if err != nil {
				t.Fatal(err)
			}
			rsp, err := svc.WriteFactorRows(context.Background(), &pb.PrimaryWriteFactorRowsReq{
				AuthInfo: &pb.AuthInfo{AppId: "factor"}, SpaceId: "space", DatasetId: "result", CommitId: "commit-1", Rows: []*pb.RowFieldUpsert{tc.row},
			})
			if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
				t.Fatalf("response=%v err=%v", rsp, err)
			}
			if resolved {
				t.Fatal("rejected write reached DataNode resolver")
			}
		})
	}
}

// __params 是一列声明过的 json 字段：按 json_value 原样转发给 DataNode，Storage 不解析里面的内容；没注册这一列的数据集照旧拒绝。
func TestWriteFactorRowsForwardsTheParamsJSONColumn(t *testing.T) {
	const params = `{"boll":{"window":20},"ema__20_50000":{"span":20,"warmup":50000}}`
	var forwarded *pb.WriteFactorRowsReq
	owner := &factorRowsNode{write: func(_ context.Context, req *pb.WriteFactorRowsReq) (*pb.WriteFactorRowsRsp, error) {
		forwarded = req
		return &pb.WriteFactorRowsRsp{RetInfo: successRetInfo(), RowsWritten: uint64(len(req.GetRows()))}, nil
	}}
	withParams := func() metadata.RequestSnapshot {
		snapshot := factorRowsMetadata().(factorRowsSnapshot)
		snapshot.columns = append(snapshot.columns, &pb.DatasetColumn{ColumnName: "__params", OriginId: "__params", Status: "active", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_JSON})
		return snapshot
	}
	row := func() *pb.RowFieldUpsert {
		return &pb.RowFieldUpsert{
			Key: &pb.RowKey{SpaceId: "space", DatasetId: "result", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "5m", DataTime: "2026-10-25T16:35:00Z"}}},
			Fields: []*pb.FieldValue{
				{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1.5}}},
				{FieldId: "__params", Value: &pb.TypedValue{Value: &pb.TypedValue_JsonValue{JsonValue: params}}},
			},
		}
	}
	newService := func(snapshot func() metadata.RequestSnapshot) *Service {
		svc, err := New(Options{
			Resolver: func(context.Context, string, string) (DataNodeClient, error) { return owner, nil },
			Snapshot: snapshot,
			AuthSigner: func(*pb.AuthInfo) (*pb.AuthInfo, error) {
				return &pb.AuthInfo{AppId: "factor", AppKey: "signed"}, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return svc
	}
	request := func() *pb.PrimaryWriteFactorRowsReq {
		return &pb.PrimaryWriteFactorRowsReq{AuthInfo: &pb.AuthInfo{AppId: "factor"}, SpaceId: "space", DatasetId: "result", CommitId: "commit-params", Rows: []*pb.RowFieldUpsert{row()}}
	}

	rsp, err := newService(withParams).WriteFactorRows(context.Background(), request())
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("registered __params column: response=%v err=%v", rsp, err)
	}
	var got string
	for _, field := range forwarded.GetRows()[0].GetFields() {
		if field.GetFieldId() == "__params" {
			got = field.GetValue().GetJsonValue()
		}
	}
	if got != params {
		t.Fatalf("forwarded __params = %q, want %q", got, params)
	}

	forwarded = nil
	rsp, err = newService(factorRowsMetadata).WriteFactorRows(context.Background(), request())
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM || forwarded != nil {
		t.Fatalf("unregistered __params column: response=%v err=%v forwarded=%v", rsp, err, forwarded)
	}
}
