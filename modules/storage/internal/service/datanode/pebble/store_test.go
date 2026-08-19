package pebble

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

func TestCleanupExpiredBucketsRemovesOnlyOldTimeSeries(t *testing.T) {
	s, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old := "2026-07-18T00:00:00Z"
	newer := "2026-07-19T00:00:00Z"
	rows := []*pb.RowFieldUpsert{
		{Key: &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "x", Freq: "1d", DataTime: old}}}, Fields: []*pb.FieldValue{{FieldId: "f", Value: &pb.TypedValue{}}}},
		{Key: &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "x", Freq: "1d", DataTime: old, SeriesTag: "venue:okx"}}}, Fields: []*pb.FieldValue{{FieldId: "f", Value: &pb.TypedValue{}}}},
		{Key: &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "x", Freq: "1d", DataTime: newer}}}, Fields: []*pb.FieldValue{{FieldId: "f", Value: &pb.TypedValue{}}}},
	}
	if err := s.UpsertFields(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.CleanupExpiredBuckets(context.Background(), "s", "d", time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC))
	if err != nil || deleted != 1 {
		t.Fatalf("cleanup deleted=%d err=%v", deleted, err)
	}
	removed, err := s.ReadFields(context.Background(), []*pb.RowKey{rows[0].GetKey(), rows[1].GetKey()}, []string{"f"}, nil)
	if err != nil || len(removed) != 2 || len(removed[0].GetFields()) != 0 || len(removed[1].GetFields()) != 0 {
		t.Fatalf("old bucket series were not removed: rows=%v err=%v", removed, err)
	}
	remaining, err := s.ReadFields(context.Background(), []*pb.RowKey{rows[2].GetKey()}, []string{"f"}, nil)
	if err != nil || len(remaining) != 1 || len(remaining[0].GetFields()) != 1 {
		t.Fatalf("newer bucket was removed: rows=%v err=%v", remaining, err)
	}
}

func TestReadTimeSeriesRowsScansPrimaryHistoryWithoutView(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows := []*pb.RowFieldUpsert{
		{Key: &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-08-16T00:00:00Z"}}}, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 10}}}}},
		{Key: &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-08-16T00:01:00Z"}}}, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 11}}}}},
	}
	if err := store.UpsertFields(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	rsp, err := store.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{
		SpaceId: "s", DatasetId: "d", TimeRange: &pb.TimeRange{StartTime: "2026-08-16T00:00:00Z", EndTime: "2026-08-16T00:02:00Z"},
		ColumnNames: []string{"close"}, Page: &pb.Page{Page: 1, Size: 10},
	})
	if err != nil || len(rsp.GetRows()) != 2 || rsp.GetRows()[1].GetKey().GetDataTime() != "2026-08-16T00:01:00.000000000Z" {
		t.Fatalf("scan rsp=%v err=%v", rsp, err)
	}
}

func TestReadTimeSeriesRowsBackfillsHistoryFromLegacyFieldKeys(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := &pb.RowKey{SpaceId: "s", DatasetId: "legacy", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
		SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-08-14T00:00:00Z",
	}}}
	if err := store.UpsertFields(context.Background(), []*pb.RowFieldUpsert{{
		Key: key, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 10}}}},
	}}); err != nil {
		t.Fatal(err)
	}
	historyKey, err := encodeHistoryKey(key, store.bucketDuration)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.Delete(historyKey, store.writeOptions); err != nil {
		t.Fatal(err)
	}
	rsp, err := store.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{
		SpaceId: "s", DatasetId: "legacy", TimeRange: &pb.TimeRange{StartTime: "2026-08-14T00:00:00Z", EndTime: "2026-08-14T00:01:00Z"},
		ColumnNames: []string{"close"}, Page: &pb.Page{Page: 1, Size: 10},
	})
	if err != nil || len(rsp.GetRows()) != 1 || rsp.GetRows()[0].GetKey().GetDataTime() != "2026-08-14T00:00:00.000000000Z" {
		t.Fatalf("legacy history rsp=%v err=%v", rsp, err)
	}
}

func TestReadTimeSeriesRowsUsesCursorWithoutRepeatingPage(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for i := 0; i < 3; i++ {
		key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: time.Date(2026, 8, 16, 0, i, 0, 0, time.UTC).Format(time.RFC3339)}}}
		if err := store.UpsertFields(context.Background(), []*pb.RowFieldUpsert{{Key: key, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: float64(i)}}}}}}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{SpaceId: "s", DatasetId: "d", TimeRange: &pb.TimeRange{StartTime: "2026-08-16T00:00:00Z", EndTime: "2026-08-16T00:03:00Z"}, ColumnNames: []string{"close"}, Page: &pb.Page{Page: 1, Size: 1}})
	if err != nil || len(first.GetRows()) != 1 {
		t.Fatalf("first page=%v err=%v", first, err)
	}
	firstKey := first.GetRows()[0].GetKey()
	cursor, err := proto.Marshal(&pb.RowKey{SpaceId: firstKey.GetSpaceId(), DatasetId: firstKey.GetDatasetId(), Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: firstKey.GetSubjectId(), Freq: firstKey.GetFreq(), DataTime: firstKey.GetDataTime(), SeriesTag: firstKey.GetSeriesTag()}}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{SpaceId: "s", DatasetId: "d", AfterKey: cursor, TimeRange: &pb.TimeRange{StartTime: "2026-08-16T00:00:00Z", EndTime: "2026-08-16T00:03:00Z"}, ColumnNames: []string{"close"}, Page: &pb.Page{Page: 1, Size: 2}})
	if err != nil || len(second.GetRows()) != 2 || second.GetRows()[0].GetKey().GetDataTime() != "2026-08-16T00:01:00Z" && second.GetRows()[0].GetKey().GetDataTime() != "2026-08-16T00:01:00.000000000Z" {
		t.Fatalf("cursor page=%v err=%v", second, err)
	}
}

func TestReadTimeSeriesRowsCursorUsesLogicalTimeAcrossSubjects(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// Physical keys sort by subject before data_time. The second row is later
	// in the logical time order but physically precedes the cursor subject.
	rows := []*pb.RowFieldUpsert{
		{Key: &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "Z", Freq: "1m", DataTime: "2026-08-16T00:00:00Z"}}}, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}}},
		{Key: &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "A", Freq: "1m", DataTime: "2026-08-16T00:01:00Z"}}}, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 2}}}}},
	}
	if err := store.UpsertFields(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	first, err := store.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{SpaceId: "s", DatasetId: "d", TimeRange: &pb.TimeRange{StartTime: "2026-08-16T00:00:00Z", EndTime: "2026-08-16T00:02:00Z"}, Page: &pb.Page{Page: 1, Size: 1}})
	if err != nil || len(first.GetRows()) != 1 {
		t.Fatalf("first page=%v err=%v", first, err)
	}
	row := first.GetRows()[0].GetKey()
	cursor, err := proto.Marshal(&pb.RowKey{SpaceId: row.GetSpaceId(), DatasetId: row.GetDatasetId(), Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: row.GetSubjectId(), Freq: row.GetFreq(), DataTime: row.GetDataTime(), SeriesTag: row.GetSeriesTag()}}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{SpaceId: "s", DatasetId: "d", AfterKey: cursor, TimeRange: &pb.TimeRange{StartTime: "2026-08-16T00:00:00Z", EndTime: "2026-08-16T00:02:00Z"}, Page: &pb.Page{Page: 1, Size: 1}})
	if err != nil || len(second.GetRows()) != 1 || second.GetRows()[0].GetKey().GetSubjectId() != "A" {
		t.Fatalf("logical cursor page=%v err=%v", second, err)
	}
}

func TestReadTimeSeriesRowsRejectsLargePageOffset(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, err = store.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{
		SpaceId: "s", DatasetId: "d", Page: &pb.Page{Page: 1001, Size: 1000},
	})
	if err == nil || !strings.Contains(err.Error(), "after_key") {
		t.Fatalf("large page offset error = %v, want cursor guidance", err)
	}
}

func TestCleanupExpiredBucketsDoesNotCreateOutboxEvent(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	row := &pb.RowFieldUpsert{
		Key: &pb.RowKey{
			SpaceId: "s", DatasetId: "d",
			Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
				SubjectId: "x", Freq: "1d", DataTime: "2026-07-18T00:00:00Z",
			}},
		},
		Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}},
	}
	if _, err := store.UpsertFieldsEvent(ctx, []*pb.RowFieldUpsert{row}, func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return BuildDatasetRowsUpsertedMessage("node-1", spaceID, datasetID, rows)
	}); err != nil {
		t.Fatal(err)
	}
	before, err := store.ListOutbox(ctx, 0, 10)
	if err != nil || len(before) != 1 {
		t.Fatalf("outbox before cleanup = %v, %v", before, err)
	}

	deleted, err := store.CleanupExpiredBuckets(ctx, "s", "d", time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC))
	if err != nil || deleted != 1 {
		t.Fatalf("cleanup deleted=%d err=%v", deleted, err)
	}
	after, err := store.ListOutbox(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("outbox count changed after cleanup: before=%d after=%d", len(before), len(after))
	}
}

func TestCleanupExpiredBucketsIsolatedBySpace(t *testing.T) {
	s, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	row := func(space string) *pb.RowFieldUpsert {
		return &pb.RowFieldUpsert{
			Key:    &pb.RowKey{SpaceId: space, DatasetId: "shared", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "x", Freq: "1d", DataTime: "2026-07-18T00:00:00Z"}}},
			Fields: []*pb.FieldValue{{FieldId: "f", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: space}}}},
		}
	}
	if err := s.UpsertFields(context.Background(), []*pb.RowFieldUpsert{row("a"), row("b")}); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.CleanupExpiredBuckets(context.Background(), "a", "shared", time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC))
	if err != nil || deleted != 1 {
		t.Fatalf("cleanup deleted=%d err=%v", deleted, err)
	}
	rows, err := s.ReadFields(context.Background(), []*pb.RowKey{row("b").GetKey()}, []string{"f"}, nil)
	if err != nil || len(rows) != 1 || len(rows[0].GetFields()) != 1 {
		t.Fatalf("space b row removed: rows=%v err=%v", rows, err)
	}
}

func TestUpsertFieldsUpsertsIndependentlyAndReadsOnlyRequestedFields(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1", BucketDuration: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "x", Freq: "1m", DataTime: "2026-07-19T10:00:00Z"}}}
	row := &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}}}
	if err := store.UpsertFields(context.Background(), []*pb.RowFieldUpsert{row}); err != nil {
		t.Fatal(err)
	}
	row = &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{{FieldId: "volume", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 10}}}}}
	if err := store.UpsertFields(context.Background(), []*pb.RowFieldUpsert{row}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ReadFields(context.Background(), []*pb.RowKey{key}, []string{"close", "volume", "missing"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].GetFields()) != 2 {
		t.Fatalf("rows=%v", rows)
	}
	row = &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 2}}}}}
	if err := store.UpsertFields(context.Background(), []*pb.RowFieldUpsert{row}); err != nil {
		t.Fatal(err)
	}
	rows, err = store.ReadFields(context.Background(), []*pb.RowKey{key}, []string{"close"}, nil)
	if err != nil || len(rows) != 1 || rows[0].GetFields()[0].GetValue().GetDoubleValue() != 2 {
		t.Fatalf("updated rows=%v err=%v", rows, err)
	}
}

func TestUpsertFieldsExplicitNullReplacesStoredValue(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := &pb.RowKey{
		SpaceId: "s", DatasetId: "d",
		Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
			SubjectId: "x", Freq: "1m", DataTime: "2026-07-19T10:00:00Z",
		}},
	}
	write := func(value *pb.TypedValue) {
		t.Helper()
		if err := store.UpsertFields(context.Background(), []*pb.RowFieldUpsert{{
			Key: key, Fields: []*pb.FieldValue{{FieldId: "close", Value: value}},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	write(&pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}})
	write(&pb.TypedValue{Value: &pb.TypedValue_NullValue{NullValue: pb.NullValue_NULL_VALUE_NULL}})

	rows, err := store.ReadFields(context.Background(), []*pb.RowKey{key}, []string{"close"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].GetFields()) != 1 {
		t.Fatalf("rows=%v", rows)
	}
	if got := rows[0].GetFields()[0].GetValue().GetNullValue(); got != pb.NullValue_NULL_VALUE_NULL {
		t.Fatalf("null marker = %s; rows=%v", got, rows)
	}
}

func TestSeriesTagsKeepSameTimestampRowsDistinct(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	key := func(tag string) *pb.RowKey {
		return &pb.RowKey{
			SpaceId:   "crypto",
			DatasetId: "prices",
			Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
				SubjectId: "BTC-USDT",
				Freq:      "1m",
				DataTime:  "2026-07-26T01:02:00Z",
				SeriesTag: tag,
			}},
		}
	}
	rows := []*pb.RowFieldUpsert{
		{Key: key(""), Fields: []*pb.FieldValue{{FieldId: "venue", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "default"}}}}},
		{Key: key("venue:binance"), Fields: []*pb.FieldValue{{FieldId: "venue", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "binance"}}}}, Attributes: map[string]*pb.TypedValue{"source": {Value: &pb.TypedValue_StringValue{StringValue: "stream-binance"}}}},
		{Key: key("venue:okx"), Fields: []*pb.FieldValue{{FieldId: "venue", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "okx"}}}}},
	}
	if err := store.UpsertFields(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	got, err := store.ReadFields(context.Background(), []*pb.RowKey{key(""), key("venue:binance"), key("venue:okx")}, []string{"venue"}, []string{"source"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 ||
		got[0].GetFields()[0].GetValue().GetStringValue() != "default" ||
		got[1].GetFields()[0].GetValue().GetStringValue() != "binance" ||
		got[1].GetAttributes()["source"].GetStringValue() != "stream-binance" ||
		got[2].GetFields()[0].GetValue().GetStringValue() != "okx" {
		t.Fatalf("series-tagged rows collided: %v", got)
	}
}

func TestSeriesTagSymbolsRoundTripWithoutRewriting(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	for _, tag := range []string{"市场:现货", "venue/binance@spot+v2"} {
		key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
			SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-26T01:02:00Z", SeriesTag: tag,
		}}}
		if err := store.UpsertFields(context.Background(), []*pb.RowFieldUpsert{{
			Key: key, Fields: []*pb.FieldValue{{FieldId: "tag", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: tag}}}},
		}}); err != nil {
			t.Fatal(err)
		}
		got, err := store.ReadFields(context.Background(), []*pb.RowKey{key}, []string{"tag"}, nil)
		if err != nil || len(got) != 1 || got[0].GetKey().GetTimeSeries().GetSeriesTag() != tag ||
			got[0].GetFields()[0].GetValue().GetStringValue() != tag {
			t.Fatalf("tag %q round trip: rows=%v err=%v", tag, got, err)
		}
	}
}

func TestReadFieldsReportsPhysicalRowPresenceWithoutRequestedField(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	present := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "present", Version: "1"}}}
	missing := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "missing", Version: "1"}}}
	if err := store.UpsertFields(context.Background(), []*pb.RowFieldUpsert{{Key: present, Fields: []*pb.FieldValue{{FieldId: "stored", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "v"}}}}}}); err != nil {
		t.Fatal(err)
	}
	rows, existing, err := store.ReadFieldsWithPresence(context.Background(), []*pb.RowKey{present, missing}, []string{"not_stored"}, nil)
	if err != nil || len(rows) != 2 || len(existing) != 1 || existing[0].GetRecord().GetRecordId() != "present" {
		t.Fatalf("rows=%v existing=%v err=%v", rows, existing, err)
	}
}

func TestRecordEmptyVersionResolvesCharacterMaximum(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, version := range []string{"1", "2", "10"} {
		key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: version}}}
		row := &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: version}}}}}
		if err := store.UpsertFields(context.Background(), []*pb.RowFieldUpsert{row}); err != nil {
			t.Fatal(err)
		}
	}
	key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r"}}}
	rows, err := store.ReadFields(context.Background(), []*pb.RowKey{key}, []string{"value"}, nil)
	if err != nil || len(rows) != 1 || rows[0].GetKey().GetRecord().GetVersion() != "2" {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
}

func TestWriteRejectsEventLargerThanPublisherLimitBeforeCommit(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1", MaxEventBytes: 32})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}
	row := &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "value"}}}}}
	if _, err := store.UpsertFieldsEvent(context.Background(), []*pb.RowFieldUpsert{row}, func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return BuildDatasetRowsUpsertedMessage("node-1", spaceID, datasetID, rows)
	}); err == nil {
		t.Fatal("expected oversized event to be rejected")
	}
	rows, err := store.ReadFields(context.Background(), []*pb.RowKey{key}, []string{"value"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].GetFields()) != 0 {
		t.Fatalf("fact committed despite oversized event: %v", rows)
	}
}
