//go:build cgo

package view

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/client"
)

type recoveryFieldReader struct{ primaryPresent bool }

func (r recoveryFieldReader) ReadRows(_ context.Context, req *pb.PrimaryReadRowsReq, _ ...client.Option) (*pb.PrimaryReadRowsRsp, error) {
	rows := make([]*pb.Row, 0, len(req.GetKeys()))
	for _, key := range req.GetKeys() {
		row := &pb.Row{Key: key}
		if key.GetDatasetId() == "primary" && r.primaryPresent {
			row.Fields = append(row.Fields, &pb.FieldValue{FieldId: "base", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 10}}})
			row.Fields = append(row.Fields, &pb.FieldValue{FieldId: "factor", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1.5}}})
		}
		rows = append(rows, row)
	}
	return &pb.PrimaryReadRowsRsp{RetInfo: successRetInfo(), Rows: rows}, nil
}

// prepareActiveIndex 在进程内准备空索引并把它转为数据集的活动索引，与建索引完成时相同。
func prepareActiveIndex(t *testing.T, svc *Service, spec *viewSpec, schema viewindex.ViewIndexSchema) *viewRuntime {
	t.Helper()
	runtime := testRuntime(svc, spec)
	if _, err := svc.prepareIndex(context.Background(), runtime, viewPhaseBuilding, func(*viewRuntime) (viewindex.ViewIndexSchema, error) { return schema, nil }); err != nil {
		t.Fatalf("prepare %s: %v", runtime.indexID, err)
	}
	if err := svc.activateBuiltIndex(runtime, spec, schema); err != nil {
		t.Fatalf("activate %s: %v", runtime.indexID, err)
	}
	return runtime
}

// writeTestRows 按 schema 的契约实时写入索引。
func writeTestRows(t *testing.T, svc *Service, runtime *viewRuntime, schema viewindex.ViewIndexSchema, rows []viewindex.RowWrite) {
	t.Helper()
	if err := svc.writeIndex(context.Background(), runtime, viewindex.ViewIndexWriteBatch{RowWrites: rows, ViewRevision: schema.ViewVersion, ViewSchemaHash: schema.SchemaHash, WriteMode: viewindex.LiveWrite}); err != nil {
		t.Fatalf("write %s: %v", runtime.indexID, err)
	}
}

func TestViewIndexAndDataViewExplicitKeyFlow(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	columns := []*viewindex.Column{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}
	schema := viewindex.ViewIndexSchema{SpaceID: "quant", DatasetID: "prices", ViewVersion: 1, Engine: "duckdb", SchemaHash: "schema-1", Columns: columns}
	runtime := prepareActiveIndex(t, svc, timeSeriesSpec("quant", "prices", "1m", "close"), schema)
	key := &pb.RowKey{SpaceId: "quant", DatasetId: "prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Frequency: "1m", DataTime: "2026-07-20T00:00:00Z", SeriesTag: "venue:okx"}}}
	value := &pb.FieldValue{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}}}
	writeTestRows(t, svc, runtime, schema, []viewindex.RowWrite{{Key: viewindex.RowKey{Key: key}, Fields: []*pb.FieldValue{value}}})
	tag := "venue:okx"
	rsp, err := svc.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{
		SpaceId: "quant", DatasetId: "prices",
		Selectors:   []*pb.TimeSeriesSelector{{SubjectId: "BTC-USDT", Frequency: "1m", SeriesTag: &tag}},
		ColumnNames: []string{"close"},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(rsp.GetRows()) != 1 || len(rsp.GetRows()[0].GetFields()) != 1 {
		t.Fatalf("query: rsp=%v err=%v", rsp, err)
	}
	if rsp.GetRows()[0].GetKey().GetTimeSeries().GetSeriesTag() != tag {
		t.Fatalf("query lost exact series tag: rsp=%v", rsp)
	}
}

func TestViewServiceDefersIncompatibleDuckDBSchemaToMetadataRecovery(t *testing.T) {
	root := filepath.Join(t.TempDir(), "views")
	duckdbRoot := filepath.Join(root, "duckdb")
	if err := os.MkdirAll(duckdbRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("duckdb", filepath.Join(duckdbRoot, "legacy.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE view_rows (
			subject_id VARCHAR NOT NULL,
			freq VARCHAR NOT NULL,
			data_time TIMESTAMP_NS NOT NULL,
			dimensions_json VARCHAR NOT NULL,
			PRIMARY KEY (subject_id, freq, data_time, dimensions_json)
		)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err = New(root); err != nil {
		t.Fatalf("New() should defer existing index validation until metadata recovery: %v", err)
	}
	if _, err := os.Stat(filepath.Join(duckdbRoot, "legacy.duckdb")); err != nil {
		t.Fatalf("existing index should remain for metadata recovery, stat err=%v", err)
	}
}

func TestDataViewSupportsTimeRangeAndTextOnlyQueries(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	columns := []*viewindex.Column{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}, {ColumnName: "title", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING}}
	prepare := func(spec *viewSpec) (*viewRuntime, viewindex.ViewIndexSchema) {
		t.Helper()
		schema := viewindex.ViewIndexSchema{SpaceID: "space", DatasetID: spec.DatasetID, ViewVersion: 1, Engine: spec.engineName(), SchemaHash: "hash", Columns: columns}
		return prepareActiveIndex(t, svc, spec, schema), schema
	}
	timesID, timesSchema := prepare(timeSeriesSpec("space", "prices", "1d", "close", "title"))
	timeRows := []viewindex.RowWrite{}
	for _, at := range []string{"2026-07-19T00:00:00Z", "2026-07-20T00:00:00Z"} {
		timeRows = append(timeRows, viewindex.RowWrite{
			Key:    viewindex.RowKey{Key: &pb.RowKey{SpaceId: "space", DatasetId: "prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Frequency: "1d", DataTime: at}}}},
			Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}}}},
		})
	}
	writeTestRows(t, svc, timesID, timesSchema, timeRows)
	timeRsp, err := svc.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{
		SpaceId: "space", DatasetId: "prices",
		TimeRange: &pb.TimeRange{StartTime: "2026-07-20T00:00:00Z", EndTime: "2026-07-20T23:59:59Z"},
	})
	if err != nil || timeRsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(timeRsp.GetRows()) != 1 {
		t.Fatalf("time rsp=%v err=%v", timeRsp, err)
	}

	recordsID, recordsSchema := prepare(recordSpec("space", "records", "close", "title"))
	recordKey := &pb.RowKey{SpaceId: "space", DatasetId: "records", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}
	writeTestRows(t, svc, recordsID, recordsSchema, []viewindex.RowWrite{{Key: viewindex.RowKey{Key: recordKey}, Fields: []*pb.FieldValue{{FieldId: "title", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "market research"}}}}}})
	recordRsp, err := svc.SearchRecordRows(ctx, &pb.SearchRecordRowsReq{SpaceId: "space", DatasetId: "records", TextQuery: "research"})
	if err != nil || recordRsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(recordRsp.GetRows()) != 1 {
		t.Fatalf("record rsp=%v err=%v", recordRsp, err)
	}
}

func successRetInfo() *pb.RetInfo {
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}
}
