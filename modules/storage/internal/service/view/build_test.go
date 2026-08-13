package view

import (
	"reflect"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
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

func TestProjectBackfillFieldsUsesNextSchemaShape(t *testing.T) {
	active := viewindex.ViewIndexSchema{Columns: []*pb.ViewColumn{
		{ColumnName: "close", OriginId: "prices.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{ColumnName: "old", OriginId: "prices.old", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}}
	next := viewindex.ViewIndexSchema{Columns: []*pb.ViewColumn{
		{ColumnName: "close", OriginId: "prices.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{ColumnName: "old", OriginId: "prices.new", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}}
	fields := []*pb.FieldValue{
		{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}},
		{FieldId: "old", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 2}}},
		{FieldId: "removed", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 3}}},
	}
	got := projectBackfillFields(fields, active, next)
	if len(got) != 1 || got[0].GetFieldId() != "close" {
		t.Fatalf("projected fields=%v, want only unchanged close", got)
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
