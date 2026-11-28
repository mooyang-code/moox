package primarystore

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	metacache "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/cache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type pagedColumnSource struct {
	dataset *pb.Dataset
	columns []*pb.DatasetColumn
}

func (s *pagedColumnSource) ListDatasets(context.Context, metadata.DatasetQuery) ([]*pb.Dataset, *pb.PageResult, error) {
	return []*pb.Dataset{s.dataset}, &pb.PageResult{Page: 1, Size: 1000, Total: 1}, nil
}

func (s *pagedColumnSource) ListDatasetColumns(_ context.Context, _, _ string, page *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	pageNo, size := uint32(1), uint32(1000)
	if page != nil {
		pageNo, size = page.GetPage(), page.GetSize()
	}
	start := int((pageNo - 1) * size)
	if start > len(s.columns) {
		start = len(s.columns)
	}
	end := start + int(size)
	if end > len(s.columns) {
		end = len(s.columns)
	}
	return s.columns[start:end], &pb.PageResult{Page: pageNo, Size: size, Total: uint32(len(s.columns)), HasMore: end < len(s.columns)}, nil
}

func TestSchemaValidationReadsAllDatasetColumnPages(t *testing.T) {
	dataset := &pb.Dataset{SpaceId: "s", DatasetId: "d", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"}
	columns := make([]*pb.DatasetColumn, 1001)
	for i := range columns {
		columns[i] = &pb.DatasetColumn{
			SpaceId: "s", DatasetId: "d",
			ColumnName: fmt.Sprintf("field_%04d", i), ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, Status: "active",
		}
	}
	cache, err := metacache.NewDatasetCache(context.Background(), &pagedColumnSource{dataset: dataset, columns: columns})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	snap := cache.Snapshot()
	err = validateRowSchema(snap, dataset, &pb.Row{
		Key: &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}},
		Fields: []*pb.FieldValue{{
			FieldId: "field_1000",
			Value:   &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "ok"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSchemaValidationAcceptsExplicitNullForRegisteredDoubleColumn(t *testing.T) {
	dataset := &pb.Dataset{SpaceId: "s", DatasetId: "d", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"}
	cache := newTestDatasetCache(t, []*pb.Dataset{dataset}, []*pb.DatasetColumn{{
		SpaceId: "s", DatasetId: "d", ColumnName: "value",
		ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active",
	}})
	if err := validateRowSchema(cache.Snapshot(), dataset, validatorRow("value", pb.NullValue_NULL_VALUE_NULL)); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaValidationRejectsExplicitNullForUnknownColumn(t *testing.T) {
	dataset := &pb.Dataset{SpaceId: "s", DatasetId: "d", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"}
	cache := newTestDatasetCache(t, []*pb.Dataset{dataset}, nil)
	if err := validateRowSchema(cache.Snapshot(), dataset, validatorRow("unknown", pb.NullValue_NULL_VALUE_NULL)); err == nil {
		t.Fatal("unknown null field was accepted")
	}
}

func TestSchemaValidationRejectsUnspecifiedNull(t *testing.T) {
	dataset := &pb.Dataset{SpaceId: "s", DatasetId: "d", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"}
	cache := newTestDatasetCache(t, []*pb.Dataset{dataset}, []*pb.DatasetColumn{{
		SpaceId: "s", DatasetId: "d", ColumnName: "value",
		ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active",
	}})
	if err := validateRowSchema(cache.Snapshot(), dataset, validatorRow("value", pb.NullValue_NULL_VALUE_UNSPECIFIED)); err == nil {
		t.Fatal("unspecified null marker was accepted")
	}
}

func validatorRow(fieldID string, nullValue pb.NullValue) *pb.Row {
	return &pb.Row{
		Key: &pb.RowKey{
			SpaceId: "s", DatasetId: "d",
			Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}},
		},
		Fields: []*pb.FieldValue{{
			FieldId: fieldID,
			Value: &pb.TypedValue{Value: &pb.TypedValue_NullValue{
				NullValue: nullValue,
			}},
		}},
	}
}

// onceSource 只在首轮加载返回数据，之后若被访问直接 panic，证明请求热路径只读内存快照。
type onceSource struct {
	loaded   bool
	datasets []*pb.Dataset
	columns  []*pb.DatasetColumn
}

func (s *onceSource) ListDatasets(context.Context, metadata.DatasetQuery) ([]*pb.Dataset, *pb.PageResult, error) {
	if s.loaded {
		panic("persistent metadata store accessed")
	}
	return s.datasets, &pb.PageResult{Total: uint32(len(s.datasets))}, nil
}

func (s *onceSource) ListDatasetColumns(context.Context, string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	if s.loaded {
		panic("persistent metadata store accessed")
	}
	s.loaded = true
	return s.columns, &pb.PageResult{Total: uint32(len(s.columns))}, nil
}

func TestServiceUsesDatasetCacheSnapshotWithoutStoreAccess(t *testing.T) {
	var writes, reads int
	store := newHookStore(t)
	store.upsert = func(ctx context.Context, spaceID, datasetID string, rows []*pb.Row) error {
		writes++
		return store.Store.UpsertFields(ctx, spaceID, datasetID, rows)
	}
	store.readRows = func(ctx context.Context, keys []*pb.RowKey, columns, attributeKeys []string) ([]*pb.Row, error) {
		reads++
		return store.Store.ReadRows(ctx, keys, columns, attributeKeys)
	}
	src := &onceSource{
		datasets: []*pb.Dataset{{SpaceId: "space", DatasetId: "dataset", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"}},
		columns:  []*pb.DatasetColumn{{SpaceId: "space", DatasetId: "dataset", ColumnName: "value", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, Status: "active"}},
	}
	cache, err := metacache.NewDatasetCache(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	svc, err := New(Options{Store: store, Datasets: cache})
	if err != nil {
		t.Fatal(err)
	}
	key := &pb.RowKey{SpaceId: "space", DatasetId: "dataset", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "record", Version: "1"}}}
	write, err := svc.UpsertFields(context.Background(), fillDataset(&pb.PrimaryUpsertFieldsReq{
		Rows: []*pb.Row{{Key: key, Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "ok"}}}}}},
	}))
	if err != nil || write.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || writes != 1 {
		t.Fatalf("write=%v err=%v writes=%d", write, err, writes)
	}
	read, err := svc.ReadRows(context.Background(), &pb.PrimaryReadRowsReq{Keys: []*pb.RowKey{key}, ColumnNames: []string{"value"}})
	if err != nil || read.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || reads != 1 {
		t.Fatalf("read=%v err=%v reads=%d", read, err, reads)
	}
}

func barsRow(freq string) *pb.Row {
	return &pb.Row{
		Key: &pb.RowKey{SpaceId: "crypto", DatasetId: "bars", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
			SubjectId: "BTC-USDT", Freq: freq, DataTime: "2026-10-08T10:00:00Z",
		}}},
		Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}},
	}
}

func TestSchemaValidationRejectsRowFreqOtherThanTheDatasetFreq(t *testing.T) {
	dataset := &pb.Dataset{SpaceId: "crypto", DatasetId: "bars", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1h", Status: "active"}
	cache := newTestDatasetCache(t, []*pb.Dataset{dataset}, []*pb.DatasetColumn{{
		SpaceId: "crypto", DatasetId: "bars", ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active",
	}})
	snap := cache.Snapshot()
	if err := validateRowSchema(snap, dataset, barsRow("1h")); err != nil {
		t.Fatalf("canonical Dataset freq rejected: %v", err)
	}
	for _, freq := range []string{"1H", "1m", ""} {
		if err := validateRowSchema(snap, dataset, barsRow(freq)); err == nil || !strings.Contains(err.Error(), "does not match dataset") {
			t.Fatalf("row freq %q got err=%v for a 1h Dataset", freq, err)
		}
	}
}

// 写入只认列名；停用的列不再接受写入。
func TestSchemaValidationRejectsDisabledColumn(t *testing.T) {
	dataset := &pb.Dataset{SpaceId: "s", DatasetId: "d", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"}
	cache := newTestDatasetCache(t, []*pb.Dataset{dataset}, []*pb.DatasetColumn{{
		SpaceId: "s", DatasetId: "d", ColumnName: "value", DisplayName: "值",
		ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "disabled",
	}})
	if err := validateRowSchema(cache.Snapshot(), dataset, validatorRow("value", pb.NullValue_NULL_VALUE_NULL)); err == nil {
		t.Fatal("停用的列被接受写入")
	}
}
