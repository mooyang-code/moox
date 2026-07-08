package storageio

import (
	"fmt"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// KLineColumns is the canonical V1 OHLCV input order.
var KLineColumns = []string{"open", "high", "low", "close", "volume", "quote_volume", "trade_num"}

// RowsToDataFrame converts Storage rows into an engine DataFrame sorted by data_time ASC.
func RowsToDataFrame(rows []*storagepb.TimeSeriesRow, columns []string) (*engine.DataFrame, error) {
	ordered := append([]*storagepb.TimeSeriesRow(nil), rows...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].GetKey().GetDataTime() < ordered[j].GetKey().GetDataTime()
	})
	frame := &engine.DataFrame{
		Columns:   append([]string(nil), columns...),
		Rows:      make([][]any, 0, len(ordered)),
		DataTimes: make([]time.Time, 0, len(ordered)),
	}
	for _, row := range ordered {
		dataTime, err := time.Parse(time.RFC3339Nano, row.GetKey().GetDataTime())
		if err != nil {
			return nil, fmt.Errorf("parse data_time %q: %w", row.GetKey().GetDataTime(), err)
		}
		valuesByName := make(map[string]any, len(row.GetColumns()))
		for _, col := range row.GetColumns() {
			valuesByName[col.GetColumnName()] = typedValueToAny(col)
		}
		out := make([]any, 0, len(columns))
		for _, name := range columns {
			out = append(out, valuesByName[name])
		}
		frame.DataTimes = append(frame.DataTimes, dataTime.UTC())
		frame.Rows = append(frame.Rows, out)
	}
	return frame, nil
}

func typedValueToAny(col *storagepb.ColumnValue) any {
	if col == nil || col.GetValue() == nil {
		return nil
	}
	switch col.GetValueType() {
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_INT:
		return col.GetValue().GetIntValue()
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE:
		return col.GetValue().GetDoubleValue()
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING:
		return col.GetValue().GetStringValue()
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_BOOL:
		return col.GetValue().GetBoolValue()
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME:
		return col.GetValue().GetTimeValue()
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_JSON:
		return col.GetValue().GetJsonValue()
	default:
		switch col.GetValue().GetValue().(type) {
		case *storagepb.TypedValue_IntValue:
			return col.GetValue().GetIntValue()
		case *storagepb.TypedValue_DoubleValue:
			return col.GetValue().GetDoubleValue()
		case *storagepb.TypedValue_StringValue:
			return col.GetValue().GetStringValue()
		}
	}
	return nil
}

func doubleField(name string, value float64) *storagepb.ColumnValue {
	return &storagepb.ColumnValue{
		ColumnName: name,
		ValueType:  storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Value:      &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: value}},
	}
}

func intField(name string, value int64) *storagepb.ColumnValue {
	return &storagepb.ColumnValue{
		ColumnName: name,
		ValueType:  storagepb.FieldValueType_FIELD_VALUE_TYPE_INT,
		Value:      &storagepb.TypedValue{Value: &storagepb.TypedValue_IntValue{IntValue: value}},
	}
}
