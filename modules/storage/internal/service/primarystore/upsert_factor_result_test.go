package primarystore

import (
	"context"
	"testing"

	metacache "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/cache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

// factorRowsCall 是存储层收到的一次写入。
type factorRowsCall struct {
	spaceID, datasetID string
	rows               []*pb.Row
}

func factorRowsDataset() *pb.Dataset {
	return &pb.Dataset{
		SpaceId: "space", DatasetId: "result", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active",
		Attributes: map[string]string{"owner_module": "factor", "dataset_role": "factor_result"},
	}
}

func factorRowsColumns() []*pb.DatasetColumn {
	return []*pb.DatasetColumn{{
		SpaceId: "space", DatasetId: "result", ColumnName: "value", Status: "active", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
	}}
}

func factorRowsRecord(field string) *pb.Row {
	return &pb.Row{
		Key:    &pb.RowKey{SpaceId: "space", DatasetId: "result", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "row-1", Version: "v1"}}},
		Fields: []*pb.FieldValue{{FieldId: field, Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "value"}}}},
	}
}

// newFactorResultService 用给定的数据集缓存构造主库；calls 记录到达存储层的写入。
func newFactorResultService(t *testing.T, cache *metacache.DatasetCache, calls *[]factorRowsCall) *Service {
	t.Helper()
	store := newHookStore(t)
	store.upsert = func(ctx context.Context, spaceID, datasetID string, rows []*pb.Row) error {
		*calls = append(*calls, factorRowsCall{spaceID: spaceID, datasetID: datasetID, rows: rows})
		return store.Store.UpsertFields(ctx, spaceID, datasetID, rows)
	}
	svc, err := New(Options{Store: store, Datasets: cache})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func factorResultRequest(appID string, rows ...*pb.Row) *pb.PrimaryUpsertFieldsReq {
	return &pb.PrimaryUpsertFieldsReq{SpaceId: "space", DatasetId: "result", Rows: rows}
}

// 请求的 space_id、dataset_id 原样交给存储层。
func TestUpsertFieldsPassesDatasetToTheStore(t *testing.T) {
	var calls []factorRowsCall
	cache := newTestDatasetCache(t, []*pb.Dataset{factorRowsDataset()}, factorRowsColumns())
	svc := newFactorResultService(t, cache, &calls)
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
	activeCache := newTestDatasetCache(t, []*pb.Dataset{factorRowsDataset()}, factorRowsColumns())
	disabledDataset := proto.Clone(factorRowsDataset()).(*pb.Dataset)
	disabledDataset.Status = "disabled"
	disabledCache := newTestDatasetCache(t, []*pb.Dataset{disabledDataset}, factorRowsColumns())
	otherDataset := factorRowsRecord("value")
	otherDataset.Key.DatasetId = "other"
	cases := []struct {
		name  string
		cache *metacache.DatasetCache
		row   *pb.Row
		code  pb.ErrorCode
	}{
		{"未登记的列", activeCache, factorRowsRecord("unregistered"), pb.ErrorCode_INVALID_PARAM},
		{"停用的数据集", disabledCache, factorRowsRecord("value"), pb.ErrorCode_INVALID_PARAM},
		{"行属于别的数据集", activeCache, otherDataset, pb.ErrorCode_INVALID_PARAM},
		{"缺少元数据快照", nil, factorRowsRecord("value"), pb.ErrorCode_INNER_ERR},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []factorRowsCall
			svc := newFactorResultService(t, tc.cache, &calls)
			rsp, err := svc.UpsertFields(context.Background(), factorResultRequest("factor", tc.row))
			if err != nil || rsp.GetRetInfo().GetCode() != tc.code {
				t.Fatalf("response=%v err=%v want=%s", rsp, err, tc.code)
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
	buildCache := func(withParams bool) *metacache.DatasetCache {
		ds := proto.Clone(factorRowsDataset()).(*pb.Dataset)
		ds.DataKind = pb.DataKind_DATA_KIND_TIME_SERIES
		ds.Frequency = "5m"
		columns := []*pb.DatasetColumn{{SpaceId: "space", DatasetId: "result", ColumnName: "value", Status: "active", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}
		if withParams {
			columns = append(columns, &pb.DatasetColumn{SpaceId: "space", DatasetId: "result", ColumnName: "__params", Status: "active", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_JSON})
		}
		return newTestDatasetCache(t, []*pb.Dataset{ds}, columns)
	}
	row := func() *pb.Row {
		return &pb.Row{
			Key: &pb.RowKey{SpaceId: "space", DatasetId: "result", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Frequency: "5m", DataTime: "2026-10-25T16:35:00Z"}}},
			Fields: []*pb.FieldValue{
				{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1.5}}},
				{FieldId: "__params", Value: &pb.TypedValue{Value: &pb.TypedValue_JsonValue{JsonValue: params}}},
			},
		}
	}

	var calls []factorRowsCall
	rsp, err := newFactorResultService(t, buildCache(true), &calls).UpsertFields(context.Background(), factorResultRequest("factor", row()))
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
	rsp, err = newFactorResultService(t, buildCache(false), &calls).UpsertFields(context.Background(), factorResultRequest("factor", row()))
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM || len(calls) != 0 {
		t.Fatalf("unregistered __params column: response=%v err=%v calls=%d", rsp, err, len(calls))
	}
}
