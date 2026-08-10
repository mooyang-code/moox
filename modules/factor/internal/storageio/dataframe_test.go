package storageio

import (
	"reflect"
	"testing"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestRowsToDataFrameSortsByTimeAndSeriesTag(t *testing.T) {
	first := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	second := first.Add(time.Nanosecond)
	row := func(at time.Time, tag string, value float64) *storagepb.TimeSeriesRow {
		return &storagepb.TimeSeriesRow{
			Key: &storagepb.TimeSeriesKey{DataTime: at.Format(time.RFC3339Nano), SeriesTag: tag},
			Fields: []*storagepb.FieldValue{{
				FieldId: "value",
				Value:   &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: value}},
			}},
		}
	}
	frame, err := RowsToDataFrame(
		[]*storagepb.TimeSeriesRow{
			row(second, "", 3),
			row(first, "venue:okx", 2),
			row(first, "venue:binance", 1),
		},
		[]string{"value"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(frame.DataTimes) != 3 || !frame.DataTimes[0].Equal(first) ||
		!frame.DataTimes[1].Equal(first) || !frame.DataTimes[2].Equal(second) {
		t.Fatalf("data times are not chronological: %v", frame.DataTimes)
	}
	if len(frame.Rows) != 3 || frame.Rows[0][0] != float64(1) ||
		frame.Rows[1][0] != float64(2) || frame.Rows[2][0] != float64(3) {
		t.Fatalf("row values do not follow sorted timestamps: %v", frame.Rows)
	}
	if got := frame.SeriesTags; len(got) != 3 ||
		got[0] != "venue:binance" || got[1] != "venue:okx" || got[2] != "" {
		t.Fatalf("series tags are not in identity order: %v", got)
	}
}

func TestRowsToDataFrameRejectsDuplicateIdentity(t *testing.T) {
	at := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	row := func(value float64) *storagepb.TimeSeriesRow {
		return &storagepb.TimeSeriesRow{
			Key: &storagepb.TimeSeriesKey{
				DataTime: at.Format(time.RFC3339Nano), SeriesTag: "venue:binance",
			},
			Fields: []*storagepb.FieldValue{{
				FieldId: "value",
				Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{
					DoubleValue: value,
				}},
			}},
		}
	}
	_, err := RowsToDataFrame([]*storagepb.TimeSeriesRow{row(1), row(2)}, []string{"value"})
	if err == nil {
		t.Fatal("expected duplicate identity rejection")
	}
}

func TestRowsToDataFrameMapsQualifiedViewFieldsToLogicalInputs(t *testing.T) {
	at := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	frame, err := RowsToDataFrame([]*storagepb.TimeSeriesRow{{
		Key: &storagepb.TimeSeriesKey{
			DatasetId: "bars", DataTime: at.Format(time.RFC3339Nano),
		},
		Fields: []*storagepb.FieldValue{{
			FieldId: "bars.close",
			Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{
				DoubleValue: 101,
			}},
		}},
	}}, []string{"close"})
	if err != nil {
		t.Fatal(err)
	}
	if len(frame.Rows) != 1 || len(frame.Rows[0]) != 1 || frame.Rows[0][0] != float64(101) {
		t.Fatalf("qualified field was not mapped to logical input: %v", frame.Rows)
	}
}

func TestRowsToDataFrameMapsOnePhysicalFieldToQualifiedAndLogicalAliases(t *testing.T) {
	at := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	frame, err := RowsToDataFrame([]*storagepb.TimeSeriesRow{{
		Key: &storagepb.TimeSeriesKey{DatasetId: "prices", DataTime: at.Format(time.RFC3339Nano)},
		Fields: []*storagepb.FieldValue{{
			FieldId: "prices.close",
			Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{
				DoubleValue: 101,
			}},
		}},
	}}, []string{"close", "prices.close"})
	if err != nil {
		t.Fatal(err)
	}
	want := []any{float64(101), float64(101)}
	if len(frame.Rows) != 1 || !reflect.DeepEqual(frame.Rows[0], want) {
		t.Fatalf("physical field aliases = %v, want %v", frame.Rows, want)
	}
}

func TestRowsToDataFrameMapsSecondaryQualifiedViewFields(t *testing.T) {
	at := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	frame, err := RowsToDataFrame([]*storagepb.TimeSeriesRow{{
		Key:    &storagepb.TimeSeriesKey{DatasetId: "bars", DataTime: at.Format(time.RFC3339Nano)},
		Fields: []*storagepb.FieldValue{{FieldId: "fundamentals.factor", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: 3.5}}}},
	}}, []string{"factor"})
	if err != nil {
		t.Fatal(err)
	}
	if got := frame.Rows[0][0]; got != float64(3.5) {
		t.Fatalf("secondary qualified field = %v, want 3.5", got)
	}
}

func TestRowsToDataFrameExactRuntimeColumnWinsOverQualifiedSuffix(t *testing.T) {
	at := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	frame, err := RowsToDataFrame([]*storagepb.TimeSeriesRow{{
		Key: &storagepb.TimeSeriesKey{
			DatasetId: "bars", DataTime: at.Format(time.RFC3339Nano),
		},
		Fields: []*storagepb.FieldValue{
			{FieldId: "close", Value: &storagepb.TypedValue{
				Value: &storagepb.TypedValue_DoubleValue{DoubleValue: 100},
			}},
			{FieldId: "bars.close", Value: &storagepb.TypedValue{
				Value: &storagepb.TypedValue_DoubleValue{DoubleValue: 101},
			}},
		},
	}}, []string{"close"})
	if err != nil {
		t.Fatal(err)
	}
	if got := frame.Rows[0][0]; got != float64(100) {
		t.Fatalf("exact close = %v, want 100", got)
	}
}

func TestRowsToDataFrameExactNullWinsOverQualifiedSuffix(t *testing.T) {
	at := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	frame, err := RowsToDataFrame([]*storagepb.TimeSeriesRow{{
		Key: &storagepb.TimeSeriesKey{DatasetId: "bars", DataTime: at.Format(time.RFC3339Nano)},
		Fields: []*storagepb.FieldValue{
			{FieldId: "close", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_NullValue{NullValue: storagepb.NullValue_NULL_VALUE_NULL}}},
			{FieldId: "bars.close", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: 101}}},
		},
	}}, []string{"close", "bars.close"})
	if err != nil {
		t.Fatal(err)
	}
	if frame.Rows[0][0] != nil || frame.Rows[0][1] != float64(101) {
		t.Fatalf("exact NULL alias resolution = %v", frame.Rows[0])
	}
}
