//go:build cgo

package view

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/client"
)

func TestAttachActiveViewRefreshesContractWhenIndexChanges(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	old := &pb.View{SpaceId: "space", ViewId: "prices", PrimaryDatasetId: "prices", DatasetIds: []string{"prices"}, ActiveIndexId: "prices-a", ActiveViewRevision: 1, DesiredViewRevision: 1, ActiveViewSchemaHash: "a", Engine: "bleve", Status: "active"}
	if err := svc.AttachActiveView(old); err != nil {
		t.Fatal(err)
	}
	activeIDs, _ := json.Marshal([]string{"prices", "fundamentals"})
	updated := &pb.View{SpaceId: "space", ViewId: "prices", PrimaryDatasetId: "fundamentals", DatasetIds: []string{"prices", "fundamentals"}, ActiveIndexId: "prices-b", ActiveViewRevision: 2, DesiredViewRevision: 2, ActiveViewSchemaHash: "b", Engine: "bleve", Status: "active", Attributes: map[string]string{activeDatasetIDsAttr: string(activeIDs), activePrimaryDatasetAttr: "fundamentals"}}
	if err := svc.AttachActiveView(updated); err != nil {
		t.Fatal(err)
	}
	svc.mu.RLock()
	runtime := svc.views[viewRef{spaceID: "space", viewID: "prices"}]
	svc.mu.RUnlock()
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.active != "prices-b" || !runtime.activeDatasetSet || len(runtime.activeDatasetIDs) != 2 || runtime.activePrimaryDatasetID != "fundamentals" {
		t.Fatalf("active contract not refreshed: active=%q datasets=%v primary=%q set=%v", runtime.active, runtime.activeDatasetIDs, runtime.activePrimaryDatasetID, runtime.activeDatasetSet)
	}
}

type fakeFieldReader struct{}

func (fakeFieldReader) ReadFields(_ context.Context, req *pb.PrimaryReadFieldsReq, _ ...client.Option) (*pb.PrimaryReadFieldsRsp, error) {
	rows := make([]*pb.RowFieldValues, 0, len(req.GetKeys()))
	for _, key := range req.GetKeys() {
		rows = append(rows, &pb.RowFieldValues{
			Key: key,
			Fields: []*pb.FieldValue{{
				FieldId: "factor",
				Value:   &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1.5}},
			}},
		})
	}
	return &pb.PrimaryReadFieldsRsp{RetInfo: successRetInfo(), Rows: rows}, nil
}

type blockingFieldReader struct {
	started chan struct{}
	release chan struct{}
}

func (r *blockingFieldReader) ReadFields(ctx context.Context, req *pb.PrimaryReadFieldsReq, _ ...client.Option) (*pb.PrimaryReadFieldsRsp, error) {
	select {
	case <-r.started:
	default:
		close(r.started)
	}
	select {
	case <-r.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	rows := make([]*pb.RowFieldValues, 0, len(req.GetKeys()))
	for _, key := range req.GetKeys() {
		rows = append(rows, &pb.RowFieldValues{
			Key: key,
			Fields: []*pb.FieldValue{{
				FieldId: "extra",
				Value:   &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 7}},
			}},
		})
	}
	return &pb.PrimaryReadFieldsRsp{RetInfo: successRetInfo(), Rows: rows}, nil
}

type recoveryFieldReader struct{ primaryPresent bool }

func (r recoveryFieldReader) ReadFields(_ context.Context, req *pb.PrimaryReadFieldsReq, _ ...client.Option) (*pb.PrimaryReadFieldsRsp, error) {
	rows := make([]*pb.RowFieldValues, 0, len(req.GetKeys()))
	for _, key := range req.GetKeys() {
		row := &pb.RowFieldValues{Key: key}
		if key.GetDatasetId() == "primary" && r.primaryPresent {
			row.Fields = append(row.Fields, &pb.FieldValue{FieldId: "base", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 10}}})
		}
		if key.GetDatasetId() == "secondary" {
			row.Fields = append(row.Fields, &pb.FieldValue{FieldId: "factor", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1.5}}})
		}
		rows = append(rows, row)
	}
	return &pb.PrimaryReadFieldsRsp{RetInfo: successRetInfo(), Rows: rows}, nil
}

type sparseRecoveryFieldReader struct{ secondaryPresent bool }

func (r sparseRecoveryFieldReader) ReadFields(_ context.Context, req *pb.PrimaryReadFieldsReq, _ ...client.Option) (*pb.PrimaryReadFieldsRsp, error) {
	rows := make([]*pb.RowFieldValues, 0, len(req.GetKeys()))
	for _, key := range req.GetKeys() {
		row := &pb.RowFieldValues{Key: key}
		if key.GetDatasetId() == "primary" {
			row.Fields = append(row.Fields, &pb.FieldValue{FieldId: "base", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 10}}})
		}
		if key.GetDatasetId() == "secondary" && r.secondaryPresent {
			row.Fields = append(row.Fields, &pb.FieldValue{FieldId: "factor", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1.5}}})
		}
		rows = append(rows, row)
	}
	return &pb.PrimaryReadFieldsRsp{RetInfo: successRetInfo(), Rows: rows}, nil
}

func TestViewIndexAndDataViewExplicitKeyFlow(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	columns := []*pb.ViewColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}
	if rsp, err := svc.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{AuthInfo: auth, IndexId: "prices-view", Schema: &pb.ViewIndexSchema{SpaceId: "quant", ViewId: "prices-view", PrimaryDatasetId: "prices", ViewVersion: 1, Engine: "duckdb", ViewSchemaHash: "schema-1", Columns: columns}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("prepare: rsp=%v err=%v", rsp, err)
	}
	if err := svc.AttachActiveView(&pb.View{
		SpaceId: "quant", ViewId: "prices-view", PrimaryDatasetId: "prices",
		ActiveIndexId: "prices-view", ActiveViewRevision: 1, ActiveViewSchemaHash: "schema-1",
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

func TestDuckDBABSwitchKeepsActiveReadableUntilCollectorDeletesOldFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "views")
	svc, err := New(root, "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	columns := []*pb.ViewColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}
	schema := func(version uint64) *pb.ViewIndexSchema {
		return &pb.ViewIndexSchema{SpaceId: "quant", ViewId: "prices-view", PrimaryDatasetId: "prices", ViewVersion: version, Engine: "duckdb", ViewSchemaHash: fmt.Sprintf("schema-%d", version), Columns: columns, DatasetIds: []string{"prices"}}
	}
	prepare := func(indexID string, version uint64) {
		t.Helper()
		rsp, err := svc.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{AuthInfo: auth, IndexId: indexID, Schema: schema(version)})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("prepare %s: rsp=%v err=%v", indexID, rsp, err)
		}
	}
	key := func(at string, close float64) *pb.ViewIndexRowWrite {
		return &pb.ViewIndexRowWrite{
			Key:    &pb.ViewIndexRowKey{RowKey: &pb.RowKey{SpaceId: "quant", DatasetId: "prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: at}}}},
			Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: close}}}},
		}
	}
	apply := func(indexID string, version uint64, mode string, rows ...*pb.ViewIndexRowWrite) {
		t.Helper()
		rsp, err := svc.ApplyViewIndex(ctx, &pb.ApplyViewIndexReq{AuthInfo: auth, IndexId: indexID, Batch: &pb.ViewIndexWriteBatch{ViewRevision: version, ViewSchemaHash: fmt.Sprintf("schema-%d", version), WriteMode: mode, RowWrites: rows}})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("apply %s: rsp=%v err=%v", indexID, rsp, err)
		}
	}
	attach := func(indexID string, version uint64) {
		t.Helper()
		if err := svc.AttachActiveView(&pb.View{SpaceId: "quant", ViewId: "prices-view", PrimaryDatasetId: "prices", DatasetIds: []string{"prices"}, ActiveIndexId: indexID, ActiveViewRevision: version, ActiveViewSchemaHash: fmt.Sprintf("schema-%d", version), Engine: "duckdb", ActiveColumns: columns, Status: "active"}); err != nil {
			t.Fatalf("attach %s: %v", indexID, err)
		}
	}
	query := func(want int, wantLast float64) {
		t.Helper()
		rsp, err := svc.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{AuthInfo: auth, SpaceId: "quant", ViewId: "prices-view", Selectors: []*pb.TimeSeriesSelector{{SpaceId: "quant", DatasetId: "prices", SubjectId: "BTC-USDT", Freq: "1m"}}, ColumnNames: []string{"close"}, Sorts: []*pb.SortSpec{{FieldName: "data_time"}}})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(rsp.GetRows()) != want {
			t.Fatalf("query rows=%d want=%d rsp=%v err=%v", len(rsp.GetRows()), want, rsp, err)
		}
		last := rsp.GetRows()[len(rsp.GetRows())-1].GetFields()[0].GetValue().GetDoubleValue()
		if last != wantLast {
			t.Fatalf("query last close=%v want=%v", last, wantLast)
		}
	}

	oldID := viewindex.ViewIndexID("quant", "prices-view", viewindex.SlotA)
	newID := viewindex.ViewIndexID("quant", "prices-view", viewindex.SlotB)
	prepare(oldID, 1)
	apply(oldID, 1, "LIVE_WRITE", key("2026-08-11T00:00:00Z", 100))
	attach(oldID, 1)
	prepare(newID, 2)
	if err := svc.BackfillView(ctx, "quant", "prices-view", 100); err != nil {
		t.Fatal(err)
	}
	// A live delta is written to both sides while B is ready but not active.
	apply(oldID, 1, "LIVE_WRITE", key("2026-08-11T00:01:00Z", 101))
	apply(newID, 2, "LIVE_WRITE", key("2026-08-11T00:01:00Z", 101))
	query(2, 101)
	if err := svc.SwitchView(ctx, "quant", "prices-view", 0); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(root, "duckdb", oldID+".duckdb")
	newPath := filepath.Join(root, "duckdb", newID+".duckdb")
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("old DuckDB file disappeared before cleanup: %v", err)
	}
	if err := os.WriteFile(oldPath+".wal", []byte("retired"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("new active DuckDB file missing: %v", err)
	}
	query(2, 101)

	metadata := &cleanupMetadata{views: []*pb.View{{
		SpaceId: "quant", ViewId: "prices-view", ActiveIndexId: newID, Engine: "duckdb", Status: "active",
	}}}
	now := time.Date(2026, 8, 19, 2, 0, 0, 0, time.UTC)
	opts := RetiredIndexCleanupOptions{Metadata: metadata, MinUnreferencedAge: time.Minute, Now: func() time.Time { return now }}
	if err := svc.CleanupRetiredIndexes(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("first discovery removed old DuckDB file: %v", err)
	}
	query(2, 101)

	now = now.Add(time.Minute)
	if err := svc.CleanupRetiredIndexes(ctx, opts); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{oldPath, oldPath + ".wal"} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("retired DuckDB artifact still exists path=%s err=%v", path, err)
		}
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("active DuckDB file was deleted: %v", err)
	}
	query(2, 101)
}

func TestDuckDBBackfillDoesNotBlockActiveLiveWrite(t *testing.T) {
	root := filepath.Join(t.TempDir(), "views")
	svc, err := New(root, "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	aColumns := []*pb.ViewColumn{{SpaceId: "quant", ViewId: "prices-view", OriginId: "prices.close", ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}
	bColumns := append(append([]*pb.ViewColumn(nil), aColumns...), &pb.ViewColumn{SpaceId: "quant", ViewId: "prices-view", OriginId: "prices.extra", ColumnName: "extra", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE})
	schema := func(version uint64, columns []*pb.ViewColumn) *pb.ViewIndexSchema {
		return &pb.ViewIndexSchema{SpaceId: "quant", ViewId: "prices-view", PrimaryDatasetId: "prices", DatasetIds: []string{"prices"}, ViewVersion: version, Engine: "duckdb", ViewSchemaHash: fmt.Sprintf("schema-%d", version), Columns: columns}
	}
	prepare := func(indexID string, version uint64, columns []*pb.ViewColumn) {
		t.Helper()
		rsp, err := svc.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{AuthInfo: auth, IndexId: indexID, Schema: schema(version, columns)})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("prepare %s: rsp=%v err=%v", indexID, rsp, err)
		}
	}
	key := func(at string) *pb.RowKey {
		return &pb.RowKey{SpaceId: "quant", DatasetId: "prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: at}}}
	}
	apply := func(indexID string, version uint64, rows []*pb.ViewIndexRowWrite) {
		t.Helper()
		rsp, err := svc.ApplyViewIndex(ctx, &pb.ApplyViewIndexReq{AuthInfo: auth, IndexId: indexID, Batch: &pb.ViewIndexWriteBatch{ViewRevision: version, ViewSchemaHash: fmt.Sprintf("schema-%d", version), WriteMode: "LIVE_WRITE", RowWrites: rows}})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("apply %s: rsp=%v err=%v", indexID, rsp, err)
		}
	}
	prepare("prices-a", 1, aColumns)
	if err := svc.AttachActiveView(&pb.View{SpaceId: "quant", ViewId: "prices-view", PrimaryDatasetId: "prices", DatasetIds: []string{"prices"}, ActiveIndexId: "prices-a", ActiveViewRevision: 1, ActiveViewSchemaHash: "schema-1", Engine: "duckdb", ActiveColumns: aColumns, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	apply("prices-a", 1, []*pb.ViewIndexRowWrite{{
		Key:    &pb.ViewIndexRowKey{RowKey: key("2026-08-11T00:00:00Z")},
		Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}}}},
	}})
	prepare("prices-b", 2, bColumns)
	svc.SetPrimaryAuth(auth)
	reader := &blockingFieldReader{started: make(chan struct{}), release: make(chan struct{})}
	backfillErr := make(chan error, 1)
	go func() { backfillErr <- svc.BackfillViewWithReader(ctx, "quant", "prices-view", 100, reader) }()
	select {
	case <-reader.started:
	case <-ctx.Done():
		t.Fatalf("backfill did not reach the blocked field read: %v", ctx.Err())
	}
	liveErr := make(chan error, 1)
	go func() {
		liveErr <- svc.applyDatasetEvent(ctx, "quant", "prices", []*pb.RowFieldUpsert{{
			Key: key("2026-08-11T00:01:00Z"),
			Fields: []*pb.FieldValue{
				{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 101}}},
				{FieldId: "extra", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 8}}},
			}}})
	}()
	select {
	case err := <-liveErr:
		if err != nil {
			t.Fatalf("live write blocked/failed while backfill was reading: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("active live write was blocked by backfill field read")
	}
	close(reader.release)
	if err := <-backfillErr; err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if err := svc.SwitchView(ctx, "quant", "prices-view", 0); err != nil {
		t.Fatalf("switch: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		rsp, queryErr := svc.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{AuthInfo: auth, SpaceId: "quant", ViewId: "prices-view", Selectors: []*pb.TimeSeriesSelector{{SpaceId: "quant", DatasetId: "prices", SubjectId: "BTC-USDT", Freq: "1m"}}, ColumnNames: []string{"close", "extra"}, Sorts: []*pb.SortSpec{{FieldName: "data_time"}}})
		if queryErr == nil && rsp.GetRetInfo().GetCode() == pb.ErrorCode_SUCCESS && len(rsp.GetRows()) == 2 {
			if got := rsp.GetRows()[1].GetFields()[0].GetValue().GetDoubleValue(); got != 101 {
				t.Fatalf("switched latest close=%v, want 101", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("switched view did not expose historical and live rows: rsp=%v err=%v", rsp, queryErr)
		}
		time.Sleep(10 * time.Millisecond)
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
		if err := svc.AttachActiveView(&pb.View{SpaceId: "space", ViewId: id, ActiveIndexId: id, ActiveViewRevision: 1, ActiveViewSchemaHash: "hash", Engine: map[string]string{"times": "duckdb", "records": "bleve"}[id], ActiveColumns: []*pb.ViewColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}, {ColumnName: "title", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING}}, Status: "active"}); err != nil {
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
				ViewVersion:    1,
				Engine:         "bleve",
				ViewSchemaHash: "hash",
				Columns:        []*pb.ViewColumn{{OriginId: "shared.value", ColumnName: "value"}},
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

func TestSecondaryDatasetEventMapsToPrimaryViewGrain(t *testing.T) {
	writes := eventWrites(viewindex.ViewIndexSchema{
		PrimaryDatasetID: "primary",
		Columns: []*pb.ViewColumn{{
			OriginId: "secondary.factor", ColumnName: "secondary.factor",
		}},
	}, "secondary", []*pb.RowFieldUpsert{{
		Key: &pb.RowKey{SpaceId: "space", DatasetId: "secondary", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}},
		Fields: []*pb.FieldValue{{
			FieldId: "factor", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1.5}},
		}},
	}})
	if len(writes) != 1 || writes[0].Key.Key.GetDatasetId() != "primary" {
		t.Fatalf("writes=%v", writes)
	}
}

func TestSecondaryEventRecoversCompleteMissingViewRow(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	if rsp, err := svc.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{AuthInfo: auth, IndexId: "multi", Schema: &pb.ViewIndexSchema{
		SpaceId: "space", ViewId: "multi", PrimaryDatasetId: "primary", ViewVersion: 1, Engine: "bleve", ViewSchemaHash: "hash",
		Columns: []*pb.ViewColumn{{OriginId: "primary.base", ColumnName: "base"}, {OriginId: "secondary.factor", ColumnName: "factor"}},
	}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("prepare rsp=%v err=%v", rsp, err)
	}
	svc.views[viewRef{spaceID: "space", viewID: "multi"}].mu.Lock()
	svc.views[viewRef{spaceID: "space", viewID: "multi"}].active = "multi"
	svc.views[viewRef{spaceID: "space", viewID: "multi"}].next = ""
	svc.views[viewRef{spaceID: "space", viewID: "multi"}].status = "active"
	svc.views[viewRef{spaceID: "space", viewID: "multi"}].mu.Unlock()
	svc.SetPrimaryAuth(&pb.AuthInfo{AppId: "storage-view"})
	svc.SetPrimaryReader(recoveryFieldReader{primaryPresent: true})
	event := &pb.RowFieldUpsert{
		Key:    &pb.RowKey{SpaceId: "space", DatasetId: "secondary", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}},
		Fields: []*pb.FieldValue{{FieldId: "factor", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 2}}}},
	}
	engine, err := svc.engineFor("multi")
	if err != nil {
		t.Fatal(err)
	}
	schema := svc.schemas["multi"]
	initial := eventWrites(schema, "secondary", []*pb.RowFieldUpsert{event})
	recovered, err := svc.recoverMissingRows(ctx, engine, "multi", schema, "secondary", []*pb.RowFieldUpsert{event}, initial)
	if err != nil || len(recovered) != 1 || len(recovered[0].Fields) != 2 {
		t.Fatalf("recover failed recovered=%v err=%v", recovered, err)
	}
	if err := svc.applyDatasetEvent(ctx, "space", "secondary", []*pb.RowFieldUpsert{event}); err != nil {
		t.Fatal(err)
	}
	key := &pb.RowKey{SpaceId: "space", DatasetId: "primary", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}
	rows, err := svc.query(ctx, "multi", []*pb.RowKey{key}, nil)
	if err != nil || len(rows) != 1 || len(rows[0].GetFields()) != 2 {
		t.Fatalf("recovered rows=%v err=%v", rows, err)
	}
	svc.SetPrimaryReader(recoveryFieldReader{primaryPresent: false})
	if err := svc.applyDatasetEvent(ctx, "space", "secondary", []*pb.RowFieldUpsert{event}); err != nil {
		t.Fatal(err)
	}
	rows, err = svc.query(ctx, "multi", []*pb.RowKey{key}, nil)
	if err != nil || len(rows) != 1 {
		t.Fatalf("row unexpectedly disappeared: rows=%v err=%v", rows, err)
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
		SpaceId: "space", ViewId: "single", PrimaryDatasetId: "market", ViewVersion: 1, Engine: "bleve", ViewSchemaHash: "hash",
		Columns: []*pb.ViewColumn{{OriginId: "market.close", ColumnName: "close"}},
	}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("prepare rsp=%v err=%v", rsp, err)
	}
	svc.views[viewRef{spaceID: "space", viewID: "single"}].mu.Lock()
	svc.views[viewRef{spaceID: "space", viewID: "single"}].active = "single"
	svc.views[viewRef{spaceID: "space", viewID: "single"}].next = ""
	svc.views[viewRef{spaceID: "space", viewID: "single"}].status = "active"
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
		SpaceId: "space", ViewId: "prices", Status: "active", DatasetIds: []string{"market"},
	}}
	svc.setMetadataClient(metadata)
	svc.setMaintenanceReady(true)
	row := &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: "space", DatasetId: "market", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}}
	if err := svc.applyDatasetEvent(context.Background(), "space", "market", []*pb.RowFieldUpsert{row}); err == nil {
		t.Fatal("managed Dataset row was ACKable before its View mapping was attached")
	}
}

func TestInitialPrimingBuildAcksRowsAfterWritingReplacement(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	columns := []*pb.ViewColumn{{OriginId: "market.close", ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}
	if rsp, err := svc.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{AuthInfo: auth, IndexId: "priming-view", Schema: &pb.ViewIndexSchema{
		SpaceId: "space", ViewId: "priming", PrimaryDatasetId: "market", DatasetIds: []string{"market"}, ViewVersion: 1, Engine: "bleve", ViewSchemaHash: "hash", Columns: columns,
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
		SpaceId: "space", ViewId: "empty", PrimaryDatasetId: "market", DatasetIds: []string{"market"}, ViewVersion: 1, Engine: "bleve", ViewSchemaHash: "hash",
	}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("prepare empty view: rsp=%v err=%v", rsp, err)
	}
	view := &pb.View{SpaceId: "space", ViewId: "empty", PrimaryDatasetId: "market", DatasetIds: []string{"market"}, ActiveIndexId: "empty-view", ActiveViewRevision: 1, DesiredViewRevision: 1, ActiveViewSchemaHash: "hash", Engine: "bleve", Status: "active", Attributes: map[string]string{viewColumnsExplicitAttr: "true"}}
	if err := svc.AttachActiveView(view); err != nil {
		t.Fatal(err)
	}
	row := &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: "space", DatasetId: "market", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}}
	if err := svc.applyDatasetEvent(ctx, "space", "market", []*pb.RowFieldUpsert{row}); err != nil {
		t.Fatalf("explicit empty view must ACK its Dataset event: %v", err)
	}
}

func TestLiveRowUsesHealthyReplacementWhenActiveIndexIsMissing(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	if rsp, err := svc.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{AuthInfo: auth, IndexId: "replacement", Schema: &pb.ViewIndexSchema{
		SpaceId: "space", ViewId: "prices", PrimaryDatasetId: "market", ViewVersion: 1, Engine: "bleve", ViewSchemaHash: "hash",
		Columns: []*pb.ViewColumn{{OriginId: "market.close", ColumnName: "close"}},
	}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("prepare replacement: rsp=%v err=%v", rsp, err)
	}
	viewKey := viewRef{spaceID: "space", viewID: "prices"}
	svc.views[viewKey].mu.Lock()
	svc.views[viewKey].active = "missing-active"
	svc.views[viewKey].next = "replacement"
	svc.views[viewKey].status = "building"
	svc.views[viewKey].mu.Unlock()
	svc.mu.Lock()
	svc.indexView["missing-active"] = viewKey
	svc.byData[datasetRef{spaceID: "space", datasetID: "market"}]["missing-active"] = struct{}{}
	svc.mu.Unlock()

	row := &pb.RowFieldUpsert{
		Key:    &pb.RowKey{SpaceId: "space", DatasetId: "market", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}},
		Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}}}},
	}
	if err := svc.applyDatasetEvent(ctx, "space", "market", []*pb.RowFieldUpsert{row}); err == nil {
		t.Fatal("replacement write was ACKed before activation")
	}
	svc.views[viewKey].mu.Lock()
	svc.views[viewKey].active = "replacement"
	svc.views[viewKey].next = ""
	svc.views[viewKey].status = "active"
	svc.views[viewKey].mu.Unlock()
	if err := svc.applyDatasetEvent(ctx, "space", "market", []*pb.RowFieldUpsert{row}); err != nil {
		t.Fatalf("replacement live write after activation: %v", err)
	}
	engine, err := svc.engineFor("replacement")
	if err != nil {
		t.Fatal(err)
	}
	stats, err := engine.Stat(ctx, "replacement")
	if err != nil || !stats.Exists || stats.EntryCount != 1 {
		t.Fatalf("replacement stats=%+v err=%v", stats, err)
	}
}

func TestViewEventUsesDatasetDots(t *testing.T) {
	schema := viewindex.ViewIndexSchema{SpaceID: "space", ViewID: "dots", PrimaryDatasetID: "primary", ViewVersion: 1, SchemaHash: "hash", Columns: []*pb.ViewColumn{
		{OriginId: "market.v2.close", ColumnName: "close"},
	}}
	event := &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: "space", DatasetId: "market.v2", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 3}}}}}
	writes := eventWrites(schema, "market.v2", []*pb.RowFieldUpsert{event})
	if len(writes) != 1 || len(writes[0].Fields) != 1 || writes[0].Fields[0].GetFieldId() != "close" {
		t.Fatalf("dataset with dot was not mapped: %v", writes)
	}
}

func TestViewBuildBackfillDoesNotOverwriteLiveAndSwitchesAtomically(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	schema := func() *pb.ViewIndexSchema {
		return &pb.ViewIndexSchema{
			SpaceId: "space", ViewId: "logical", PrimaryDatasetId: "shared", ViewVersion: 1, Engine: "bleve", ViewSchemaHash: "hash",
			Columns: []*pb.ViewColumn{{OriginId: "shared.value", ColumnName: "shared.value"}},
		}
	}
	prepare := func(id string) {
		t.Helper()
		rsp, err := svc.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{AuthInfo: auth, IndexId: id, Schema: schema()})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("prepare %s rsp=%v err=%v", id, rsp, err)
		}
	}
	key := &pb.RowKey{SpaceId: "space", DatasetId: "shared", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}
	apply := func(id, value, mode string) {
		t.Helper()
		rsp, err := svc.ApplyViewIndex(ctx, &pb.ApplyViewIndexReq{
			AuthInfo: auth, IndexId: id,
			Batch: &pb.ViewIndexWriteBatch{
				ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: mode,
				RowWrites: []*pb.ViewIndexRowWrite{{
					Key:    &pb.ViewIndexRowKey{RowKey: key},
					Fields: []*pb.FieldValue{{FieldId: "shared.value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: value}}}},
				}},
			},
		})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("apply %s rsp=%v err=%v", id, rsp, err)
		}
	}
	prepare("idx-a")
	apply("idx-a", "old", "LIVE_WRITE")
	prepare("idx-b")
	apply("idx-b", "live", "LIVE_WRITE")
	if err := svc.BackfillView(ctx, "space", "logical", 100); err != nil {
		t.Fatal(err)
	}
	if err := svc.SwitchView(ctx, "space", "logical", time.Hour); err != nil {
		t.Fatal(err)
	}
	rsp, err := svc.SearchRecordRows(ctx, &pb.SearchRecordRowsReq{
		AuthInfo: auth, SpaceId: "space", ViewId: "logical",
		Keys:        []*pb.RecordKey{{SpaceId: "space", DatasetId: "shared", RecordId: "r", Version: "1"}},
		ColumnNames: []string{"shared.value"},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(rsp.GetRows()) != 1 ||
		rsp.GetRows()[0].GetFields()[0].GetValue().GetStringValue() != "live" {
		t.Fatalf("rsp=%v err=%v", rsp, err)
	}
}

func TestBackfillReadsNewDatasetColumnsByExistingGrain(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	svc.SetPrimaryAuth(&pb.AuthInfo{AppId: "storage-view", AppKey: "primary-key"})
	ctx := context.Background()
	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	prepare := func(id string, columns ...*pb.ViewColumn) {
		t.Helper()
		rsp, err := svc.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{
			AuthInfo: auth, IndexId: id,
			Schema: &pb.ViewIndexSchema{
				SpaceId: "space", ViewId: "joined", PrimaryDatasetId: "primary", ViewVersion: 1, Engine: "bleve",
				ViewSchemaHash: "hash", Columns: columns,
			},
		})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("prepare %s rsp=%v err=%v", id, rsp, err)
		}
	}
	primaryColumn := &pb.ViewColumn{OriginId: "primary.close", ColumnName: "primary.close"}
	secondaryColumn := &pb.ViewColumn{OriginId: "secondary.factor", ColumnName: "secondary.factor"}
	prepare("join-a", primaryColumn)
	key := &pb.RowKey{SpaceId: "space", DatasetId: "primary", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}
	rsp, err := svc.ApplyViewIndex(ctx, &pb.ApplyViewIndexReq{
		AuthInfo: auth, IndexId: "join-a",
		Batch: &pb.ViewIndexWriteBatch{
			ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: "LIVE_WRITE",
			RowWrites: []*pb.ViewIndexRowWrite{{
				Key:    &pb.ViewIndexRowKey{RowKey: key},
				Fields: []*pb.FieldValue{{FieldId: "primary.close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}}}},
			}},
		},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("apply rsp=%v err=%v", rsp, err)
	}
	if err := svc.AttachActiveView(&pb.View{SpaceId: "space", ViewId: "joined", ActiveIndexId: "join-a", ActiveViewRevision: 1, ActiveViewSchemaHash: "hash", Engine: "bleve", ActiveColumns: []*pb.ViewColumn{primaryColumn}, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	prepare("join-b", primaryColumn, secondaryColumn)
	if err := svc.BackfillViewWithReader(ctx, "space", "joined", 100, fakeFieldReader{}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SwitchView(ctx, "space", "joined", time.Hour); err != nil {
		t.Fatal(err)
	}
	result, err := svc.SearchRecordRows(ctx, &pb.SearchRecordRowsReq{
		AuthInfo: auth, SpaceId: "space", ViewId: "joined",
		Keys:        []*pb.RecordKey{{SpaceId: "space", DatasetId: "primary", RecordId: "r", Version: "1"}},
		ColumnNames: []string{"primary.close", "secondary.factor"},
	})
	if err != nil || result.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(result.GetRows()) != 1 || len(result.GetRows()[0].GetFields()) != 2 {
		t.Fatalf("result=%v err=%v", result, err)
	}
}

func successRetInfo() *pb.RetInfo {
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}
}
