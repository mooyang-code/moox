package storageio

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// RowsToDataFrame converts Storage rows into a DataFrame sorted by (data_time, series_tag).
func RowsToDataFrame(rows []*storagepb.TimeSeriesRow, columns []string) (*engine.DataFrame, error) {
	type timedRow struct {
		row       *storagepb.TimeSeriesRow
		dataTime  time.Time
		seriesTag string
	}
	ordered := make([]timedRow, 0, len(rows))
	for _, row := range rows {
		raw := row.GetKey().GetDataTime()
		dataTime, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return nil, fmt.Errorf("parse data_time %q: %w", raw, err)
		}
		ordered = append(ordered, timedRow{
			row: row, dataTime: dataTime.UTC(), seriesTag: row.GetKey().GetSeriesTag(),
		})
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].dataTime.Equal(ordered[j].dataTime) {
			return ordered[i].dataTime.Before(ordered[j].dataTime)
		}
		return ordered[i].seriesTag < ordered[j].seriesTag
	})
	frame := &engine.DataFrame{
		Columns:    append([]string(nil), columns...),
		Rows:       make([][]any, 0, len(ordered)),
		DataTimes:  make([]time.Time, 0, len(ordered)),
		SeriesTags: make([]string, 0, len(ordered)),
	}
	for i, item := range ordered {
		if i > 0 && item.dataTime.Equal(ordered[i-1].dataTime) &&
			item.seriesTag == ordered[i-1].seriesTag {
			return nil, fmt.Errorf("duplicate time-series identity data_time=%s series_tag=%q",
				item.dataTime.Format(time.RFC3339Nano), item.seriesTag)
		}
		rawValues := make(map[string]any, len(item.row.GetFields()))
		for _, field := range item.row.GetFields() {
			rawName := field.GetFieldId()
			if _, duplicate := rawValues[rawName]; duplicate {
				return nil, fmt.Errorf("duplicate physical View field %q", rawName)
			}
			rawValues[rawName] = typedValueToAny(field.GetValue())
		}
		out := make([]any, 0, len(columns))
		for _, name := range columns {
			value, resolveErr := resolveInputValue(rawValues, name)
			if resolveErr != nil {
				return nil, resolveErr
			}
			out = append(out, value)
		}
		frame.DataTimes = append(frame.DataTimes, item.dataTime)
		frame.SeriesTags = append(frame.SeriesTags, item.seriesTag)
		frame.Rows = append(frame.Rows, out)
	}
	return frame, nil
}

func resolveInputValue(rawValues map[string]any, input string) (any, error) {
	if value, ok := rawValues[input]; ok {
		return value, nil
	}
	if strings.Contains(input, ".") {
		return nil, nil
	}
	var matched string
	for rawName := range rawValues {
		_, suffix, qualified := strings.Cut(rawName, ".")
		if !qualified || suffix != input {
			continue
		}
		if matched != "" {
			return nil, fmt.Errorf("duplicate projected field %q from %q and %q", input, matched, rawName)
		}
		matched = rawName
	}
	if matched == "" {
		return nil, nil
	}
	return rawValues[matched], nil
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
