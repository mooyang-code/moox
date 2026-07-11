package storageio

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func (c *Client) WriteProviderKlines(ctx context.Context, datasetID string, rows []marketdata.ProviderKline) error {
	binding, err := c.binding(datasetID, RoleProviderData)
	if err != nil {
		return err
	}
	if binding.Feed != "kline" {
		return fmt.Errorf("dataset %q is not a kline dataset", datasetID)
	}
	converted := make([]*storagepb.TimeSeriesRow, 0, len(rows))
	for _, row := range rows {
		if err := row.Validate(); err != nil {
			return err
		}
		if binding.RequiredVolume && row.Volume == nil {
			return fmt.Errorf("dataset %q requires volume", datasetID)
		}
		if binding.VolumeUnit != "" && row.VolumeUnit != binding.VolumeUnit {
			return fmt.Errorf("volume unit %q does not match %q", row.VolumeUnit, binding.VolumeUnit)
		}
		if binding.AmountUnit != "" && row.AmountUnit != binding.AmountUnit {
			return fmt.Errorf("amount unit %q does not match %q", row.AmountUnit, binding.AmountUnit)
		}
		convertedRow, err := providerRow(binding, row)
		if err != nil {
			return err
		}
		converted = append(converted, convertedRow)
	}
	rsp, err := c.access.WriteTimeSeriesRows(ctx, &storagepb.WriteTimeSeriesRowsReq{AuthInfo: c.auth, Rows: converted, WriteMode: storagepb.RowWriteMode_ROW_WRITE_MODE_REPLACE})
	if err != nil {
		return fmt.Errorf("write provider kline: %w", err)
	}
	return ensureOK("write provider kline", rsp.GetRetInfo())
}

func providerRow(binding Binding, row marketdata.ProviderKline) (*storagepb.TimeSeriesRow, error) {
	columns, err := klineColumns(row)
	if err != nil {
		return nil, err
	}
	return &storagepb.TimeSeriesRow{Key: &storagepb.TimeSeriesKey{SpaceId: binding.SpaceID, DatasetId: binding.DatasetID, SubjectId: row.SubjectID, Freq: string(row.Frequency), DataTime: row.DataTime.Format(time.RFC3339)}, Columns: columns, Attributes: map[string]string{"provider_id": string(row.ProviderID), "provider_symbol": row.ProviderSymbol}}, nil
}
func klineColumns(row marketdata.ProviderKline) ([]*storagepb.ColumnValue, error) {
	columns := make([]*storagepb.ColumnValue, 0, 24)
	for _, value := range []struct {
		name  string
		value marketdata.Decimal
	}{{"open", row.Open}, {"high", row.High}, {"low", row.Low}, {"close", row.Close}} {
		pair, err := decimalColumns(value.name, value.value)
		if err != nil {
			return nil, err
		}
		columns = append(columns, pair...)
	}
	columns = append(columns, stringColumn("trade_date", row.TradeDate), timeColumn("close_time", row.CloseTime), stringColumn("feed_scope", row.FeedScope), stringColumn("volume_unit", row.VolumeUnit), stringColumn("amount_unit", row.AmountUnit), timeColumn("provider_timestamp", row.ProviderTimestamp), timeColumn("fetched_at", row.FetchedAt), stringColumn("request_id", row.RequestID), boolColumn("is_closed", row.Closed))
	if row.Volume != nil {
		pair, err := decimalColumns("volume", *row.Volume)
		if err != nil {
			return nil, err
		}
		columns = append(columns, pair...)
	}
	if row.Amount != nil {
		pair, err := decimalColumns("amount", *row.Amount)
		if err != nil {
			return nil, err
		}
		columns = append(columns, pair...)
	}
	return columns, nil
}
func decimalColumns(name string, value marketdata.Decimal) ([]*storagepb.ColumnValue, error) {
	numeric, err := strconv.ParseFloat(value.String(), 64)
	if err != nil || math.IsNaN(numeric) || math.IsInf(numeric, 0) {
		return nil, fmt.Errorf("decimal %q cannot be represented as finite double", value.String())
	}
	return []*storagepb.ColumnValue{{ColumnName: name, ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: numeric}}}, stringColumn(name+"_exact", value.String())}, nil
}
func stringColumn(name, value string) *storagepb.ColumnValue {
	return &storagepb.ColumnValue{ColumnName: name, ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_StringValue{StringValue: value}}}
}
func timeColumn(name string, value time.Time) *storagepb.ColumnValue {
	return &storagepb.ColumnValue{ColumnName: name, ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_TimeValue{TimeValue: value.UTC().Format(time.RFC3339Nano)}}}
}
func boolColumn(name string, value bool) *storagepb.ColumnValue {
	return &storagepb.ColumnValue{ColumnName: name, ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_BOOL, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_BoolValue{BoolValue: value}}}
}
