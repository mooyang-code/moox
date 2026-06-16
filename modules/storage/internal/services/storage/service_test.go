package storage

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestServiceDataAndQueryView(t *testing.T) {
	svc := NewService(t.TempDir())
	scope := &pb.DataScope{SpaceId: "default", DatasetId: "binance_spot_kline_1m", SubjectId: "APT-USDT", Freq: "1m"}

	writeRsp, err := svc.WriteRows(context.Background(), &pb.WriteRowsReq{
		WriteMode: pb.WriteMode_WRITE_MODE_APPEND,
		Rows: []*pb.DataRow{{
			Key:     &pb.DataKey{Scope: scope, DataTime: "2026-01-01 00:00:00"},
			Columns: []*pb.ColumnValue{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 3.14}}}},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, writeRsp.GetRetInfo().GetCode())

	readRsp, err := svc.ReadRows(context.Background(), &pb.ReadRowsReq{
		Scope:       scope,
		ReadMode:    pb.ReadMode_READ_MODE_RANGE,
		TimeRange:   &pb.TimeRange{StartTime: "2026-01-01 00:00:00", StartInclusive: true},
		ColumnNames: []string{"close"},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, readRsp.GetRetInfo().GetCode())
	require.Len(t, readRsp.GetRows(), 1)

	spaceRsp, err := svc.CreateSpace(context.Background(), &pb.CreateSpaceReq{Space: &pb.Space{SpaceId: "default", Name: "default"}})
	require.NoError(t, err)

	viewRsp, err := svc.CreateView(context.Background(), &pb.CreateViewReq{
		View: &pb.View{
			ViewId:     "kline_close_view",
			SpaceId:    spaceRsp.GetSpace().GetSpaceId(),
			Name:       "kline_close_view",
			DatasetIds: []string{"binance_spot_kline_1m"},
		},
	})
	require.NoError(t, err)

	queryRsp, err := svc.QueryView(context.Background(), &pb.QueryViewReq{
		SpaceId:     "default",
		ViewId:      viewRsp.GetView().GetViewId(),
		SubjectIds:  []string{"APT-USDT"},
		QueryTime:   &pb.QueryTime{TimeRange: &pb.TimeRange{StartTime: "2026-01-01 00:00:00", StartInclusive: true}},
		ColumnNames: []string{"close"},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, queryRsp.GetRetInfo().GetCode())
	require.Len(t, queryRsp.GetRows(), 1)
	require.Equal(t, "APT-USDT", queryRsp.GetRows()[0].GetSubjectId())
}

func TestServiceMetadataUsesNewModel(t *testing.T) {
	svc := NewService(t.TempDir())

	spaceRsp, err := svc.CreateSpace(context.Background(), &pb.CreateSpaceReq{Space: &pb.Space{Name: "research"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, spaceRsp.GetRetInfo().GetCode())
	require.NotEmpty(t, spaceRsp.GetSpace().GetSpaceId())

	viewRsp, err := svc.CreateView(context.Background(), &pb.CreateViewReq{View: &pb.View{SpaceId: spaceRsp.GetSpace().GetSpaceId(), Name: "kline_factor_view", DatasetIds: []string{"binance_spot_kline"}}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, viewRsp.GetRetInfo().GetCode())

	sourceRsp, err := svc.CreateDataSource(context.Background(), &pb.CreateDataSourceReq{DataSource: &pb.DataSource{SpaceId: spaceRsp.GetSpace().GetSpaceId(), Name: "binance", Kind: "exchange", Market: "crypto"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, sourceRsp.GetRetInfo().GetCode())

	subjectRsp, err := svc.UpsertSubject(context.Background(), &pb.UpsertSubjectReq{Subject: &pb.Subject{SpaceId: spaceRsp.GetSpace().GetSpaceId(), SubjectId: "APT-USDT", Name: "APT-USDT", SubjectType: "crypto_pair"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, subjectRsp.GetRetInfo().GetCode())

	symbolRsp, err := svc.UpsertSubjectSymbol(context.Background(), &pb.UpsertSubjectSymbolReq{SubjectSymbol: &pb.SubjectSymbol{SpaceId: spaceRsp.GetSpace().GetSpaceId(), SubjectId: subjectRsp.GetSubject().GetSubjectId(), DataSourceId: sourceRsp.GetDataSource().GetDataSourceId(), ExternalSymbol: "APTUSDT"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, symbolRsp.GetRetInfo().GetCode())

	datasetRsp, err := svc.CreateDataSet(context.Background(), &pb.CreateDataSetReq{Dataset: &pb.DataSet{SpaceId: spaceRsp.GetSpace().GetSpaceId(), DataSourceId: sourceRsp.GetDataSource().GetDataSourceId(), Name: "binance_spot_kline", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"1m", "1h"}}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, datasetRsp.GetRetInfo().GetCode())

	bindSubjectRsp, err := svc.BindDataSetSubject(context.Background(), &pb.BindDataSetSubjectReq{DatasetSubject: &pb.DataSetSubject{SpaceId: spaceRsp.GetSpace().GetSpaceId(), DatasetId: datasetRsp.GetDataset().GetDatasetId(), SubjectId: subjectRsp.GetSubject().GetSubjectId()}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, bindSubjectRsp.GetRetInfo().GetCode())

	fieldRsp, err := svc.CreateField(context.Background(), &pb.CreateFieldReq{Field: &pb.Field{SpaceId: spaceRsp.GetSpace().GetSpaceId(), FieldId: "close", Name: "收盘价", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, fieldRsp.GetRetInfo().GetCode())

	columnRsp, err := svc.UpsertDataSetColumn(context.Background(), &pb.UpsertDataSetColumnReq{Column: &pb.DataSetColumn{SpaceId: spaceRsp.GetSpace().GetSpaceId(), DatasetId: datasetRsp.GetDataset().GetDatasetId(), ColumnName: "close", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_FIELD, OriginId: fieldRsp.GetField().GetFieldId(), ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, columnRsp.GetRetInfo().GetCode())

	factorRsp, err := svc.CreateFactor(context.Background(), &pb.CreateFactorReq{Factor: &pb.Factor{SpaceId: spaceRsp.GetSpace().GetSpaceId(), FactorId: "ma20_close", Name: "MA20 收盘均线", Algorithm: "MA", ParamsJson: `{"window":20,"price":"close"}`, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, factorRsp.GetRetInfo().GetCode())

	nodeRsp, err := svc.CreateStorageNode(context.Background(), &pb.CreateStorageNodeReq{Node: &pb.StorageNode{Name: "adapter-1", Endpoint: "127.0.0.1:19001"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, nodeRsp.GetRetInfo().GetCode())

	deviceRsp, err := svc.CreateDevice(context.Background(), &pb.CreateDeviceReq{Device: &pb.Device{Name: "pebble-1", NodeId: nodeRsp.GetNode().GetNodeId(), Engine: "pebble", Endpoint: "/tmp/pebble"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, deviceRsp.GetRetInfo().GetCode())

	routeRsp, err := svc.CreateStorageRoute(context.Background(), &pb.CreateStorageRouteReq{StorageRoute: &pb.StorageRoute{SpaceId: spaceRsp.GetSpace().GetSpaceId(), DatasetId: datasetRsp.GetDataset().GetDatasetId(), NodeId: nodeRsp.GetNode().GetNodeId()}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, routeRsp.GetRetInfo().GetCode())

	archiveRsp, err := svc.RegisterArchiveFile(context.Background(), &pb.RegisterArchiveFileReq{ArchiveFile: &pb.ArchiveFile{SpaceId: spaceRsp.GetSpace().GetSpaceId(), DatasetId: datasetRsp.GetDataset().GetDatasetId(), DeviceId: deviceRsp.GetDevice().GetDeviceId(), PartitionKey: "date=2026-06-15", FileUri: "file:///archive/date=2026-06-15/part-000.parquet", Columns: []string{"close"}}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, archiveRsp.GetRetInfo().GetCode())

	listColumnsRsp, err := svc.ListDataSetColumns(context.Background(), &pb.ListDataSetColumnsReq{SpaceId: spaceRsp.GetSpace().GetSpaceId(), DatasetId: datasetRsp.GetDataset().GetDatasetId()})
	require.NoError(t, err)
	require.Len(t, listColumnsRsp.GetColumns(), 1)

	listArchiveRsp, err := svc.ListArchiveFiles(context.Background(), &pb.ListArchiveFilesReq{SpaceId: spaceRsp.GetSpace().GetSpaceId(), DatasetId: datasetRsp.GetDataset().GetDatasetId()})
	require.NoError(t, err)
	require.Len(t, listArchiveRsp.GetArchiveFiles(), 1)
}

func TestServiceSearchRowsSupportsTextAndFilters(t *testing.T) {
	svc := NewService(t.TempDir())
	_, err := svc.WriteRows(context.Background(), &pb.WriteRowsReq{
		WriteMode: pb.WriteMode_WRITE_MODE_UPSERT,
		Rows: []*pb.DataRow{
			{
				Key: &pb.DataKey{Scope: &pb.DataScope{SpaceId: "default", DatasetId: "binance_spot_symbols", SubjectId: "APT-USDT"}, RowId: "APT-USDT"},
				Columns: []*pb.ColumnValue{
					stringColumn("symbol", "APTUSDT"),
					stringColumn("status", "active"),
					stringColumn("base_asset", "APT"),
				},
			},
			{
				Key: &pb.DataKey{Scope: &pb.DataScope{SpaceId: "default", DatasetId: "binance_spot_symbols", SubjectId: "AR-USDT"}, RowId: "AR-USDT"},
				Columns: []*pb.ColumnValue{
					stringColumn("symbol", "ARUSDT"),
					stringColumn("status", "inactive"),
					stringColumn("base_asset", "AR"),
				},
			},
		},
	})
	require.NoError(t, err)

	searchRsp, err := svc.SearchRows(context.Background(), &pb.SearchRowsReq{
		SpaceId:     "default",
		DatasetId:   "binance_spot_symbols",
		TextQuery:   "USDT",
		ColumnNames: []string{"symbol", "status"},
		Filters: []*pb.FilterExpr{{
			Expr: "status == $status",
			Args: map[string]*pb.TypedValue{
				"status": {Value: &pb.TypedValue_StringValue{StringValue: "active"}},
			},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, searchRsp.GetRetInfo().GetCode())
	require.Len(t, searchRsp.GetRows(), 1)
	require.Equal(t, "APT-USDT", searchRsp.GetRows()[0].GetKey().GetScope().GetSubjectId())
	require.Len(t, searchRsp.GetRows()[0].GetColumns(), 2)
	require.Equal(t, "symbol", searchRsp.GetRows()[0].GetColumns()[0].GetColumnName())
	require.Equal(t, "status", searchRsp.GetRows()[0].GetColumns()[1].GetColumnName())
}

func stringColumn(name, value string) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: value}},
	}
}
