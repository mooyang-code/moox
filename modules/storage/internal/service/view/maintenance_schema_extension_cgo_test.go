//go:build cgo

package view

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestFactorResultColumnAddDoesNotRebuild(t *testing.T) {
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
		SpaceId: "space", ViewId: viewID, ColumnName: datasetID + ".close", OriginId: datasetID + ".close",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	}
	biasColumn := &pb.ViewColumn{
		SpaceId: "space", ViewId: viewID, ColumnName: datasetID + ".bias_20", OriginId: datasetID + ".bias_20",
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
	metadata := &capacityMaintenanceMetadata{maintenanceMetadata: maintenanceMetadata{view: &pb.View{
		SpaceId: "space", ViewId: viewID, DatasetId: datasetID, Engine: "duckdb", ActiveIndexId: indexID,
		ActiveViewRevision: 1, DesiredViewRevision: 2, ActiveViewSchemaHash: activeSchema.SchemaHash,
		ActiveColumns: []*pb.ViewColumn{closeColumn}, Columns: []*pb.ViewColumn{closeColumn, biasColumn},
		Status: "active", Attributes: map[string]string{
			"dataset_role": "factor_result", "view_role": "factor_result", "primary_dataset_role": "factor_result",
			activePrimaryDatasetAttr: datasetID,
		},
	}}}
	if err := svc.AttachActiveViewWithGrace(ctx, metadata.view, 0); err != nil {
		t.Fatalf("attach active view: %v", err)
	}
	newFieldEvent := &pb.RowFieldUpsert{
		Key: rowKey,
		Fields: []*pb.FieldValue{
			{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 10}}},
			{FieldId: "bias_20", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 0.5}}},
		},
	}
	if _, err := svc.applyEventToIndex(ctx, indexID, datasetID, []*pb.RowFieldUpsert{newFieldEvent}); err == nil {
		t.Fatal("event with a not-yet-active factor column was accepted")
	}

	if err := svc.maintainView(ctx, MaintenanceOptions{Metadata: metadata}, svc.internalAuth(), metadata.view); err != nil {
		t.Fatalf("maintainView: %v", err)
	}
	if metadata.claims != 0 || metadata.extensions != 1 || metadata.activated {
		t.Fatalf("maintenance rebuilt instead of extending: claims=%d extensions=%d activated=%v", metadata.claims, metadata.extensions, metadata.activated)
	}
	if metadata.created != nil {
		t.Fatalf("unexpected rebuild record: %v", metadata.created)
	}
	if metadata.view.GetActiveIndexId() != indexID || metadata.view.GetActiveViewRevision() != 2 {
		t.Fatalf("active contract after extension = index %q revision %d", metadata.view.GetActiveIndexId(), metadata.view.GetActiveViewRevision())
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

func TestRestoreRecoversFactorResultSchemaExtensionAfterRestart(t *testing.T) {
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
		SpaceId: "space", ViewId: viewID, ColumnName: datasetID + ".close", OriginId: datasetID + ".close",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	}
	biasColumn := &pb.ViewColumn{
		SpaceId: "space", ViewId: viewID, ColumnName: datasetID + ".bias_20", OriginId: datasetID + ".bias_20",
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

	metadata := &capacityMaintenanceMetadata{maintenanceMetadata: maintenanceMetadata{view: &pb.View{
		SpaceId: "space", ViewId: viewID, DatasetId: datasetID, Engine: "duckdb", ActiveIndexId: indexID,
		ActiveViewRevision: 1, DesiredViewRevision: 2, ActiveViewSchemaHash: activeSchema.SchemaHash,
		ActiveColumns: []*pb.ViewColumn{closeColumn}, Columns: []*pb.ViewColumn{closeColumn, biasColumn},
		Status: "active", Attributes: map[string]string{
			"dataset_role": "factor_result", "view_role": "factor_result", "primary_dataset_role": "factor_result",
			activePrimaryDatasetAttr: datasetID,
		},
	}}}
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
	if metadata.extensions != 1 || metadata.claims != 0 || metadata.activated {
		t.Fatalf("restart recovery did not CAS the existing index: extensions=%d claims=%d activated=%v", metadata.extensions, metadata.claims, metadata.activated)
	}
	if metadata.view.GetActiveIndexId() != indexID || metadata.view.GetActiveViewRevision() != 2 {
		t.Fatalf("recovered active contract = index %q revision %d", metadata.view.GetActiveIndexId(), metadata.view.GetActiveViewRevision())
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
		SpaceId: "space", ViewId: viewID, ColumnName: datasetID + ".volume", OriginId: datasetID + ".volume",
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
	metadata.view.DesiredViewRevision = 3
	metadata.view.Columns = []*pb.ViewColumn{closeColumn, biasColumn, volumeColumn}
	if err := restarted.maintainView(ctx, MaintenanceOptions{Metadata: metadata}, restarted.internalAuth(), metadata.view); err != nil {
		t.Fatalf("maintainer recovery of an ahead physical schema: %v", err)
	}
	if metadata.extensions != 2 || metadata.claims != 0 || metadata.activated || metadata.view.GetActiveViewRevision() != 3 {
		t.Fatalf("maintainer did not recover same active index: extensions=%d claims=%d activated=%v revision=%d",
			metadata.extensions, metadata.claims, metadata.activated, metadata.view.GetActiveViewRevision())
	}
}
