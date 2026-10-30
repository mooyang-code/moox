//go:build cgo

package view

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestFollowingViewColumnAddDoesNotRebuild(t *testing.T) {
	ctx := context.Background()
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, engine := range svc.engines {
			if closer, ok := engine.(interface{ Close() error }); ok {
				_ = closer.Close()
			}
		}
	}()
	engine := svc.engines["duckdb"]
	if engine == nil {
		t.Fatal("DuckDB engine is unavailable")
	}
	const datasetID = "dataset_factor_btc_1m"
	const viewID = "view_factor_btc_1m"
	const indexID = "factor-view-a"
	closeColumn := &pb.ViewColumn{
		SpaceId: "space", ViewId: viewID, ColumnName: "close", OriginId: "close",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	}
	biasColumn := &pb.ViewColumn{
		SpaceId: "space", ViewId: viewID, ColumnName: "bias_20", OriginId: "bias_20",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, SortOrder: 1,
	}
	activeSchema := viewindex.ViewIndexSchema{
		SpaceID: "space", ViewID: viewID, PrimaryDatasetID: datasetID, ViewVersion: 1,
		Engine: "duckdb", Columns: []*pb.ViewColumn{closeColumn},
	}
	activeSchema.SchemaHash = viewindex.HashViewIndexSchema(activeSchema)
	if err := engine.Prepare(ctx, indexID, activeSchema); err != nil {
		t.Fatal(err)
	}
	rowKey := &pb.RowKey{SpaceId: "space", DatasetId: datasetID, Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
		SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-10-04T00:00:00Z",
	}}}
	if err := engine.Write(ctx, indexID, viewindex.ViewIndexWriteBatch{
		ViewRevision: 1, ViewSchemaHash: activeSchema.SchemaHash, WriteMode: viewindex.LiveWrite,
		RowWrites: []viewindex.RowWrite{{Key: viewindex.RowKey{Key: rowKey}, Fields: []*pb.FieldValue{{
			FieldId: closeColumn.GetColumnName(), Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 10}},
		}}}},
	}); err != nil {
		t.Fatal(err)
	}
	desiredSchema := viewindex.ViewIndexSchema{
		SpaceID: "space", ViewID: viewID, PrimaryDatasetID: datasetID, ViewVersion: 2,
		Engine: "duckdb", Columns: []*pb.ViewColumn{closeColumn, biasColumn},
	}
	desiredSchema.SchemaHash = viewindex.HashViewIndexSchema(desiredSchema)
	metadata := &maintenanceMetadata{view: &pb.View{
		SpaceId: "space", ViewId: viewID, DatasetId: datasetID, Engine: "duckdb", ActiveIndexId: indexID,
		ViewRevision: 1, ActiveViewSchemaHash: activeSchema.SchemaHash,
		ActiveColumns: []*pb.ViewColumn{closeColumn}, Columns: []*pb.ViewColumn{closeColumn, biasColumn},
		Status: "active", Attributes: map[string]string{
			"follow_dataset_columns": "true",
		},
	}}
	if err := svc.AttachActiveView(ctx, metadata.view); err != nil {
		t.Fatalf("attach active view: %v", err)
	}
	newFieldEvent := &pb.RowFieldUpsert{
		Key: rowKey,
		Fields: []*pb.FieldValue{
			{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 10}}},
			{FieldId: "bias_20", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 0.5}}},
		},
	}
	// 事件里 View 还没扩展出来的新列直接裁掉：不阻塞消费，扩展完成后这一行的新列为空（系统边界，见设计 3.4）。
	if _, err := svc.applyEventToIndex(ctx, indexID, datasetID, []*pb.RowFieldUpsert{newFieldEvent}); err != nil {
		t.Fatalf("event with a not-yet-active column must be applied with the new column trimmed: %v", err)
	}

	svc.setMetadataClient(metadata)
	svc.extendViewSchemasRound(ctx)
	if metadata.completes != 0 || metadata.extensions != 1 {
		t.Fatalf("加列不应重建：completes=%d extensions=%d", metadata.completes, metadata.extensions)
	}
	// 没有新列时不再提交。
	svc.extendViewSchemasRound(ctx)
	if metadata.extensions != 1 {
		t.Fatalf("没有新列时不应提交：extensions=%d", metadata.extensions)
	}
	if metadata.view.GetActiveIndexId() != indexID || metadata.view.GetViewRevision() != 2 {
		t.Fatalf("active contract after extension = index %q revision %d", metadata.view.GetActiveIndexId(), metadata.view.GetViewRevision())
	}
	stats, err := engine.(viewindex.MetadataStatReader).StatMetadata(ctx, indexID)
	if err != nil || stats.ViewVersion != 2 || stats.SchemaHash != desiredSchema.SchemaHash {
		t.Fatalf("physical schema = %#v err=%v, want revision 2", stats, err)
	}
	rows, _, err := engine.Query(ctx, indexID, viewindex.QuerySpec{
		Selectors: []viewindex.TimeSeriesSelector{{SpaceID: "space", DatasetID: datasetID, SubjectID: "BTC-USDT", Freq: "1m"}},
		Includes:  []string{biasColumn.GetColumnName()},
	})
	if err != nil || len(rows) != 1 || len(rows[0].GetFields()) != 1 || rows[0].GetFields()[0].GetValue().GetNullValue() != pb.NullValue_NULL_VALUE_NULL {
		t.Fatalf("existing row after extension = %v err=%v, want NULL bias_20", rows, err)
	}
	if _, err := svc.applyEventToIndex(ctx, indexID, datasetID, []*pb.RowFieldUpsert{newFieldEvent}); err != nil {
		t.Fatalf("event after extension: %v", err)
	}
}

func TestRestoreRecoversViewSchemaExtensionAfterRestart(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "views")
	first, err := New(root, "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	engine := first.engines["duckdb"]
	if engine == nil {
		t.Fatal("DuckDB engine is unavailable")
	}
	const datasetID = "dataset_factor_btc_1m"
	const viewID = "view_factor_btc_1m"
	const indexID = "factor-view-a"
	closeColumn := &pb.ViewColumn{
		SpaceId: "space", ViewId: viewID, ColumnName: "close", OriginId: "close",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	}
	biasColumn := &pb.ViewColumn{
		SpaceId: "space", ViewId: viewID, ColumnName: "bias_20", OriginId: "bias_20",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, SortOrder: 1,
	}
	activeSchema := viewindex.ViewIndexSchema{
		SpaceID: "space", ViewID: viewID, PrimaryDatasetID: datasetID, ViewVersion: 1,
		Engine: "duckdb", Columns: []*pb.ViewColumn{closeColumn},
	}
	activeSchema.SchemaHash = viewindex.HashViewIndexSchema(activeSchema)
	if err := engine.Prepare(ctx, indexID, activeSchema); err != nil {
		t.Fatal(err)
	}
	rowKey := &pb.RowKey{SpaceId: "space", DatasetId: datasetID, Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
		SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-10-04T00:00:00Z",
	}}}
	if err := engine.Write(ctx, indexID, viewindex.ViewIndexWriteBatch{
		ViewRevision: 1, ViewSchemaHash: activeSchema.SchemaHash, WriteMode: viewindex.LiveWrite,
		RowWrites: []viewindex.RowWrite{{Key: viewindex.RowKey{Key: rowKey}, Fields: []*pb.FieldValue{{
			FieldId: closeColumn.GetColumnName(), Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 10}},
		}}}},
	}); err != nil {
		t.Fatal(err)
	}
	desiredSchema := viewindex.ViewIndexSchema{
		SpaceID: "space", ViewID: viewID, PrimaryDatasetID: datasetID, ViewVersion: 2,
		Engine: "duckdb", Columns: []*pb.ViewColumn{closeColumn, biasColumn},
	}
	desiredSchema.SchemaHash = viewindex.HashViewIndexSchema(desiredSchema)
	extender, ok := engine.(viewindex.SchemaExtender)
	if !ok {
		t.Fatal("DuckDB engine does not support schema extension")
	}
	if err := extender.ExtendColumns(ctx, indexID, activeSchema, desiredSchema); err != nil {
		t.Fatalf("simulate DuckDB commit before Metadata CAS: %v", err)
	}

	metadata := &maintenanceMetadata{view: &pb.View{
		SpaceId: "space", ViewId: viewID, DatasetId: datasetID, Engine: "duckdb", ActiveIndexId: indexID,
		ViewRevision: 1, ActiveViewSchemaHash: activeSchema.SchemaHash,
		ActiveColumns: []*pb.ViewColumn{closeColumn}, Columns: []*pb.ViewColumn{closeColumn, biasColumn},
		Status: "active", Attributes: map[string]string{
			"follow_dataset_columns": "true",
		},
	}}
	closeEngines := func(service *Service) {
		for _, candidate := range service.engines {
			if closer, ok := candidate.(interface{ Close() error }); ok {
				_ = closer.Close()
			}
		}
	}
	closeEngines(first)

	// A fresh View process sees Metadata at revision 1 and DuckDB at revision
	// 2, exactly the crash window before the metadata CAS.
	restarted, err := New(root, "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer closeEngines(restarted)
	if err := restarted.RestoreActiveViews(ctx, MaintenanceOptions{Metadata: metadata}); err != nil {
		t.Fatalf("restore factor result active index: %v", err)
	}
	if metadata.extensions != 1 || metadata.completes != 0 {
		t.Fatalf("重启后应当补提交已有索引：extensions=%d completes=%d", metadata.extensions, metadata.completes)
	}
	if metadata.view.GetActiveIndexId() != indexID || metadata.view.GetViewRevision() != 2 {
		t.Fatalf("recovered active contract = index %q revision %d", metadata.view.GetActiveIndexId(), metadata.view.GetViewRevision())
	}
	rows, _, err := restarted.engines["duckdb"].Query(ctx, indexID, viewindex.QuerySpec{
		Selectors: []viewindex.TimeSeriesSelector{{SpaceID: "space", DatasetID: datasetID, SubjectID: "BTC-USDT", Freq: "1m"}},
		Includes:  []string{closeColumn.GetColumnName(), biasColumn.GetColumnName()},
	})
	if err != nil || len(rows) != 1 || len(rows[0].GetFields()) != 2 {
		t.Fatalf("row after restart recovery = %v err=%v", rows, err)
	}
	fields := make(map[string]*pb.TypedValue, len(rows[0].GetFields()))
	for _, field := range rows[0].GetFields() {
		fields[field.GetFieldId()] = field.GetValue()
	}
	if got := fields[closeColumn.GetColumnName()].GetDoubleValue(); got != 10 {
		t.Fatalf("existing value after restart recovery = %v, want 10", got)
	}
	if got := fields[biasColumn.GetColumnName()].GetNullValue(); got != pb.NullValue_NULL_VALUE_NULL {
		t.Fatalf("new value after restart recovery = %v, want NULL", got)
	}

	volumeColumn := &pb.ViewColumn{
		SpaceId: "space", ViewId: viewID, ColumnName: "volume", OriginId: "volume",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, SortOrder: 2,
	}
	currentSchema := desiredSchema
	restartedExtender, ok := restarted.engines["duckdb"].(viewindex.SchemaExtender)
	if !ok {
		t.Fatal("restarted DuckDB engine does not support schema extension")
	}
	nextSchema := viewindex.ViewIndexSchema{
		SpaceID: "space", ViewID: viewID, PrimaryDatasetID: datasetID, ViewVersion: 3,
		Engine: "duckdb", Columns: []*pb.ViewColumn{closeColumn, biasColumn, volumeColumn},
	}
	nextSchema.SchemaHash = viewindex.HashViewIndexSchema(nextSchema)
	if err := restartedExtender.ExtendColumns(ctx, indexID, currentSchema, nextSchema); err != nil {
		t.Fatalf("simulate later DuckDB commit before Metadata CAS: %v", err)
	}
	metadata.view.Columns = []*pb.ViewColumn{closeColumn, biasColumn, volumeColumn}
	if err := restarted.maintainView(ctx, MaintenanceOptions{Metadata: metadata}, restarted.internalAuth(), metadata.view); err != nil {
		t.Fatalf("maintainer recovery of an ahead physical schema: %v", err)
	}
	if metadata.extensions != 2 || metadata.completes != 0 || metadata.view.GetViewRevision() != 3 {
		t.Fatalf("维护应当补提交同一个索引：extensions=%d completes=%d revision=%d",
			metadata.extensions, metadata.completes, metadata.view.GetViewRevision())
	}
}
