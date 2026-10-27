package primarystore

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

// factorRowsNode 是记录下发请求的假数据节点。
type factorRowsNode struct {
	write func(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error)
}

func (n *factorRowsNode) UpsertFields(ctx context.Context, req *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
	if n.write == nil {
		return &pb.UpsertFieldsRsp{RetInfo: successRetInfo()}, nil
	}
	return n.write(ctx, req)
}

func (n *factorRowsNode) ReadRows(context.Context, *pb.ReadRowsReq) (*pb.ReadRowsRsp, error) {
	return nil, nil
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

func factorRowsMetadata() metadata.RequestSnapshot {
	return factorRowsSnapshot{
		dataset: &pb.Dataset{SpaceId: "space", DatasetId: "result", DataNodeId: "node-owner", Status: "active", Attributes: map[string]string{"owner_module": "factor", "dataset_role": "factor_result"}},
		columns: []*pb.DatasetColumn{{ColumnName: "value", OriginId: "factor-id", Status: "active", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING}},
	}
}

func factorRowsRecord(field string) *pb.RowFieldUpsert {
	return &pb.RowFieldUpsert{
		Key:    &pb.RowKey{SpaceId: "space", DatasetId: "result", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "row-1", Version: "v1"}}},
		Fields: []*pb.FieldValue{{FieldId: field, Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "value"}}}},
	}
}

// newFactorResultService 用给定的元数据快照与数据节点构造主库；resolved 记录是否走到了路由。
func newFactorResultService(t *testing.T, snapshot func() metadata.RequestSnapshot, node *factorRowsNode, resolved *bool) *Service {
	t.Helper()
	svc, err := New(Options{
		Resolver: func(context.Context, string, string) (DataNodeClient, error) {
			*resolved = true
			return node, nil
		},
		Snapshot:  snapshot,
		Validator: NewMetadataValidator(nil),
		AuthSigner: func(auth *pb.AuthInfo) (*pb.AuthInfo, error) {
			return &pb.AuthInfo{AppId: auth.GetAppId(), AppKey: "signed"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func factorResultRequest(appID, commitID string, rows ...*pb.RowFieldUpsert) *pb.PrimaryUpsertFieldsReq {
	return &pb.PrimaryUpsertFieldsReq{AuthInfo: &pb.AuthInfo{AppId: appID}, SpaceId: "space", DatasetId: "result", CommitId: commitID, Rows: rows}
}

// 请求的 space_id、dataset_id、commit_id 原样下发给数据集所在的数据节点。
func TestUpsertFieldsRoutesCommitToOwnerDataNode(t *testing.T) {
	resolved, written := false, 0
	node := &factorRowsNode{write: func(_ context.Context, req *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
		written++
		if req.GetSpaceId() != "space" || req.GetDatasetId() != "result" || req.GetCommitId() != "commit-1" || len(req.GetRows()) != 1 {
			t.Fatalf("routed request=%v", req)
		}
		return &pb.UpsertFieldsRsp{RetInfo: successRetInfo(), Keys: []*pb.RowKey{req.GetRows()[0].GetKey()}}, nil
	}}
	svc := newFactorResultService(t, factorRowsMetadata, node, &resolved)
	rsp, err := svc.UpsertFields(context.Background(), factorResultRequest("factor", "commit-1", factorRowsRecord("value")))
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(rsp.GetKeys()) != 1 {
		t.Fatalf("response=%v err=%v", rsp, err)
	}
	if !resolved || written != 1 {
		t.Fatalf("resolved=%v written=%d", resolved, written)
	}
}

// 通用校验拦住未登记的列与停用的数据集；行的数据集与请求不一致、请求缺少元数据快照同样在路由之前拒绝。
func TestUpsertFieldsRejectsBeforeRouting(t *testing.T) {
	inactive := func() metadata.RequestSnapshot {
		s := factorRowsMetadata().(factorRowsSnapshot)
		s.dataset = proto.Clone(s.dataset).(*pb.Dataset)
		s.dataset.Status = "disabled"
		return s
	}
	otherDataset := factorRowsRecord("value")
	otherDataset.Key.DatasetId = "other"
	cases := []struct {
		name     string
		snapshot func() metadata.RequestSnapshot
		row      *pb.RowFieldUpsert
		code     pb.ErrorCode
	}{
		{"未登记的列", factorRowsMetadata, factorRowsRecord("unregistered"), pb.ErrorCode_INVALID_PARAM},
		{"停用的数据集", inactive, factorRowsRecord("value"), pb.ErrorCode_INVALID_PARAM},
		{"行属于别的数据集", factorRowsMetadata, otherDataset, pb.ErrorCode_INVALID_PARAM},
		{"缺少元数据快照", func() metadata.RequestSnapshot { return nil }, factorRowsRecord("value"), pb.ErrorCode_INVALID_PARAM},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolved := false
			svc := newFactorResultService(t, tc.snapshot, &factorRowsNode{}, &resolved)
			rsp, err := svc.UpsertFields(context.Background(), factorResultRequest("factor", "commit-1", tc.row))
			if err != nil || rsp.GetRetInfo().GetCode() != tc.code {
				t.Fatalf("response=%v err=%v", rsp, err)
			}
			if resolved {
				t.Fatal("rejected write reached DataNode resolver")
			}
		})
	}
}

// __params 是一列声明过的 json 字段：按 json_value 原样转发给 DataNode，Storage 不解析里面的内容；没注册这一列的数据集照旧拒绝。
func TestUpsertFieldsForwardsTheParamsJSONColumn(t *testing.T) {
	const params = `{"boll":{"window":20},"ema__20_50000":{"span":20,"warmup":50000}}`
	var forwarded *pb.UpsertFieldsReq
	node := &factorRowsNode{write: func(_ context.Context, req *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
		forwarded = req
		return &pb.UpsertFieldsRsp{RetInfo: successRetInfo(), Keys: []*pb.RowKey{req.GetRows()[0].GetKey()}}, nil
	}}
	snapshot := func(withParams bool) func() metadata.RequestSnapshot {
		return func() metadata.RequestSnapshot {
			s := factorRowsMetadata().(factorRowsSnapshot)
			s.dataset = proto.Clone(s.dataset).(*pb.Dataset)
			s.dataset.Freq = "5m"
			s.columns = []*pb.DatasetColumn{{ColumnName: "value", Status: "active", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}
			if withParams {
				s.columns = append(s.columns, &pb.DatasetColumn{ColumnName: "__params", OriginId: "__params", Status: "active", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_JSON})
			}
			return s
		}
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

	resolved := false
	rsp, err := newFactorResultService(t, snapshot(true), node, &resolved).UpsertFields(context.Background(), factorResultRequest("factor", "commit-params", row()))
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
	rsp, err = newFactorResultService(t, snapshot(false), node, &resolved).UpsertFields(context.Background(), factorResultRequest("factor", "commit-params", row()))
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM || forwarded != nil {
		t.Fatalf("unregistered __params column: response=%v err=%v forwarded=%v", rsp, err, forwarded)
	}
}
