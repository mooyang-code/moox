package storageio

import (
	"testing"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestRowsToDataFrameSortsRFC3339NanoChronologically(t *testing.T) {
	first := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	second := first.Add(time.Nanosecond)
	row := func(at time.Time, value float64) *storagepb.TimeSeriesRow {
		return &storagepb.TimeSeriesRow{
			Key: &storagepb.TimeSeriesKey{DataTime: at.Format(time.RFC3339Nano)},
			Fields: []*storagepb.FieldValue{{
				FieldId: "value",
				Value:   &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: value}},
			}},
		}
	}
	frame, err := RowsToDataFrame(
		[]*storagepb.TimeSeriesRow{row(second, 2), row(first, 1)},
		[]string{"value"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(frame.DataTimes) != 2 || !frame.DataTimes[0].Equal(first) || !frame.DataTimes[1].Equal(second) {
		t.Fatalf("data times are not chronological: %v", frame.DataTimes)
	}
	if len(frame.Rows) != 2 || frame.Rows[0][0] != float64(1) || frame.Rows[1][0] != float64(2) {
		t.Fatalf("row values do not follow sorted timestamps: %v", frame.Rows)
	}
}
