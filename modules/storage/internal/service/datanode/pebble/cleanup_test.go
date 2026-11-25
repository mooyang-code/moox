package pebble

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestDeleteDatasetRowsRemovesRowsAndHistoryWithoutTouchingOtherDataset(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	row := func(dataset, subject string) *pb.RowFieldUpsert {
		return &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: "s", DatasetId: dataset, Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: subject, Freq: "1m", DataTime: "2026-08-16T00:00:00Z"}}}, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}}}
	}
	rows := []*pb.RowFieldUpsert{row("delete-me", "BTC-USDT"), row("keep-me", "ETH-USDT")}
	if err := upsertTestRows(store, context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{SpaceId: "s", DatasetId: "delete-me", ColumnNames: []string{"close"}, TimeRange: &pb.TimeRange{StartTime: "2026-08-15T00:00:00Z", EndTime: "2026-08-17T00:00:00Z"}, Page: &pb.Page{Size: 10}}); err != nil {
		t.Fatal(err)
	}
	if deleted, err := store.DeleteDatasetRows(context.Background(), "s", "delete-me"); err != nil || deleted == 0 {
		t.Fatalf("deleted ranges=%d err=%v", deleted, err)
	}
	removed, err := store.ReadRows(context.Background(), []*pb.RowKey{rows[0].GetKey()}, []string{"close"}, nil)
	if err != nil || len(removed) != 0 {
		t.Fatalf("deleted dataset still has fields: rows=%v err=%v", removed, err)
	}
	remaining, err := store.ReadRows(context.Background(), []*pb.RowKey{rows[1].GetKey()}, []string{"close"}, nil)
	if err != nil || len(remaining) != 1 || len(remaining[0].GetFields()) != 1 {
		t.Fatalf("other dataset was removed: rows=%v err=%v", remaining, err)
	}
	history, err := store.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{SpaceId: "s", DatasetId: "delete-me", ColumnNames: []string{"close"}, TimeRange: &pb.TimeRange{StartTime: "2026-08-15T00:00:00Z", EndTime: "2026-08-17T00:00:00Z"}, Page: &pb.Page{Size: 10}})
	if err != nil || len(history.GetRows()) != 0 {
		t.Fatalf("deleted dataset still has history: rows=%v err=%v", history, err)
	}
}

// 元数据里已经没有的数据集，写入在拿到读锁后被拒绝；重建元数据后照常写入。
func TestWriteRejectedWhileDatasetIsMissingFromMetadata(t *testing.T) {
	exists := true
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), DatasetExists: func(string, string) bool { return exists }})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	row := &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: "s", DatasetId: "delete-me", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}, Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "v"}}}}}
	if err := upsertTestRows(store, context.Background(), []*pb.RowFieldUpsert{row}); err != nil {
		t.Fatal(err)
	}
	exists = false
	if _, err := store.DeleteDatasetRows(context.Background(), "s", "delete-me"); err != nil {
		t.Fatal(err)
	}
	if err := upsertTestRows(store, context.Background(), []*pb.RowFieldUpsert{row}); err != ErrDatasetDeleted {
		t.Fatalf("write after delete err=%v, want ErrDatasetDeleted", err)
	}
	exists = true
	if err := upsertTestRows(store, context.Background(), []*pb.RowFieldUpsert{row}); err != nil {
		t.Fatalf("write after recreate: %v", err)
	}
}

// 删除数据集后，变更日志里它的完成标记条目还在，但标记本体已删，转发时跳过。
func TestDeleteDatasetRowsMakesMarkersUnpublishable(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := &pb.RowKey{SpaceId: "s", DatasetId: "delete-me", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}
	if err := store.UpsertFields(context.Background(), "s", "delete-me", []*pb.RowFieldUpsert{{Key: key, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}}}}); err != nil {
		t.Fatal(err)
	}
	marker, _, err := BuildLedgerPeriodCompletedMessage("s", &LedgerPeriodCompleted{DatasetID: "delete-me", Frequency: "1m", PeriodTime: 1, Status: "complete", SnapshotRef: "cfg-1", CompletedAt: time.Unix(1, 0).UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendDatasetMarker(context.Background(), marker); err != nil {
		t.Fatal(err)
	}
	entries := listMarkerChanges(t, store)
	if len(entries) != 1 {
		t.Fatalf("删除前的完成标记条目=%d", len(entries))
	}
	if ok, err := store.MarkerPublishable(entries[0].Data); err != nil || !ok {
		t.Fatalf("删除前标记应可发布: ok=%v err=%v", ok, err)
	}
	if _, err := store.DeleteDatasetRows(context.Background(), "s", "delete-me"); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.MarkerPublishable(entries[0].Data); err != nil || ok {
		t.Fatalf("删除后标记不应再发布: ok=%v err=%v", ok, err)
	}
	if _, err := store.MarkerPublishable([]byte("不是事件")); !errors.Is(err, ErrInvalidMarker) {
		t.Fatalf("无法解析的标记应返回 ErrInvalidMarker: %v", err)
	}
}
