package v2

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/genv2"
	"github.com/stretchr/testify/require"
)

func TestServiceDataAndQueryFrame(t *testing.T) {
	svc := NewService(t.TempDir())
	ref := &pb.DataRef{WorkspaceId: "default", DatasetId: "binance_spot_kline_1m", InstrumentId: "APT-USDT", ExchangeId: "BINANCE", Freq: "1m"}

	setRsp, err := svc.SetTimeSeries(context.Background(), &pb.SetTimeSeriesReq{
		WriteMode: pb.WriteMode_WRITE_MODE_APPEND,
		Points: []*pb.TimeSeriesPoint{{
			DataRef: ref,
			Time:    "2026-01-01 00:00:00",
			Fields:  []*pb.FieldValue{{FieldName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 3.14}}}},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, setRsp.GetRetInfo().GetCode())

	queryRsp, err := svc.QueryFrame(context.Background(), &pb.QueryFrameReq{
		WorkspaceId:   "default",
		InstrumentIds: []string{"APT-USDT"},
		QueryTime:     &pb.QueryTime{TimeRange: &pb.TimeRange{StartTime: "2026-01-01 00:00:00", StartInclusive: true}},
		Columns: []*pb.QueryFrameColumn{{
			ColumnId:     "close",
			OutputName:   "close",
			ColumnOrigin: pb.ColumnOrigin_COLUMN_ORIGIN_FIELD,
			DatasetId:    "binance_spot_kline_1m",
			FieldId:      "close",
			ValueType:    pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		}},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, queryRsp.GetRetInfo().GetCode())
	require.Len(t, queryRsp.GetRows(), 1)
	require.Equal(t, "APT-USDT", queryRsp.GetRows()[0].GetInstrumentId())
}
