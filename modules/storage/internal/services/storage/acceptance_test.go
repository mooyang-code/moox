package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/services/archive"
	"github.com/mooyang-code/moox/modules/storage/internal/services/materializer"
	"github.com/mooyang-code/moox/modules/storage/pkg/quantstore"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestStorageAcceptance(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	svc := NewService(root)

	_, err := svc.CreateSpace(ctx, &pb.CreateSpaceReq{Space: &pb.Space{SpaceId: "crypto_acceptance", Name: "crypto_acceptance"}})
	require.NoError(t, err)
	_, err = svc.CreateDataSource(ctx, &pb.CreateDataSourceReq{DataSource: &pb.DataSource{SpaceId: "crypto_acceptance", DataSourceId: "binance", Name: "Binance", Kind: "exchange"}})
	require.NoError(t, err)
	_, err = svc.UpsertSubject(ctx, &pb.UpsertSubjectReq{Subject: &pb.Subject{SpaceId: "crypto_acceptance", SubjectId: "APT-USDT", SubjectType: "crypto_pair", Name: "APT-USDT"}})
	require.NoError(t, err)
	_, err = svc.UpsertSubjectSymbol(ctx, &pb.UpsertSubjectSymbolReq{SubjectSymbol: &pb.SubjectSymbol{SpaceId: "crypto_acceptance", SubjectId: "APT-USDT", DataSourceId: "binance", ExternalSymbol: "APTUSDT"}})
	require.NoError(t, err)
	_, err = svc.CreateDataSet(ctx, &pb.CreateDataSetReq{Dataset: &pb.DataSet{
		SpaceId:      "crypto_acceptance",
		DatasetId:    "binance_spot_kline_1m",
		DataSourceId: "binance",
		Name:         "Binance 现货 K 线",
		DataKind:     pb.DataKind_DATA_KIND_TIME_SERIES,
		Freqs:        []string{"1m"},
		Status:       "active",
	}})
	require.NoError(t, err)
	_, err = svc.BindDataSetSubject(ctx, &pb.BindDataSetSubjectReq{DatasetSubject: &pb.DataSetSubject{SpaceId: "crypto_acceptance", DatasetId: "binance_spot_kline_1m", SubjectId: "APT-USDT"}})
	require.NoError(t, err)
	_, err = svc.CreateField(ctx, &pb.CreateFieldReq{Field: &pb.Field{SpaceId: "crypto_acceptance", FieldId: "close", Name: "收盘价", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}})
	require.NoError(t, err)
	_, err = svc.CreateField(ctx, &pb.CreateFieldReq{Field: &pb.Field{SpaceId: "crypto_acceptance", FieldId: "note", Name: "说明", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING}})
	require.NoError(t, err)
	_, err = svc.UpsertDataSetColumn(ctx, &pb.UpsertDataSetColumnReq{Column: &pb.DataSetColumn{SpaceId: "crypto_acceptance", DatasetId: "binance_spot_kline_1m", ColumnName: "close", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_FIELD, OriginId: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active"}})
	require.NoError(t, err)
	_, err = svc.UpsertDataSetColumn(ctx, &pb.UpsertDataSetColumnReq{Column: &pb.DataSetColumn{SpaceId: "crypto_acceptance", DatasetId: "binance_spot_kline_1m", ColumnName: "note", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_FIELD, OriginId: "note", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, TextIndexed: true, Status: "active"}})
	require.NoError(t, err)
	seedRoute(t, svc, "crypto_acceptance", "binance_spot_kline_1m")

	writeRsp, err := svc.WriteRows(ctx, &pb.WriteRowsReq{WriteMode: pb.WriteMode_WRITE_MODE_UPSERT, Rows: []*pb.DataRow{{
		Key: &pb.DataKey{
			Scope:    &pb.DataScope{SpaceId: "crypto_acceptance", DatasetId: "binance_spot_kline_1m", SubjectId: "APT-USDT", Freq: "1m"},
			DataTime: "2026-06-15T00:00:00Z",
		},
		Columns: []*pb.ColumnValue{
			quantstore.DoubleValue("close", 8.1),
			quantstore.StringValue("note", "acceptance row"),
		},
	}}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, writeRsp.GetRetInfo().GetCode())

	readRsp, err := svc.ReadRows(ctx, &pb.ReadRowsReq{Scope: &pb.DataScope{SpaceId: "crypto_acceptance", DatasetId: "binance_spot_kline_1m", SubjectId: "APT-USDT", Freq: "1m"}, ReadMode: pb.ReadMode_READ_MODE_RANGE})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, readRsp.GetRetInfo().GetCode())
	require.Len(t, readRsp.GetRows(), 1)

	searchRsp, err := svc.SearchRows(ctx, &pb.SearchRowsReq{SpaceId: "crypto_acceptance", DatasetId: "binance_spot_kline_1m", TextQuery: "acceptance"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, searchRsp.GetRetInfo().GetCode())
	require.Len(t, searchRsp.GetRows(), 1)

	_, err = svc.CreateView(ctx, &pb.CreateViewReq{View: &pb.View{SpaceId: "crypto_acceptance", ViewId: "kline_view", Name: "K线视图", PrimaryDatasetId: "binance_spot_kline_1m", DatasetIds: []string{"binance_spot_kline_1m"}, QueryWindow: "30d", Status: "active"}})
	require.NoError(t, err)
	_, err = svc.UpsertViewColumn(ctx, &pb.UpsertViewColumnReq{Column: &pb.ViewColumn{SpaceId: "crypto_acceptance", ViewId: "kline_view", ColumnName: "close", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "binance_spot_kline_1m.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}})
	require.NoError(t, err)
	viewStore, err := svc.viewStore()
	require.NoError(t, err)
	builder := materializer.NewBuilder(materializer.Options{
		Metadata: svc.metadata,
		Facts:    svc.store,
		Views:    viewStore,
		Now: func() time.Time {
			return time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC)
		},
	})
	_, err = builder.Build(ctx, "crypto_acceptance", "kline_view")
	require.NoError(t, err)
	queryRsp, err := svc.QueryView(ctx, &pb.QueryViewReq{SpaceId: "crypto_acceptance", ViewId: "kline_view", SubjectIds: []string{"APT-USDT"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, queryRsp.GetRetInfo().GetCode())
	require.Len(t, queryRsp.GetRows(), 1)

	_, err = svc.CreateStorageNode(ctx, &pb.CreateStorageNodeReq{Node: &pb.StorageNode{NodeId: "archive-node", Name: "archive-node", Status: "active"}})
	require.NoError(t, err)
	_, err = svc.CreateDevice(ctx, &pb.CreateDeviceReq{Device: &pb.Device{DeviceId: "archive-device", NodeId: "archive-node", Name: "archive", Engine: "parquet_archive", Status: "active"}})
	require.NoError(t, err)
	archiveSvc := archive.NewService(archive.Options{
		Metadata:    svc.metadata,
		Facts:       svc.store,
		ArchiveRoot: filepath.Join(root, "archive"),
		DeviceID:    "archive-device",
		Now: func() time.Time {
			return time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC)
		},
	})
	file, err := archiveSvc.ArchiveDataSet(ctx, "crypto_acceptance", "binance_spot_kline_1m", "date=2026-06-15", nil)
	require.NoError(t, err)
	require.Equal(t, "parquet", file.GetFileFormat())
	require.Equal(t, uint64(2), file.GetRowCount())
}
