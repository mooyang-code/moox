package view

import (
	"reflect"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestBackfillSortsUseCompleteTimeSeriesIdentity(t *testing.T) {
	got := backfillSorts("duckdb")
	want := []*pb.SortSpec{
		{FieldName: "subject_id"},
		{FieldName: "freq"},
		{FieldName: "data_time"},
		{FieldName: "series_tag"},
		{FieldName: "record_id"},
		{FieldName: "version"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("backfill sorts=%v want=%v", got, want)
	}
}
