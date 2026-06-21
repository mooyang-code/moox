package access

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/services/archive"
	"github.com/mooyang-code/moox/modules/storage/internal/services/view"
	"github.com/mooyang-code/moox/modules/storage/internal/testutil"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestStorageAcceptance(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	svc := newAccessTestServiceWithRootAndEvents(t, root, eventbus.NewMemoryBus())
	seedAcceptanceDataset(t, ctx, svc)

	writeRsp, err := svc.WriteTimeSeriesRows(ctx, &pb.WriteTimeSeriesRowsReq{Rows: []*pb.TimeSeriesRow{{
		Key: &pb.TimeSeriesKey{SpaceId: "crypto_acceptance", DatasetId: "binance_spot_kline_1m", SubjectId: "APT-USDT", Freq: "1m", DataTime: "2026-06-15T00:00:00Z"},
		Columns: []*pb.ColumnValue{
			testutil.DoubleValue("close", 8.1),
			testutil.StringValue("note", "acceptance row"),
		},
	}}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, writeRsp.GetRetInfo().GetCode())

	readRsp, err := svc.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
		Keys: []*pb.TimeSeriesKey{{SpaceId: "crypto_acceptance", DatasetId: "binance_spot_kline_1m", SubjectId: "APT-USDT", Freq: "1m"}},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, readRsp.GetRetInfo().GetCode())
	require.Len(t, readRsp.GetRows(), 1)

	_, err = svc.CreateView(ctx, &pb.CreateViewReq{View: &pb.View{SpaceId: "crypto_acceptance", ViewId: "kline_view", Name: "K线视图", PrimaryDatasetId: "binance_spot_kline_1m", DatasetIds: []string{"binance_spot_kline_1m"}, QueryWindow: "30d", Status: "active"}})
	require.NoError(t, err)
	_, err = svc.UpsertViewColumn(ctx, &pb.UpsertViewColumnReq{Column: &pb.ViewColumn{SpaceId: "crypto_acceptance", ViewId: "kline_view", ColumnName: "close", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "binance_spot_kline_1m.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}})
	require.NoError(t, err)
	viewStore, err := svc.viewStore()
	require.NoError(t, err)
	builder := view.NewBuilder(view.Options{
		Metadata: svc.metadata,
		Facts:    svc.primaryFactReader(),
		Views:    viewStore,
		Now:      func() time.Time { return time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC) },
	})
	_, err = builder.Build(ctx, "crypto_acceptance", "kline_view")
	require.NoError(t, err)
	queryRsp, err := svc.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{
		SpaceId: "crypto_acceptance",
		ViewId:  "kline_view",
		Keys:    []*pb.TimeSeriesKey{{SubjectId: "APT-USDT"}},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, queryRsp.GetRetInfo().GetCode())
	require.Len(t, queryRsp.GetRows(), 1)

	_, err = svc.CreatePrimaryStoreNode(ctx, &pb.CreatePrimaryStoreNodeReq{Node: &pb.PrimaryStoreNode{NodeId: "archive-node", Name: "archive-node", Status: "active"}})
	require.NoError(t, err)
	_, err = svc.CreateDevice(ctx, &pb.CreateDeviceReq{Device: &pb.Device{DeviceId: "archive-device", NodeId: "archive-node", Name: "archive", Engine: "parquet_archive", Status: "active"}})
	require.NoError(t, err)
	archiveSvc := archive.NewService(archive.Options{
		Metadata:    svc.metadata,
		Facts:       svc.primaryFactReader(),
		ArchiveRoot: filepath.Join(root, "archive"),
		DeviceID:    "archive-device",
		Now:         func() time.Time { return time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC) },
	})
	file, err := archiveSvc.ArchiveDataset(ctx, "crypto_acceptance", "binance_spot_kline_1m", "date=2026-06-15", nil)
	require.NoError(t, err)
	require.Equal(t, "parquet", file.GetFileFormat())
	require.Equal(t, uint64(2), file.GetRowCount())
}

func seedAcceptanceDataset(t *testing.T, ctx context.Context, svc *Service) {
	t.Helper()
	_, err := svc.metadata.UpsertSpace(ctx, &pb.Space{SpaceId: "crypto_acceptance", Name: "crypto_acceptance"})
	require.NoError(t, err)
	_, err = svc.metadata.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "crypto_acceptance", DataSourceId: "binance", Name: "Binance", Kind: "exchange"})
	require.NoError(t, err)
	_, err = svc.metadata.UpsertDataset(ctx, &pb.Dataset{SpaceId: "crypto_acceptance", DatasetId: "binance_spot_kline_1m", DataSourceId: "binance", Name: "Binance 现货 K 线", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"1m"}, Status: "active"})
	require.NoError(t, err)
	_, err = svc.metadata.UpsertSubject(ctx, &pb.Subject{SpaceId: "crypto_acceptance", SubjectId: "APT-USDT", SubjectType: "crypto_pair", Name: "APT-USDT", Status: "active"})
	require.NoError(t, err)
	_, err = svc.metadata.BindDatasetSubject(ctx, &pb.DatasetSubject{SpaceId: "crypto_acceptance", DatasetId: "binance_spot_kline_1m", SubjectId: "APT-USDT", Status: "active"})
	require.NoError(t, err)
	_, err = svc.metadata.UpsertDatasetColumn(ctx, &pb.DatasetColumn{SpaceId: "crypto_acceptance", DatasetId: "binance_spot_kline_1m", ColumnName: "close", OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, OriginId: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active"})
	require.NoError(t, err)
	_, err = svc.metadata.UpsertDatasetColumn(ctx, &pb.DatasetColumn{SpaceId: "crypto_acceptance", DatasetId: "binance_spot_kline_1m", ColumnName: "note", OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, OriginId: "note", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, Status: "active"})
	require.NoError(t, err)
	_, err = svc.metadata.UpsertPrimaryStoreRoute(ctx, &pb.PrimaryStoreRoute{SpaceId: "crypto_acceptance", RouteId: "acceptance-route", DatasetId: "binance_spot_kline_1m", SubjectPattern: "*", NodeId: "node-1", Status: "active"})
	require.NoError(t, err)
}
