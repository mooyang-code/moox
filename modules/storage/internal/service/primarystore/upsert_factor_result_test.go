package primarystore

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

// factorRowsCall 是存储层收到的一次写入。
type factorRowsCall struct {
	spaceID, datasetID string
	rows               []*pb.RowFieldUpsert
}

type factorRowsSnapshot struct {
	dataset *pb.Dataset
	columns []*pb.DatasetColumn
}

func (s factorRowsSnapshot) GetDataset(string, string) (*pb.Dataset, bool) {
	return s.dataset, s.dataset != nil
}

func (s factorRowsSnapshot) ListDatasetColumns(string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return s.columns, &pb.PageResult{Total: uint32(len(s.columns))}, nil
}

func factorRowsMetadata() metadata.RequestSnapshot {
	return factorRowsSnapshot{
		dataset: &pb.Dataset{SpaceId: "space", DatasetId: "result", Status: "active", Attributes: map[string]string{"owner_module": "factor", "dataset_role": "factor_result"}},
		columns: []*pb.DatasetColumn{{ColumnName: "value", OriginId: "factor-id", Status: "active", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING}},
	}
}

func factorRowsRecord(field string) *pb.RowFieldUpsert {
	return &pb.RowFieldUpsert{
		Key:    &pb.RowKey{SpaceId: "space", DatasetId: "result", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "row-1", Version: "v1"}}},
		Fields: []*pb.FieldValue{{FieldId: field, Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "value"}}}},
	}
}

// newFactorResultService 用给定的元数据快照构造主库；calls 记录到达存储层的写入。
func newFactorResultService(t *testing.T, snapshot func() metadata.RequestSnapshot, calls *[]factorRowsCall) *Service {
	t.Helper()
	store := newHookStore(t)
	store.upsert = func(ctx context.Context, spaceID, datasetID string, rows []*pb.RowFieldUpsert) error {
		*calls = append(*calls, factorRowsCall{spaceID: spaceID, datasetID: datasetID, rows: rows})
		return store.Store.UpsertFields(ctx, spaceID, datasetID, rows)
	}
	svc, err := New(Options{Store: store, Snapshot: snapshot, Validator: NewMetadataValidator(nil)})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func factorResultRequest(appID string, rows ...*pb.RowFieldUpsert) *pb.PrimaryUpsertFieldsReq {
	return &pb.PrimaryUpsertFieldsReq{AuthInfo: &pb.AuthInfo{AppId: appID}, SpaceId: "space", DatasetId: "result", Rows: rows}
}

// 请求的 space_id、dataset_id 原样交给存储层。
func TestUpsertFieldsPassesDatasetToTheStore(t *testing.T) {
	var calls []factorRowsCall
	svc := newFactorResultService(t, factorRowsMetadata, &calls)
	rsp, err := svc.UpsertFields(context.Background(), factorResultRequest("factor", factorRowsRecord("value")))
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(rsp.GetKeys()) != 1 {
		t.Fatalf("response=%v err=%v", rsp, err)
	}
	if len(calls) != 1 || calls[0].spaceID != "space" || calls[0].datasetID != "result" || len(calls[0].rows) != 1 {
		t.Fatalf("calls=%+v", calls)
	}
}

// 通用校验拦住未登记的列与停用的数据集；行的数据集与请求不一致、请求缺少元数据快照同样在写入之前拒绝。
func TestUpsertFieldsRejectsBeforeWriting(t *testing.T) {
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
			var calls []factorRowsCall
			svc := newFactorResultService(t, tc.snapshot, &calls)
			rsp, err := svc.UpsertFields(context.Background(), factorResultRequest("factor", tc.row))
			if err != nil || rsp.GetRetInfo().GetCode() != tc.code {
				t.Fatalf("response=%v err=%v", rsp, err)
			}
			if len(calls) != 0 {
				t.Fatal("rejected write reached the store")
			}
		})
	}
}

// __params 是一列声明过的 json 字段：按 json_value 原样写入存储层，Storage 不解析里面的内容；没注册这一列的数据集照旧拒绝。
func TestUpsertFieldsForwardsTheParamsJSONColumn(t *testing.T) {
	const params = `{"boll":{"window":20},"ema__20_50000":{"span":20,"warmup":50000}}`
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

	var calls []factorRowsCall
	rsp, err := newFactorResultService(t, snapshot(true), &calls).UpsertFields(context.Background(), factorResultRequest("factor", row()))
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("registered __params column: response=%v err=%v", rsp, err)
	}
	var got string
	for _, field := range calls[0].rows[0].GetFields() {
		if field.GetFieldId() == "__params" {
			got = field.GetValue().GetJsonValue()
		}
	}
	if got != params {
		t.Fatalf("forwarded __params = %q, want %q", got, params)
	}

	calls = nil
	rsp, err = newFactorResultService(t, snapshot(false), &calls).UpsertFields(context.Background(), factorResultRequest("factor", row()))
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM || len(calls) != 0 {
		t.Fatalf("unregistered __params column: response=%v err=%v calls=%d", rsp, err, len(calls))
	}
}
