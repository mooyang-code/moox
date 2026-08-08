package view

import (
	"reflect"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestBackfillSortsUseCompleteTimeSeriesIdentity(t *testing.T) {
	got := backfillSorts("duckdb")
	want := []*pb.SortSpec{
		{FieldName: "data_time"},
		{FieldName: "subject_id"},
		{FieldName: "freq"},
		{FieldName: "series_tag"},
		{FieldName: "record_id"},
		{FieldName: "version"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("backfill sorts=%v want=%v", got, want)
	}
}

func TestBackfillTimeRangesUseViewRetention(t *testing.T) {
	ranges := backfillTimeRanges(&pb.View{KeepDuration: "72h"})
	if len(ranges) < 72*12 {
		t.Fatalf("backfill ranges = %d, want at least %d", len(ranges), 72*12)
	}
	start, err := time.Parse(time.RFC3339Nano, ranges[0].GetStartTime())
	if err != nil {
		t.Fatalf("parse start time: %v", err)
	}
	startAfter := time.Now().UTC().Add(-72 * time.Hour)
	if start.Before(startAfter.Add(-time.Second)) || start.After(startAfter.Add(time.Second)) {
		t.Fatalf("start time %s is not near the 72h retention boundary", start)
	}
	if ranges[0].GetEndTime() == "" {
		t.Fatal("first range end time is empty")
	}
}

func TestBackfillTimeRangeSkipsPermanentRetention(t *testing.T) {
	got := backfillTimeRanges(&pb.View{KeepDuration: "0"})
	if len(got) != 1 || got[0] != nil {
		t.Fatalf("permanent retention ranges = %+v, want one nil range", got)
	}
}
