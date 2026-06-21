package sqlite_test

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/metadata/sqlite"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestStoreInitializesStorageMetadataSchema(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "storage_metadata.db")
	schemaPath := storageMetadataSchemaPath(t)

	store, err := sqlite.Open(ctx, sqlite.Options{Path: dbPath, SchemaPath: schemaPath})
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.InitSchema(ctx))

	tables, err := store.TableNames(ctx)
	require.NoError(t, err)
	require.Contains(t, tables, "t_spaces")
	require.Contains(t, tables, "t_primary_store_nodes")
	require.Contains(t, tables, "t_storage_devices")
	require.Contains(t, tables, "t_primary_store_routes")
	oldStoragePrefix := "t_storage_"
	require.NotContains(t, tables, oldStoragePrefix+"nodes")
	require.NotContains(t, tables, oldStoragePrefix+"routes")
}

func TestStorePersistsMetadataRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	space, err := store.UpsertSpace(ctx, &pb.Space{SpaceId: "quant", Name: "量化空间", Owner: "tester"})
	require.NoError(t, err)
	require.Equal(t, "quant", space.GetSpaceId())
	gotSpace, err := store.GetSpace(ctx, "quant")
	require.NoError(t, err)
	require.Equal(t, "量化空间", gotSpace.GetName())
	spaces, _, err := store.ListSpaces(ctx, "tester", nil)
	require.NoError(t, err)
	require.Len(t, spaces, 1)

	source, err := store.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "quant", DataSourceId: "binance", Name: "币安", Kind: "exchange", Market: "crypto"})
	require.NoError(t, err)
	require.Equal(t, "binance", source.GetDataSourceId())
	gotSource, err := store.GetDataSource(ctx, "quant", "binance")
	require.NoError(t, err)
	require.Equal(t, "crypto", gotSource.GetMarket())

	subject, err := store.UpsertSubject(ctx, &pb.Subject{SpaceId: "quant", SubjectId: "APT-USDT", SubjectType: "crypto_pair", Name: "APT-USDT"})
	require.NoError(t, err)
	require.Equal(t, "APT-USDT", subject.GetSubjectId())
	gotSubject, err := store.GetSubject(ctx, "quant", "APT-USDT")
	require.NoError(t, err)
	require.Equal(t, "crypto_pair", gotSubject.GetSubjectType())
	_, err = store.UpsertSubjectSymbol(ctx, &pb.SubjectSymbol{SpaceId: "quant", SubjectId: "APT-USDT", DataSourceId: "binance", ExternalSymbol: "APTUSDT"})
	require.NoError(t, err)

	dataset, err := store.UpsertDataset(ctx, &pb.Dataset{SpaceId: "quant", DatasetId: "binance_spot_kline", DataSourceId: "binance", Name: "币安现货K线", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"1m", "1h"}})
	require.NoError(t, err)
	gotDataset, err := store.GetDataset(ctx, "quant", dataset.GetDatasetId())
	require.NoError(t, err)
	require.Equal(t, []string{"1m", "1h"}, gotDataset.GetFreqs())
	_, err = store.BindDatasetSubject(ctx, &pb.DatasetSubject{SpaceId: "quant", DatasetId: dataset.GetDatasetId(), SubjectId: "APT-USDT"})
	require.NoError(t, err)
	datasetSubjects, _, err := store.ListDatasetSubjects(ctx, "quant", dataset.GetDatasetId(), "", nil)
	require.NoError(t, err)
	require.Len(t, datasetSubjects, 1)

	field, err := store.UpsertField(ctx, &pb.Field{SpaceId: "quant", FieldId: "close", Name: "收盘价", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE})
	require.NoError(t, err)
	gotField, err := store.GetField(ctx, "quant", field.GetFieldId())
	require.NoError(t, err)
	require.Equal(t, pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, gotField.GetValueType())
	factor, err := store.UpsertFactor(ctx, &pb.Factor{SpaceId: "quant", FactorId: "ma20_close", Name: "MA20", Algorithm: "MA", ParamsJson: `{"window":20}`, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE})
	require.NoError(t, err)
	gotFactor, err := store.GetFactor(ctx, "quant", factor.GetFactorId())
	require.NoError(t, err)
	require.Equal(t, "MA", gotFactor.GetAlgorithm())

	_, err = store.UpsertDatasetColumn(ctx, &pb.DatasetColumn{SpaceId: "quant", DatasetId: dataset.GetDatasetId(), ColumnName: "close", OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, OriginId: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE})
	require.NoError(t, err)
	columns, _, err := store.ListDatasetColumns(ctx, "quant", dataset.GetDatasetId(), nil)
	require.NoError(t, err)
	require.Len(t, columns, 1)

	view, err := store.UpsertView(ctx, &pb.View{SpaceId: "quant", ViewId: "kline_view", Name: "K线视图", PrimaryDatasetId: dataset.GetDatasetId(), DatasetIds: []string{dataset.GetDatasetId()}, QueryWindow: "30d"})
	require.NoError(t, err)
	gotView, err := store.GetView(ctx, "quant", view.GetViewId())
	require.NoError(t, err)
	require.Equal(t, "30d", gotView.GetQueryWindow())
	_, err = store.UpsertViewColumn(ctx, &pb.ViewColumn{SpaceId: "quant", ViewId: view.GetViewId(), ColumnName: "close", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: dataset.GetDatasetId() + ".close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE})
	require.NoError(t, err)
	viewColumns, _, err := store.ListViewColumns(ctx, "quant", view.GetViewId(), nil)
	require.NoError(t, err)
	require.Len(t, viewColumns, 1)

	node, err := store.UpsertPrimaryStoreNode(ctx, &pb.PrimaryStoreNode{NodeId: "node-1", Name: "primary-1", Endpoint: "127.0.0.1:19001"})
	require.NoError(t, err)
	gotNode, err := store.GetPrimaryStoreNode(ctx, node.GetNodeId())
	require.NoError(t, err)
	require.Equal(t, "primary-1", gotNode.GetName())
	nodes, _, err := store.ListPrimaryStoreNodes(ctx, nil)
	require.NoError(t, err)
	require.Len(t, nodes, 1)

	device, err := store.UpsertDevice(ctx, &pb.Device{DeviceId: "pebble-1", NodeId: node.GetNodeId(), Name: "Pebble主存", Engine: "pebble", Endpoint: "/tmp/pebble"})
	require.NoError(t, err)
	gotDevice, err := store.GetDevice(ctx, device.GetDeviceId())
	require.NoError(t, err)
	require.Equal(t, "pebble", gotDevice.GetEngine())
	devices, _, err := store.ListDevices(ctx, node.GetNodeId(), "pebble", nil)
	require.NoError(t, err)
	require.Len(t, devices, 1)

	route, err := store.UpsertPrimaryStoreRoute(ctx, &pb.PrimaryStoreRoute{SpaceId: "quant", RouteId: "route-1", DatasetId: dataset.GetDatasetId(), NodeId: node.GetNodeId(), Priority: 10})
	require.NoError(t, err)
	gotRoute, err := store.GetPrimaryStoreRoute(ctx, "quant", route.GetRouteId())
	require.NoError(t, err)
	require.Equal(t, "node-1", gotRoute.GetNodeId())
	routes, _, err := store.ListPrimaryStoreRoutes(ctx, "quant", dataset.GetDatasetId(), "", node.GetNodeId(), nil)
	require.NoError(t, err)
	require.Len(t, routes, 1)

	archive, err := store.RegisterArchiveFile(ctx, &pb.ArchiveFile{SpaceId: "quant", ArchiveFileId: "archive-1", DatasetId: dataset.GetDatasetId(), DeviceId: device.GetDeviceId(), PartitionKey: "date=2026-06-17", FileUri: "file:///tmp/archive.parquet", Columns: []string{"close"}})
	require.NoError(t, err)
	require.Equal(t, "archive-1", archive.GetArchiveFileId())
	archives, _, err := store.ListArchiveFiles(ctx, "quant", dataset.GetDatasetId(), nil)
	require.NoError(t, err)
	require.Len(t, archives, 1)
}

func TestUpsertViewColumnMarksActiveViewPending(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	_, err := store.UpsertSpace(ctx, &pb.Space{SpaceId: "quant", Name: "量化空间"})
	require.NoError(t, err)
	_, err = store.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "quant", DataSourceId: "binance", Name: "币安", Kind: "exchange"})
	require.NoError(t, err)
	_, err = store.UpsertDataset(ctx, &pb.Dataset{SpaceId: "quant", DatasetId: "kline", DataSourceId: "binance", Name: "K线", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES})
	require.NoError(t, err)
	_, err = store.UpsertView(ctx, &pb.View{
		SpaceId:          "quant",
		ViewId:           "kline_view",
		Name:             "K线视图",
		PrimaryDatasetId: "kline",
		DatasetIds:       []string{"kline"},
		ActiveResult:     "view_result_quant_kline_v1",
		BuildStatus:      "active",
		Status:           "active",
	})
	require.NoError(t, err)

	_, err = store.UpsertViewColumn(ctx, &pb.ViewColumn{
		SpaceId:    "quant",
		ViewId:     "kline_view",
		ColumnName: "ma20",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId:   "kline.ma20",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	})
	require.NoError(t, err)

	view, err := store.GetView(ctx, "quant", "kline_view")
	require.NoError(t, err)
	require.Equal(t, "pending", view.GetBuildStatus())
	require.Equal(t, "view_result_quant_kline_v1", view.GetActiveResult())
	require.EqualValues(t, 2, view.GetViewVersion())

	_, err = store.UpsertViewColumn(ctx, &pb.ViewColumn{
		SpaceId:    "quant",
		ViewId:     "kline_view",
		ColumnName: "ma20",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId:   "kline.ma20",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	})
	require.NoError(t, err)
	view, err = store.GetView(ctx, "quant", "kline_view")
	require.NoError(t, err)
	require.EqualValues(t, 2, view.GetViewVersion(), "no-op ViewColumn upsert must not bump view_version")
}

func TestUpsertViewShapeChangeMarksActiveViewPendingAndKeepsActiveResult(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	_, err := store.UpsertSpace(ctx, &pb.Space{SpaceId: "quant", Name: "量化空间"})
	require.NoError(t, err)
	_, err = store.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "quant", DataSourceId: "binance", Name: "币安", Kind: "exchange"})
	require.NoError(t, err)
	_, err = store.UpsertDataset(ctx, &pb.Dataset{SpaceId: "quant", DatasetId: "kline", DataSourceId: "binance", Name: "K线", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES})
	require.NoError(t, err)
	_, err = store.UpsertView(ctx, &pb.View{
		SpaceId:          "quant",
		ViewId:           "kline_view",
		Name:             "K线视图",
		PrimaryDatasetId: "kline",
		DatasetIds:       []string{"kline"},
		QueryWindow:      "30d",
		ActiveResult:     "view_result_quant_kline_v1",
		BuildStatus:      "active",
		Status:           "active",
	})
	require.NoError(t, err)

	_, err = store.UpsertView(ctx, &pb.View{
		SpaceId:          "quant",
		ViewId:           "kline_view",
		Name:             "K线视图",
		PrimaryDatasetId: "kline",
		DatasetIds:       []string{"kline"},
		QueryWindow:      "60d",
		Status:           "active",
	})
	require.NoError(t, err)

	view, err := store.GetView(ctx, "quant", "kline_view")
	require.NoError(t, err)
	require.Equal(t, "pending", view.GetBuildStatus())
	require.Equal(t, "view_result_quant_kline_v1", view.GetActiveResult())
	require.Equal(t, "60d", view.GetQueryWindow())
	require.EqualValues(t, 2, view.GetViewVersion())
}

func TestViewBuildStateTransitionsVersionedResult(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	_, err := store.UpsertSpace(ctx, &pb.Space{SpaceId: "quant", Name: "量化空间"})
	require.NoError(t, err)
	_, err = store.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "quant", DataSourceId: "binance", Name: "币安", Kind: "exchange"})
	require.NoError(t, err)
	_, err = store.UpsertDataset(ctx, &pb.Dataset{SpaceId: "quant", DatasetId: "kline", DataSourceId: "binance", Name: "K线", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES})
	require.NoError(t, err)
	_, err = store.UpsertView(ctx, &pb.View{SpaceId: "quant", ViewId: "kline_view", Name: "K线视图", PrimaryDatasetId: "kline", DatasetIds: []string{"kline"}, Status: "active"})
	require.NoError(t, err)
	_, err = store.UpsertViewColumn(ctx, &pb.ViewColumn{
		SpaceId:    "quant",
		ViewId:     "kline_view",
		ColumnName: "close",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId:   "kline.close",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	})
	require.NoError(t, err)
	view, err := store.GetView(ctx, "quant", "kline_view")
	require.NoError(t, err)
	require.EqualValues(t, 2, view.GetViewVersion())

	building, err := store.BeginViewBuild(ctx, "quant", "kline_view", view.GetViewVersion(), "ts_view_quant_kline_view_v2_build")
	require.NoError(t, err)
	require.Equal(t, "building", building.GetBuildStatus())
	require.EqualValues(t, 2, building.GetBuildingViewVersion())
	require.Equal(t, "ts_view_quant_kline_view_v2_build", building.GetBuildingResult())
	require.NotEmpty(t, building.GetBuildStartedAt())

	require.NoError(t, store.CompleteViewBuild(ctx, "quant", "kline_view", view.GetViewVersion(), "ts_view_quant_kline_view_v2_build"))
	active, err := store.GetView(ctx, "quant", "kline_view")
	require.NoError(t, err)
	require.Equal(t, "active", active.GetBuildStatus())
	require.EqualValues(t, 2, active.GetActiveViewVersion())
	require.Equal(t, "ts_view_quant_kline_view_v2_build", active.GetActiveResult())
	require.Empty(t, active.GetBuildingResult())
	require.Zero(t, active.GetBuildingViewVersion())
}

func TestViewVersionChangeInvalidatesInFlightBuild(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	_, err := store.UpsertSpace(ctx, &pb.Space{SpaceId: "quant", Name: "量化空间"})
	require.NoError(t, err)
	_, err = store.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "quant", DataSourceId: "binance", Name: "币安", Kind: "exchange"})
	require.NoError(t, err)
	_, err = store.UpsertDataset(ctx, &pb.Dataset{SpaceId: "quant", DatasetId: "kline", DataSourceId: "binance", Name: "K线", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES})
	require.NoError(t, err)
	_, err = store.UpsertView(ctx, &pb.View{SpaceId: "quant", ViewId: "kline_view", Name: "K线视图", PrimaryDatasetId: "kline", DatasetIds: []string{"kline"}, QueryWindow: "30d", ActiveResult: "ts_view_v1", ActiveViewVersion: 1, BuildStatus: "active", Status: "active"})
	require.NoError(t, err)
	view, err := store.GetView(ctx, "quant", "kline_view")
	require.NoError(t, err)
	targetVersion := view.GetViewVersion()
	_, err = store.BeginViewBuild(ctx, "quant", "kline_view", targetVersion, "ts_view_building_v1")
	require.NoError(t, err)

	_, err = store.UpsertView(ctx, &pb.View{SpaceId: "quant", ViewId: "kline_view", Name: "K线视图", PrimaryDatasetId: "kline", DatasetIds: []string{"kline"}, QueryWindow: "60d", Status: "active"})
	require.NoError(t, err)
	changed, err := store.GetView(ctx, "quant", "kline_view")
	require.NoError(t, err)
	require.EqualValues(t, targetVersion+1, changed.GetViewVersion())
	require.Empty(t, changed.GetBuildingResult())
	require.Zero(t, changed.GetBuildingViewVersion())

	err = store.CompleteViewBuild(ctx, "quant", "kline_view", targetVersion, "ts_view_building_v1")
	require.Error(t, err)
	after, err := store.GetView(ctx, "quant", "kline_view")
	require.NoError(t, err)
	require.Equal(t, "ts_view_v1", after.GetActiveResult())
	require.EqualValues(t, 1, after.GetActiveViewVersion())
}

func openTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "storage_metadata.db")
	schemaPath := storageMetadataSchemaPath(t)
	store, err := sqlite.Open(ctx, sqlite.Options{Path: dbPath, SchemaPath: schemaPath})
	require.NoError(t, err)
	require.NoError(t, store.InitSchema(ctx))
	return store
}

func storageMetadataSchemaPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../schema/metadata.sql"))
}
