//go:build cgo

package view

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	"github.com/mooyang-code/moox/modules/storage/internal/serviceauth"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

const (
	extensionDatasetID = "dataset_factor_btc_1m"
	extensionViewID    = "view_factor_btc_1m"
)

func extensionColumn(name string) *pb.ViewColumn {
	return &pb.ViewColumn{
		SpaceId: "space", ViewId: extensionViewID, ColumnName: name, OriginId: name,
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	}
}

func closeServiceEngines(service *Service) {
	for _, engine := range service.engines {
		if closer, ok := engine.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}
}

// prepareBuiltExtensionIndex 在 DuckDB 里建好一个只有 close 列的索引：写一行，并记下跟随进度（建好的标志）。
func prepareBuiltExtensionIndex(t *testing.T, ctx context.Context, svc *Service) string {
	t.Helper()
	engine := svc.engines["duckdb"]
	if engine == nil {
		t.Fatal("DuckDB engine is unavailable")
	}
	indexID := viewindex.ViewIndexID("space", extensionViewID)
	schema := viewindex.ViewIndexSchema{
		SpaceID: "space", ViewID: extensionViewID, PrimaryDatasetID: extensionDatasetID, ViewVersion: 1,
		Engine: "duckdb", Columns: numberedViewColumns([]*pb.ViewColumn{extensionColumn("close")}),
	}
	schema.SchemaHash = viewindex.HashViewIndexSchema(schema)
	if err := engine.Prepare(ctx, indexID, schema); err != nil {
		t.Fatal(err)
	}
	rowKey := &pb.RowKey{SpaceId: "space", DatasetId: extensionDatasetID, Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
		SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-10-04T00:00:00Z",
	}}}
	if err := engine.Write(ctx, indexID, viewindex.ViewIndexWriteBatch{
		ViewRevision: 1, ViewSchemaHash: schema.SchemaHash, WriteMode: viewindex.LiveWrite,
		RowWrites: []viewindex.RowWrite{{Key: viewindex.RowKey{Key: rowKey}, Fields: []*pb.FieldValue{{
			FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 10}},
		}}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.(viewindex.FollowStateWriter).SaveFollowState(ctx, indexID, viewindex.FollowState{Cursor: viewindex.ChangeCursor{StoreID: "store", Sequence: 1}, BuiltSequence: 1}); err != nil {
		t.Fatal(err)
	}
	return indexID
}

func extensionView(columns ...string) *pb.View {
	view := &pb.View{SpaceId: "space", ViewId: extensionViewID, DatasetId: extensionDatasetID, Engine: "duckdb", Status: "active", Freq: "1m"}
	for _, name := range columns {
		view.Columns = append(view.Columns, extensionColumn(name))
	}
	return view
}

// 数据集加了一列（元数据里 View 的列随之多一列）：定时任务原地给索引加列，不重建、不需要任何 View 侧的调用；
// GetViewState 返回的已建列和查询都能看到新列，已有行的新列为空。
func TestDatasetColumnAddExtendsIndexWithoutRebuild(t *testing.T) {
	ctx := context.Background()
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer closeServiceEngines(svc)
	indexID := prepareBuiltExtensionIndex(t, ctx, svc)
	metadata := &maintenanceMetadata{view: extensionView("close")}
	if err := svc.RestoreActiveViews(ctx, MaintenanceOptions{Metadata: metadata}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	metadata.view = extensionView("close", "bias_20")
	svc.setMetadataClient(metadata)
	svc.extendViewSchemasRound(ctx)
	svc.extendViewSchemasRound(ctx)

	engine := svc.engines["duckdb"]
	stats, err := engine.(viewindex.MetadataStatReader).StatMetadata(ctx, indexID)
	if err != nil || stats.ViewVersion != 2 || len(stats.Columns) != 2 || stats.Columns[1].GetColumnName() != "bias_20" || stats.Columns[1].GetSortOrder() != 1 {
		t.Fatalf("索引文件记下的契约 = %#v err=%v，想要修订号 2、两列", stats, err)
	}
	if schema := svc.schemas[indexID]; schema.ViewVersion != 2 || schema.SchemaHash != stats.SchemaHash {
		t.Fatalf("内存契约 = %+v，应与索引文件一致", schema)
	}
	auth := &pb.AuthInfo{AppId: "caller", AppKey: serviceauth.Key("view-secret", "caller")}
	state, err := svc.GetViewState(ctx, &pb.GetViewStateReq{AuthInfo: auth, SpaceId: "space", ViewId: extensionViewID})
	if err != nil || state.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("GetViewState = %v err=%v", state, err)
	}
	if state.GetIndexId() != indexID || state.GetViewRevision() != 2 || len(state.GetColumns()) != 2 || state.GetColumns()[1].GetColumnName() != "bias_20" {
		t.Fatalf("GetViewState 应返回新列：%v", state)
	}
	rows, _, err := engine.Query(ctx, indexID, viewindex.QuerySpec{
		Selectors: []viewindex.TimeSeriesSelector{{SpaceID: "space", DatasetID: extensionDatasetID, SubjectID: "BTC-USDT", Freq: "1m"}},
		Includes:  []string{"bias_20"},
	})
	if err != nil || len(rows) != 1 || len(rows[0].GetFields()) != 1 || rows[0].GetFields()[0].GetValue().GetNullValue() != pb.NullValue_NULL_VALUE_NULL {
		t.Fatalf("加列后的已有行 = %v err=%v，想要 bias_20 为空", rows, err)
	}
}

// 重启后写入契约只从索引文件还原：加过列的索引照常挂接，之后还能接着加列。
func TestRestartRestoresContractFromIndexFile(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "views")
	first, err := New(root, "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	indexID := prepareBuiltExtensionIndex(t, ctx, first)
	metadata := &maintenanceMetadata{view: extensionView("close")}
	if err := first.RestoreActiveViews(ctx, MaintenanceOptions{Metadata: metadata}); err != nil {
		t.Fatal(err)
	}
	metadata.view = extensionView("close", "bias_20")
	first.setMetadataClient(metadata)
	first.extendViewSchemasRound(ctx)
	extended := first.schemas[indexID]
	closeServiceEngines(first)

	restarted, err := New(root, "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer closeServiceEngines(restarted)
	if err := restarted.RestoreActiveViews(ctx, MaintenanceOptions{Metadata: metadata}); err != nil {
		t.Fatalf("restore after restart: %v", err)
	}
	restored := restarted.schemas[indexID]
	if restored.ViewVersion != 2 || restored.SchemaHash != extended.SchemaHash || viewindex.HashViewIndexSchema(restored) != extended.SchemaHash {
		t.Fatalf("重启后的契约 = %+v，想要与加列后一致（%s）", restored, extended.SchemaHash)
	}
	metadata.view = extensionView("close", "bias_20", "volume")
	restarted.setMetadataClient(metadata)
	restarted.extendViewSchemasRound(ctx)
	stats, err := restarted.engines["duckdb"].(viewindex.MetadataStatReader).StatMetadata(ctx, indexID)
	if err != nil || stats.ViewVersion != 3 || len(stats.Columns) != 3 {
		t.Fatalf("重启后接着加列 = %#v err=%v", stats, err)
	}
}
