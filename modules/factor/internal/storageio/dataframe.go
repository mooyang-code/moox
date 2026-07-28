package storageio

import (
	"fmt"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// RowsToDataFrame converts Storage rows into an engine DataFrame sorted by data_time ASC.
func RowsToDataFrame(rows []*storagepb.TimeSeriesRow, columns []string) (*engine.DataFrame, error) {
	type timedRow struct {
		row      *storagepb.TimeSeriesRow
		dataTime time.Time
	}
	ordered := make([]timedRow, 0, len(rows))
	for _, row := range rows {
		raw := row.GetKey().GetDataTime()
		dataTime, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return nil, fmt.Errorf("parse data_time %q: %w", raw, err)
		}
		ordered = append(ordered, timedRow{row: row, dataTime: dataTime.UTC()})
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].dataTime.Before(ordered[j].dataTime)
	})
	frame := &engine.DataFrame{
		Columns:   append([]string(nil), columns...),
		Rows:      make([][]any, 0, len(ordered)),
		DataTimes: make([]time.Time, 0, len(ordered)),
	}
	for _, item := range ordered {
		valuesByName := make(map[string]any, len(item.row.GetFields()))
		for _, field := range item.row.GetFields() {
			valuesByName[field.GetFieldId()] = typedValueToAny(field.GetValue())
		}
		out := make([]any, 0, len(columns))
		for _, name := range columns {
			out = append(out, valuesByName[name])
		}
		frame.DataTimes = append(frame.DataTimes, item.dataTime)
		frame.Rows = append(frame.Rows, out)
	}
	return frame, nil
}

func typedValueToAny(value *storagepb.TypedValue) any {
	if value == nil {
		return nil
	}
	switch value.GetValue().(type) {
	case *storagepb.TypedValue_IntValue:
		return value.GetIntValue()
	case *storagepb.TypedValue_DoubleValue:
		return value.GetDoubleValue()
	case *storagepb.TypedValue_StringValue:
		return value.GetStringValue()
	case *storagepb.TypedValue_BoolValue:
		return value.GetBoolValue()
	case *storagepb.TypedValue_TimeValue:
		return value.GetTimeValue()
	case *storagepb.TypedValue_JsonValue:
		return value.GetJsonValue()
	}
	return nil
}

func doubleField(name string, value float64) *storagepb.FieldValue {
	return &storagepb.FieldValue{
		FieldId: name,
		Value:   &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: value}},
	}
}
