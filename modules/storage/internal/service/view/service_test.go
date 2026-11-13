//go:build cgo

package view

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	"github.com/mooyang-code/moox/modules/storage/internal/serviceauth"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/client"
)

type recoveryFieldReader struct{ primaryPresent bool }

func (r recoveryFieldReader) ReadRows(_ context.Context, req *pb.PrimaryReadRowsReq, _ ...client.Option) (*pb.PrimaryReadRowsRsp, error) {
	rows := make([]*pb.RowFieldValues, 0, len(req.GetKeys()))
	for _, key := range req.GetKeys() {
		row := &pb.RowFieldValues{Key: key}
		if key.GetDatasetId() == "primary" && r.primaryPresent {
			row.Fields = append(row.Fields, &pb.FieldValue{FieldId: "base", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 10}}})
			row.Fields = append(row.Fields, &pb.FieldValue{FieldId: "factor", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1.5}}})
		}
		rows = append(rows, row)
	}
	return &pb.PrimaryReadRowsRsp{RetInfo: successRetInfo(), Rows: rows}, nil
}

// prepareActiveIndex 在进程内准备空索引并把它转为 View 的活动索引，与建索引完成时相同。
func prepareActiveIndex(t *testing.T, svc *Service, view *pb.View, schema viewindex.ViewIndexSchema) string {
	t.Helper()
	indexID := viewindex.ViewIndexID(view.GetSpaceId(), view.GetViewId())
	if err := svc.prepareIndex(context.Background(), indexID, schema); err != nil {
		t.Fatalf("prepare %s: %v", indexID, err)
	}
	if err := svc.activateBuiltIndex(view, schema, indexID); err != nil {
		t.Fatalf("activate %s: %v", indexID, err)
	}
	return indexID
}

// writeTestRows 按 schema 的契约实时写入索引。
func writeTestRows(t *testing.T, svc *Service, indexID string, schema viewindex.ViewIndexSchema, rows []viewindex.RowWrite) {
	t.Helper()
	engine, err := svc.engineFor(indexID)
	if err == nil {
		err = svc.writeIndex(context.Background(), indexID, engine, viewindex.ViewIndexWriteBatch{RowWrites: rows, ViewRevision: schema.ViewVersion, ViewSchemaHash: schema.SchemaHash, WriteMode: viewindex.LiveWrite})
	}
	if err != nil {
		t.Fatalf("write %s: %v", indexID, err)
	}
}

func TestViewIndexAndDataViewExplicitKeyFlow(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	auth := &pb.AuthInfo{AppId: "caller", AppKey: serviceauth.Key("view-secret", "caller")}
	columns := []*pb.ViewColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}
	schema := viewindex.ViewIndexSchema{SpaceID: "quant", ViewID: "prices-view", PrimaryDatasetID: "prices", ViewVersion: 1, Engine: "duckdb", SchemaHash: "schema-1", Columns: columns}
	indexID := prepareActiveIndex(t, svc, &pb.View{SpaceId: "quant", ViewId: "prices-view", DatasetId: "prices", Engine: "duckdb", Status: "active"}, schema)
	key := &pb.RowKey{SpaceId: "quant", DatasetId: "prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-20T00:00:00Z", SeriesTag: "venue:okx"}}}
	value := &pb.FieldValue{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}}}
	writeTestRows(t, svc, indexID, schema, []viewindex.RowWrite{{Key: viewindex.RowKey{Key: key}, Fields: []*pb.FieldValue{value}}})
	tag := "venue:okx"
	rsp, err := svc.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{
		AuthInfo: auth, SpaceId: "quant", ViewId: "prices-view",
		Selectors:   []*pb.TimeSeriesSelector{{SpaceId: "quant", DatasetId: "prices", SubjectId: "BTC-USDT", Freq: "1m", SeriesTag: &tag}},
		ColumnNames: []string{"close"},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(rsp.GetRows()) != 1 || len(rsp.GetRows()[0].GetFields()) != 1 {
		t.Fatalf("query: rsp=%v err=%v", rsp, err)
	}
	if rsp.GetRows()[0].GetKey().GetSeriesTag() != tag {
		t.Fatalf("query lost exact series tag: rsp=%v", rsp)
	}
}

func TestViewServiceRequiresSecretAndAuth(t *testing.T) {
	if _, err := New(filepath.Join(t.TempDir(), "views"), ""); err == nil {
		t.Fatal("expected missing view auth secret to be rejected")
	}
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := svc.QueryTimeSeriesRows(context.Background(), &pb.QueryTimeSeriesRowsReq{SpaceId: "s", ViewId: "v"})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("rsp=%v err=%v", rsp, err)
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

	if _, err = New(root, "view-secret"); err != nil {
		t.Fatalf("New() should defer existing index validation until metadata recovery: %v", err)
	}
	if _, err := os.Stat(filepath.Join(duckdbRoot, "legacy.duckdb")); err != nil {
		t.Fatalf("existing index should remain for metadata recovery, stat err=%v", err)
	}
}

func TestDataViewSupportsTimeRangeAndTextOnlyQueries(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	auth := &pb.AuthInfo{AppId: "caller", AppKey: serviceauth.Key("view-secret", "caller")}
	columns := []*pb.ViewColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}, {ColumnName: "title", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING}}
	prepare := func(id string) (string, viewindex.ViewIndexSchema) {
		t.Helper()
		engine := map[string]string{"times": "duckdb", "records": "bleve"}[id]
		dataset := map[string]string{"times": "prices", "records": "records"}[id]
		schema := viewindex.ViewIndexSchema{SpaceID: "space", ViewID: id, PrimaryDatasetID: dataset, ViewVersion: 1, Engine: engine, SchemaHash: "hash", Columns: columns}
		return prepareActiveIndex(t, svc, &pb.View{SpaceId: "space", ViewId: id, DatasetId: dataset, Engine: engine, Status: "active"}, schema), schema
	}
	timesID, timesSchema := prepare("times")
	timeRows := []viewindex.RowWrite{}
	for _, at := range []string{"2026-07-19T00:00:00Z", "2026-07-20T00:00:00Z"} {
		timeRows = append(timeRows, viewindex.RowWrite{
			Key:    viewindex.RowKey{Key: &pb.RowKey{SpaceId: "space", DatasetId: "prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1d", DataTime: at}}}},
			Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}}}},
		})
	}
	writeTestRows(t, svc, timesID, timesSchema, timeRows)
	timeRsp, err := svc.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{
		AuthInfo: auth, SpaceId: "space", ViewId: "times",
		TimeRange: &pb.TimeRange{StartTime: "2026-07-20T00:00:00Z", EndTime: "2026-07-20T23:59:59Z"},
	})
	if err != nil || timeRsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(timeRsp.GetRows()) != 1 {
		t.Fatalf("time rsp=%v err=%v", timeRsp, err)
	}

	recordsID, recordsSchema := prepare("records")
	recordKey := &pb.RowKey{SpaceId: "space", DatasetId: "records", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}
	writeTestRows(t, svc, recordsID, recordsSchema, []viewindex.RowWrite{{Key: viewindex.RowKey{Key: recordKey}, Fields: []*pb.FieldValue{{FieldId: "title", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "market research"}}}}}})
	recordRsp, err := svc.SearchRecordRows(ctx, &pb.SearchRecordRowsReq{AuthInfo: auth, SpaceId: "space", ViewId: "records", TextQuery: "research"})
	if err != nil || recordRsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(recordRsp.GetRows()) != 1 {
		t.Fatalf("record rsp=%v err=%v", recordRsp, err)
	}
}

func successRetInfo() *pb.RetInfo {
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}
}
