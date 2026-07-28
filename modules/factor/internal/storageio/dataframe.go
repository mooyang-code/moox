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
		valuesByName := make(map[string]any, len(row.GetFields()))
		for _, field := range row.GetFields() {
			valuesByName[field.GetFieldId()] = typedValueToAny(field.GetValue())
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
