//go:build cgo

package view

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
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

func TestViewIndexAndDataViewExplicitKeyFlow(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	columns := []*pb.ViewColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}
	if rsp, err := svc.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{AuthInfo: auth, IndexId: "prices-view", Schema: &pb.ViewIndexSchema{SpaceId: "quant", ViewId: "prices-view", DatasetId: "prices", ViewVersion: 1, Engine: "duckdb", ViewSchemaHash: "schema-1", Columns: columns}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("prepare: rsp=%v err=%v", rsp, err)
	}
	if err := svc.AttachActiveView(context.Background(), &pb.View{
		SpaceId: "quant", ViewId: "prices-view", DatasetId: "prices",
		ActiveIndexId: "prices-view", ViewRevision: 1, ActiveViewSchemaHash: "schema-1",
		Engine: "duckdb", ActiveColumns: columns, Status: "active",
	}); err != nil {
		t.Fatalf("attach active view: %v", err)
	}
	key := &pb.RowKey{SpaceId: "quant", DatasetId: "prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-20T00:00:00Z", SeriesTag: "venue:okx"}}}
	value := &pb.FieldValue{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}}}
	if rsp, err := svc.ApplyViewIndex(ctx, &pb.ApplyViewIndexReq{AuthInfo: auth, IndexId: "prices-view", Batch: &pb.ViewIndexWriteBatch{ViewRevision: 1, ViewSchemaHash: "schema-1", WriteMode: "LIVE_WRITE", RowWrites: []*pb.ViewIndexRowWrite{{Key: &pb.ViewIndexRowKey{RowKey: key}, Fields: []*pb.FieldValue{value}}}}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("apply: rsp=%v err=%v", rsp, err)
	}
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
	rsp, err := svc.PrepareViewIndex(context.Background(), &pb.PrepareViewIndexReq{
		IndexId: "idx",
		Schema:  &pb.ViewIndexSchema{SpaceId: "s", ViewId: "v", ViewVersion: 1, Engine: "bleve", ViewSchemaHash: "hash"},
	})
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
	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	prepare := func(id string) {
		t.Helper()
		rsp, err := svc.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{
			AuthInfo: auth, IndexId: id,
			Schema: &pb.ViewIndexSchema{SpaceId: "space", ViewId: id, ViewVersion: 1, Engine: map[string]string{"times": "duckdb", "records": "bleve"}[id], ViewSchemaHash: "hash", Columns: []*pb.ViewColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}, {ColumnName: "title", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING}}},
		})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("prepare rsp=%v err=%v", rsp, err)
		}
		if err := svc.AttachActiveView(context.Background(), &pb.View{SpaceId: "space", ViewId: id, DatasetId: map[string]string{"times": "prices", "records": "records"}[id], ActiveIndexId: id, ViewRevision: 1, ActiveViewSchemaHash: "hash", Engine: map[string]string{"times": "duckdb", "records": "bleve"}[id], ActiveColumns: []*pb.ViewColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}, {ColumnName: "title", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING}}, Status: "active"}); err != nil {
			t.Fatalf("attach %s: %v", id, err)
		}
	}
	prepare("times")
	timeRows := []*pb.ViewIndexRowWrite{}
	for _, at := range []string{"2026-07-19T00:00:00Z", "2026-07-20T00:00:00Z"} {
		timeRows = append(timeRows, &pb.ViewIndexRowWrite{
			Key:    &pb.ViewIndexRowKey{RowKey: &pb.RowKey{SpaceId: "space", DatasetId: "prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1d", DataTime: at}}}},
			Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}}}},
		})
	}
	if rsp, err := svc.ApplyViewIndex(ctx, &pb.ApplyViewIndexReq{AuthInfo: auth, IndexId: "times", Batch: &pb.ViewIndexWriteBatch{ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: "LIVE_WRITE", RowWrites: timeRows}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("apply time rows rsp=%v err=%v", rsp, err)
	}
	timeRsp, err := svc.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{
		AuthInfo: auth, SpaceId: "space", ViewId: "times",
		TimeRange: &pb.TimeRange{StartTime: "2026-07-20T00:00:00Z", EndTime: "2026-07-20T23:59:59Z"},
	})
	if err != nil || timeRsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(timeRsp.GetRows()) != 1 {
		t.Fatalf("time rsp=%v err=%v", timeRsp, err)
	}

	prepare("records")
	recordKey := &pb.RowKey{SpaceId: "space", DatasetId: "records", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}
	if rsp, err := svc.ApplyViewIndex(ctx, &pb.ApplyViewIndexReq{AuthInfo: auth, IndexId: "records", Batch: &pb.ViewIndexWriteBatch{
		ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: "LIVE_WRITE",
		RowWrites: []*pb.ViewIndexRowWrite{{Key: &pb.ViewIndexRowKey{RowKey: recordKey}, Fields: []*pb.FieldValue{{FieldId: "title", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "market research"}}}}}},
	}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("apply record rsp=%v err=%v", rsp, err)
	}
	recordRsp, err := svc.SearchRecordRows(ctx, &pb.SearchRecordRowsReq{AuthInfo: auth, SpaceId: "space", ViewId: "records", TextQuery: "research"})
	if err != nil || recordRsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(recordRsp.GetRows()) != 1 {
		t.Fatalf("record rsp=%v err=%v", recordRsp, err)
	}
}

func TestViewDatasetMappingIncludesSpace(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	for _, space := range []string{"space-a", "space-b"} {
		rsp, err := svc.PrepareViewIndex(context.Background(), &pb.PrepareViewIndexReq{
			AuthInfo: auth,
			IndexId:  space + "-view",
			Schema: &pb.ViewIndexSchema{
				SpaceId:        space,
				ViewId:         "view",
				DatasetId:      "shared",
				ViewVersion:    1,
				Engine:         "bleve",
				ViewSchemaHash: "hash",
				Columns:        []*pb.ViewColumn{{OriginId: "value", ColumnName: "value"}},
			},
		})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("space=%s rsp=%v err=%v", space, rsp, err)
		}
	}
	if len(svc.byData[datasetRef{spaceID: "space-a", datasetID: "shared"}]) != 1 ||
		len(svc.byData[datasetRef{spaceID: "space-b", datasetID: "shared"}]) != 1 {
		t.Fatalf("byData=%v", svc.byData)
	}
}

func TestForeignDatasetEventDoesNotMapToOwnedViewGrain(t *testing.T) {
	writes := eventWrites(viewindex.ViewIndexSchema{
		PrimaryDatasetID: "primary",
		Columns: []*pb.ViewColumn{{
			OriginId: "factor", ColumnName: "factor",
		}},
	}, "secondary", []*pb.RowFieldUpsert{{
		Key: &pb.RowKey{SpaceId: "space", DatasetId: "secondary", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}},
		Fields: []*pb.FieldValue{{
			FieldId: "factor", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1.5}},
		}},
	}})
	if len(writes) != 0 {
		t.Fatalf("foreign dataset event must not write owned view grain: writes=%v", writes)
	}
}

func TestOwnedDatasetEventRecoversMissingColumn(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	if rsp, err := svc.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{AuthInfo: auth, IndexId: "owned", Schema: &pb.ViewIndexSchema{
		SpaceId: "space", ViewId: "owned", DatasetId: "primary", ViewVersion: 1, Engine: "bleve", ViewSchemaHash: "hash",
		Columns: []*pb.ViewColumn{{OriginId: "base", ColumnName: "base"}, {OriginId: "factor", ColumnName: "factor"}},
	}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("prepare rsp=%v err=%v", rsp, err)
	}
	svc.views[viewRef{spaceID: "space", viewID: "owned"}].mu.Lock()
	svc.views[viewRef{spaceID: "space", viewID: "owned"}].active = "owned"

	svc.views[viewRef{spaceID: "space", viewID: "owned"}].mu.Unlock()
	svc.SetPrimaryAuth(&pb.AuthInfo{AppId: "storage-view"})
	svc.SetPrimaryReader(recoveryFieldReader{primaryPresent: true})
	event := &pb.RowFieldUpsert{
		Key:    &pb.RowKey{SpaceId: "space", DatasetId: "primary", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}},
		Fields: []*pb.FieldValue{{FieldId: "base", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 2}}}},
	}
	schema := svc.schemas["owned"]
	initial := eventWrites(schema, "primary", []*pb.RowFieldUpsert{event})
	recovered, err := svc.recoverMissingRows(ctx, schema, []*pb.RowFieldUpsert{event}, initial)
	if err != nil || len(recovered) != 1 || len(recovered[0].Fields) < 1 {
		t.Fatalf("recover failed recovered=%v err=%v", recovered, err)
	}
	if err := svc.applyDatasetEvent(ctx, "space", "primary", []*pb.RowFieldUpsert{event}); err != nil {
		t.Fatal(err)
	}
	key := &pb.RowKey{SpaceId: "space", DatasetId: "primary", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}
	rows, err := svc.query(ctx, "owned", []*pb.RowKey{key}, nil)
	if err != nil || len(rows) != 1 {
		t.Fatalf("recovered rows=%v err=%v", rows, err)
	}
}

func TestCompleteEventCreatesMissingViewRowWithoutRecovery(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	if rsp, err := svc.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{AuthInfo: auth, IndexId: "single", Schema: &pb.ViewIndexSchema{
		SpaceId: "space", ViewId: "single", DatasetId: "market", ViewVersion: 1, Engine: "bleve", ViewSchemaHash: "hash",
		Columns: []*pb.ViewColumn{{OriginId: "close", ColumnName: "close"}},
	}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("prepare rsp=%v err=%v", rsp, err)
	}
	svc.views[viewRef{spaceID: "space", viewID: "single"}].mu.Lock()
	svc.views[viewRef{spaceID: "space", viewID: "single"}].active = "single"

	svc.views[viewRef{spaceID: "space", viewID: "single"}].mu.Unlock()
	event := &pb.RowFieldUpsert{
		Key:    &pb.RowKey{SpaceId: "space", DatasetId: "market", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}},
		Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 3}}}},
	}
	if err := svc.applyDatasetEvent(ctx, "space", "market", []*pb.RowFieldUpsert{event}); err != nil {
		t.Fatal(err)
	}
	rows, err := svc.query(ctx, "single", []*pb.RowKey{event.GetKey()}, nil)
	if err != nil || len(rows) != 1 || len(rows[0].GetFields()) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
}

func TestDatasetEventAcksUnmanagedBeforeInitialMaintenance(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	row := &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: "space", DatasetId: "market", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}}
	if err := svc.applyDatasetEvent(context.Background(), "space", "market", []*pb.RowFieldUpsert{row}); err != nil {
		t.Fatalf("unmanaged Dataset row should ACK during initial maintenance: %v", err)
	}
}

func TestDatasetEventStaysPendingWhenNewManagedViewIsNotAttached(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	metadata := &maintenanceMetadata{view: &pb.View{
		SpaceId: "space", ViewId: "prices", DatasetId: "market", Status: "active",
	}}
	svc.setMetadataClient(metadata)
	svc.setMaintenanceReady(true)
	row := &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: "space", DatasetId: "market", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}}
	if err := svc.applyDatasetEvent(context.Background(), "space", "market", []*pb.RowFieldUpsert{row}); err == nil {
		t.Fatal("managed Dataset row was ACKable before its View mapping was attached")
	}
}

func TestInitialBuildAcksRowsAfterWritingBuildingIndex(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	columns := []*pb.ViewColumn{{OriginId: "close", ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}
	if rsp, err := svc.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{AuthInfo: auth, IndexId: "priming-view", Schema: &pb.ViewIndexSchema{
		SpaceId: "space", ViewId: "priming", DatasetId: "market", ViewVersion: 1, Engine: "bleve", ViewSchemaHash: "hash", Columns: columns,
	}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("prepare priming view: rsp=%v err=%v", rsp, err)
	}
	row := &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: "space", DatasetId: "market", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 3}}}}}
	if err := svc.applyDatasetEvent(ctx, "space", "market", []*pb.RowFieldUpsert{row}); err != nil {
		t.Fatalf("priming row should ACK after replacement write: %v", err)
	}
	rows, err := svc.query(ctx, "priming-view", []*pb.RowKey{row.GetKey()}, nil)
	if err != nil || len(rows) != 1 {
		t.Fatalf("priming row was not written: rows=%v err=%v", rows, err)
	}
}

func TestExplicitEmptyViewRoutesDatasetEventsWithoutBlocking(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	if rsp, err := svc.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{AuthInfo: auth, IndexId: "empty-view", Schema: &pb.ViewIndexSchema{
		SpaceId: "space", ViewId: "empty", DatasetId: "market", ViewVersion: 1, Engine: "bleve", ViewSchemaHash: "hash",
	}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("prepare empty view: rsp=%v err=%v", rsp, err)
	}
	view := &pb.View{SpaceId: "space", ViewId: "empty", DatasetId: "market", ActiveIndexId: "empty-view", ViewRevision: 1, ActiveViewSchemaHash: "hash", Engine: "bleve", Status: "active", Attributes: map[string]string{viewColumnsExplicitAttr: "true"}}
	if err := svc.AttachActiveView(context.Background(), view); err != nil {
		t.Fatal(err)
	}
	row := &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: "space", DatasetId: "market", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}}
	if err := svc.applyDatasetEvent(ctx, "space", "market", []*pb.RowFieldUpsert{row}); err != nil {
		t.Fatalf("explicit empty view must ACK its Dataset event: %v", err)
	}
}

func successRetInfo() *pb.RetInfo {
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}
}
