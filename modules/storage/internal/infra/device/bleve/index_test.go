package bleve

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestBleveViewIndexStatReturnsRecordVersionBounds(t *testing.T) {
	ctx := context.Background()
	index, err := Open(Options{Path: filepath.Join(t.TempDir(), "record_view")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer index.Close()

	rows := []*pb.RecordRow{
		bleveTestRecordRow("news-1", "2026-07-09T01:00:00Z"),
		bleveTestRecordRow("news-2", "2026-07-09T01:01:00Z"),
	}
	if err := index.IndexRows(ctx, rows, map[string]bool{"title": true}); err != nil {
		t.Fatalf("IndexRows: %v", err)
	}

	stats, err := index.Stat(ctx)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !stats.Exists {
		t.Fatal("stats exists = false, want true")
	}
	if stats.EntryCount != 2 {
		t.Fatalf("entry count = %d, want 2", stats.EntryCount)
	}
	if stats.MinVersion != "2026-07-09T01:00:00.000000000Z" {
		t.Fatalf("min version = %q, want normalized timestamp", stats.MinVersion)
	}
	if stats.MaxVersion != "2026-07-09T01:01:00.000000000Z" {
		t.Fatalf("max version = %q, want normalized timestamp", stats.MaxVersion)
	}
}

func TestSearchRecordRowsPaginatesBeforeRowDeserialization(t *testing.T) {
	ctx := context.Background()
	index, err := Open(Options{Path: filepath.Join(t.TempDir(), "record_view")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer index.Close()

	rows := make([]*pb.RecordRow, 0, 500)
	for i := 0; i < 500; i++ {
		rows = append(rows, bleveTestRecordRow(
			"news-"+timeSuffix(i),
			"2026-07-10T00:"+minuteSecond(i)+"Z",
		))
	}
	if err := index.IndexRows(ctx, rows, map[string]bool{"title": true}); err != nil {
		t.Fatalf("IndexRows: %v", err)
	}

	decoded := 0
	index.unmarshalRow = func(raw []byte, row *pb.RecordRow) error {
		decoded++
		return (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, row)
	}
	got, page, err := index.SearchRecordRows(ctx, SearchRequest{
		SpaceID:   "crypto",
		DatasetID: "news",
		Page:      &pb.Page{Page: 3, Size: 25},
		Sorts:     []*pb.SortSpec{{FieldName: "version", Desc: true}},
	})
	if err != nil {
		t.Fatalf("SearchRecordRows: %v", err)
	}
	if len(got) != 25 || decoded > 26 {
		t.Fatalf("rows=%d decoded=%d, want 25 and at most 26", len(got), decoded)
	}
	if page.GetPage() != 3 || page.GetSize() != 25 || !page.GetHasMore() || page.GetTotal() != 500 {
		t.Fatalf("page = %+v", page)
	}
	if got[0].GetKey().GetVersion() <= got[len(got)-1].GetKey().GetVersion() {
		t.Fatalf("versions not sorted descending: %q .. %q", got[0].GetKey().GetVersion(), got[len(got)-1].GetKey().GetVersion())
	}
}

func TestSearchRecordRowsPushesFiltersAndColumnSortIntoBleve(t *testing.T) {
	ctx := context.Background()
	index, err := Open(Options{Path: filepath.Join(t.TempDir(), "record_view")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer index.Close()

	rows := []*pb.RecordRow{
		bleveFilterTestRow("news-1", "alpha", 2),
		bleveFilterTestRow("news-2", "market beta", 10),
		bleveFilterTestRow("news-3", "market gamma", 20),
	}
	if err := index.IndexRows(ctx, rows, map[string]bool{"title": true, "score": true}); err != nil {
		t.Fatalf("IndexRows: %v", err)
	}

	got, page, err := index.SearchRecordRows(ctx, SearchRequest{
		SpaceID: "crypto", DatasetID: "news", Page: &pb.Page{Page: 1, Size: 25},
		Filters: []*pb.FilterExpr{
			{Expr: "score >= $minimum", Args: map[string]*pb.TypedValue{"minimum": bleveIntValue(10)}},
			{Expr: "title contains $text", Args: map[string]*pb.TypedValue{"text": bleveStringValue("market")}},
		},
		Sorts: []*pb.SortSpec{{FieldName: "score", Desc: true}},
	})
	if err != nil {
		t.Fatalf("SearchRecordRows: %v", err)
	}
	if len(got) != 2 || got[0].GetKey().GetRecordId() != "news-3" || got[1].GetKey().GetRecordId() != "news-2" {
		t.Fatalf("filtered/sorted rows = %+v", got)
	}
	if page.GetTotal() != 2 || page.GetHasMore() {
		t.Fatalf("page = %+v", page)
	}
}

func TestSearchRecordRowsMatchesExactRecordKeyVersion(t *testing.T) {
	ctx := context.Background()
	index, err := Open(Options{Path: filepath.Join(t.TempDir(), "record_view")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer index.Close()
	if err := index.IndexRows(ctx, []*pb.RecordRow{
		bleveTestRecordRow("news-1", "2026-07-10T00:00:00Z"),
		bleveTestRecordRow("news-1", "2026-07-10T01:00:00Z"),
	}, map[string]bool{"title": true}); err != nil {
		t.Fatalf("IndexRows: %v", err)
	}

	got, _, err := index.SearchRecordRows(ctx, SearchRequest{
		SpaceID: "crypto", DatasetID: "news", Page: &pb.Page{Page: 1, Size: 25},
		Keys: []*pb.RecordKey{{RecordId: "news-1", Version: "2026-07-10T00:00:00Z"}},
	})
	if err != nil {
		t.Fatalf("SearchRecordRows: %v", err)
	}
	if len(got) != 1 || got[0].GetKey().GetVersion() != "2026-07-10T00:00:00Z" {
		t.Fatalf("exact key rows = %+v", got)
	}
}

func TestSearchRecordRowsHandlesEmptyAndNegativeTextFilters(t *testing.T) {
	ctx := context.Background()
	index, err := Open(Options{Path: filepath.Join(t.TempDir(), "record_view")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer index.Close()
	empty := bleveTestRecordRow("news-empty", "2026-07-10T00:00:00Z")
	empty.Columns[0].Value = bleveStringValue("")
	missing := bleveTestRecordRow("news-missing", "2026-07-10T00:01:00Z")
	missing.Columns = nil
	if err := index.IndexRows(ctx, []*pb.RecordRow{
		bleveFilterTestRow("news-market", "market update", 1), empty, missing,
	}, map[string]bool{"title": true}); err != nil {
		t.Fatalf("IndexRows: %v", err)
	}

	for _, test := range []struct {
		name string
		expr string
		args map[string]*pb.TypedValue
		want int
	}{
		{name: "empty", expr: "is_empty(title)", want: 2},
		{name: "not empty", expr: "is_not_empty(title)", want: 1},
		{name: "not contains", expr: "not_contains(title, $text)", args: map[string]*pb.TypedValue{"text": bleveStringValue("market")}, want: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			rows, _, err := index.SearchRecordRows(ctx, SearchRequest{
				SpaceID: "crypto", DatasetID: "news", Page: &pb.Page{Page: 1, Size: 25},
				Filters: []*pb.FilterExpr{{Expr: test.expr, Args: test.args}},
			})
			if err != nil || len(rows) != test.want {
				t.Fatalf("rows=%d err=%v, want %d", len(rows), err, test.want)
			}
		})
	}

	rows, _, err := index.SearchRecordRows(ctx, SearchRequest{
		SpaceID: "crypto", DatasetID: "news", TextQuery: "update", Page: &pb.Page{Page: 1, Size: 25},
	})
	if err != nil || len(rows) != 1 || rows[0].GetKey().GetRecordId() != "news-market" {
		t.Fatalf("text query rows=%+v err=%v", rows, err)
	}
}

func TestBleveViewIndexConcurrentWritesKeepExactStats(t *testing.T) {
	ctx := context.Background()
	index, err := Open(Options{Path: filepath.Join(t.TempDir(), "record_view")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer index.Close()

	const writers = 32
	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs <- index.IndexRows(ctx, []*pb.RecordRow{
				bleveTestRecordRow(fmt.Sprintf("news-%d", i), fmt.Sprintf("2026-07-10T00:00:%02dZ", i)),
			}, map[string]bool{"title": true})
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("IndexRows: %v", err)
		}
	}
	stats, err := index.Stat(ctx)
	if err != nil || stats.EntryCount != writers {
		t.Fatalf("stats=%+v err=%v, want %d entries", stats, err, writers)
	}
}

func bleveFilterTestRow(recordID string, title string, score int64) *pb.RecordRow {
	row := bleveTestRecordRow(recordID, "2026-07-10T00:00:00Z")
	row.Columns[0].Value = bleveStringValue(title)
	row.Columns = append(row.Columns, &pb.ColumnValue{
		ColumnName: "score", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_INT, Value: bleveIntValue(score),
	})
	return row
}

func bleveStringValue(value string) *pb.TypedValue {
	return &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: value}}
}

func bleveIntValue(value int64) *pb.TypedValue {
	return &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: value}}
}

func timeSuffix(value int) string {
	return fmt.Sprintf("%04d", value)
}

func minuteSecond(value int) string {
	return fmt.Sprintf("%02d:%02d", (value/60)%60, value%60)
}

func bleveTestRecordRow(recordID string, version string) *pb.RecordRow {
	return &pb.RecordRow{
		Key: &pb.RecordKey{
			SpaceId:   "crypto",
			DatasetId: "news",
			RecordId:  recordID,
			Version:   version,
		},
		Columns: []*pb.ColumnValue{{
			ColumnName: "title",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
			Value:      &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "market update"}},
		}},
	}
}
