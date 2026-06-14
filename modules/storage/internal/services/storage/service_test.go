package storage

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestServiceDataAndQueryView(t *testing.T) {
	svc := NewService(t.TempDir())
	slice := &pb.DataSlice{DatasetId: "binance_spot_kline_1m", SubjectId: "APT-USDT", Freq: "1m"}

	writeRsp, err := svc.WriteRows(context.Background(), &pb.WriteRowsReq{
		WriteMode: pb.WriteMode_WRITE_MODE_APPEND,
		Rows: []*pb.DataRow{{
			Slice:    slice,
			DataTime: "2026-01-01 00:00:00",
			Columns:  []*pb.ColumnValue{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 3.14}}}},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, writeRsp.GetRetInfo().GetCode())

	readRsp, err := svc.ReadRows(context.Background(), &pb.ReadRowsReq{
		Slice:       slice,
		ReadMode:    pb.ReadMode_READ_MODE_RANGE,
		TimeRange:   &pb.TimeRange{StartTime: "2026-01-01 00:00:00", StartInclusive: true},
		ColumnNames: []string{"close"},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, readRsp.GetRetInfo().GetCode())
	require.Len(t, readRsp.GetRows(), 1)

	_, err = svc.CreateDataView(context.Background(), &pb.CreateDataViewReq{
		DataView: &pb.DataView{
			WorkspaceId: "default",
			DataViewId:  "kline_close_view",
			Name:        "kline_close_view",
			QueryConfig: &pb.DataViewQueryConfig{PrimaryDatasetId: "binance_spot_kline_1m"},
		},
	})
	require.NoError(t, err)

	queryRsp, err := svc.QueryView(context.Background(), &pb.QueryViewReq{
		SpaceId:     "default",
		ViewId:      "kline_close_view",
		SubjectIds:  []string{"APT-USDT"},
		QueryTime:   &pb.QueryTime{TimeRange: &pb.TimeRange{StartTime: "2026-01-01 00:00:00", StartInclusive: true}},
		ColumnNames: []string{"close"},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, queryRsp.GetRetInfo().GetCode())
	require.Len(t, queryRsp.GetRows(), 1)
	require.Equal(t, "APT-USDT", queryRsp.GetRows()[0].GetSubjectId())
}

func TestServiceSearchRowsSupportsTextAndFilters(t *testing.T) {
	svc := NewService(t.TempDir())
	_, err := svc.WriteRows(context.Background(), &pb.WriteRowsReq{
		WriteMode: pb.WriteMode_WRITE_MODE_UPSERT,
		Rows: []*pb.DataRow{
			{
				Slice: &pb.DataSlice{DatasetId: "binance_spot_symbols", SubjectId: "APT-USDT"},
				RowId: "APT-USDT",
				Columns: []*pb.ColumnValue{
					stringColumn("symbol", "APTUSDT"),
					stringColumn("status", "active"),
					stringColumn("base_asset", "APT"),
				},
			},
			{
				Slice: &pb.DataSlice{DatasetId: "binance_spot_symbols", SubjectId: "AR-USDT"},
				RowId: "AR-USDT",
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
	require.Equal(t, "APT-USDT", searchRsp.GetRows()[0].GetSlice().GetSubjectId())
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
