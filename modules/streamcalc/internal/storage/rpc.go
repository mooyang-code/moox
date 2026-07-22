package storage

import (
	"context"
	"fmt"
	"strings"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/modules/streamcalc/internal/aggregate"
	"trpc.group/trpc-go/trpc-go/client"
)

type RPCWriter struct {
	access    storagepb.PrimaryStoreClientProxy
	spaceID   string
	datasetID string
	authInfo  *storagepb.AuthInfo
}

func NewRPCWriter(target, spaceID, datasetID string, authInfo *storagepb.AuthInfo) (*RPCWriter, error) {
	if strings.TrimSpace(target) == "" || strings.TrimSpace(datasetID) == "" {
		return nil, fmt.Errorf("storage target and dataset_id are required")
	}
	return &RPCWriter{access: storagepb.NewPrimaryStoreClientProxy(client.WithTarget(target)), spaceID: spaceID, datasetID: datasetID, authInfo: authInfo}, nil
}

func (w *RPCWriter) Write(ctx context.Context, bar aggregate.Bar) error {
	if w == nil {
		return fmt.Errorf("storage writer is nil")
	}
	spaceID := bar.Key.SpaceID
	if spaceID == "" {
		spaceID = w.spaceID
	}
	if spaceID == "" {
		return fmt.Errorf("aggregated bar space_id is required")
	}
	row := &storagepb.TimeSeriesRow{
		Key: &storagepb.TimeSeriesKey{SpaceId: spaceID, DatasetId: w.datasetID, SubjectId: bar.Key.Subject, Freq: bar.Key.Frequency, DataTime: bar.Key.Start.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")},
		Columns: []*storagepb.ColumnValue{
			doubleField("open", bar.Open), doubleField("high", bar.High), doubleField("low", bar.Low), doubleField("close", bar.Close),
			doubleField("volume", bar.Volume), doubleField("quote_volume", bar.QuoteVolume), intField("trade_num", bar.TradeCount),
		},
	}
	rsp, err := w.access.MergeTimeSeriesRows(ctx, &storagepb.MergeTimeSeriesRowsReq{AuthInfo: w.authInfo, Rows: []*storagepb.TimeSeriesRow{row}})
	if err != nil {
		return fmt.Errorf("write aggregated kline: %w", err)
	}
	if rsp == nil || rsp.GetRetInfo() == nil || rsp.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
		if rsp == nil || rsp.GetRetInfo() == nil {
			return fmt.Errorf("write aggregated kline: empty ret_info")
		}
		return fmt.Errorf("write aggregated kline: %s", rsp.GetRetInfo().GetMsg())
	}
	return nil
}

func doubleField(name string, value float64) *storagepb.ColumnValue {
	return &storagepb.ColumnValue{ColumnName: name, ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: value}}}
}

func intField(name string, value int64) *storagepb.ColumnValue {
	return &storagepb.ColumnValue{ColumnName: name, ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_INT, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_IntValue{IntValue: value}}}
}
