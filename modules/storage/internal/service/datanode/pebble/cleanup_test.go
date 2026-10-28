package pebble

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	cpebble "github.com/cockroachdb/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestDeleteDatasetRowsRemovesRowsAndHistoryWithoutTouchingOtherDataset(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
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

// 删除数据集会清掉它的 commit 标记：恢复后同一 commit_id 作为新写入重新生效。
func TestDeleteDatasetRowsClearsCommitMarkers(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	row := &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: "s", DatasetId: "delete-me", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-08-16T00:00:00Z"}}}, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}}}
	if entries, err := store.UpsertFields(context.Background(), "s", "delete-me", "commit-1", []*pb.RowFieldUpsert{row}); err != nil || len(entries) != 1 {
		t.Fatalf("first commit write entries=%d err=%v", len(entries), err)
	}
	if _, err := store.DeleteDatasetRows(context.Background(), "s", "delete-me"); err != nil {
		t.Fatal(err)
	}
	if err := store.RestoreDatasetRows(context.Background(), "s", "delete-me"); err != nil {
		t.Fatal(err)
	}
	if entries, err := store.UpsertFields(context.Background(), "s", "delete-me", "commit-1", []*pb.RowFieldUpsert{row}); err != nil || len(entries) != 1 {
		t.Fatalf("recreated commit write entries=%d err=%v", len(entries), err)
	}
}

func TestDeleteDatasetRowsTombstoneBlocksWritesUntilRestore(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	row := &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: "s", DatasetId: "delete-me", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}, Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "v"}}}}}
	if err := upsertTestRows(store, context.Background(), []*pb.RowFieldUpsert{row}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteDatasetRows(context.Background(), "s", "delete-me"); err != nil {
		t.Fatal(err)
	}
	if err := upsertTestRows(store, context.Background(), []*pb.RowFieldUpsert{row}); err != ErrDatasetDeleted {
		t.Fatalf("write after delete err=%v, want ErrDatasetDeleted", err)
	}
	if err := store.RestoreDatasetRows(context.Background(), "s", "delete-me"); err != nil {
		t.Fatal(err)
	}
	if err := upsertTestRows(store, context.Background(), []*pb.RowFieldUpsert{row}); err != nil {
		t.Fatalf("write after restore: %v", err)
	}
}

func TestDeleteDatasetRowsRemovesCommitMarkersMarkersAndOutbox(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := &pb.RowKey{SpaceId: "s", DatasetId: "delete-me", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}
	if _, err := store.UpsertFields(context.Background(), "s", "delete-me", "receipt-1", []*pb.RowFieldUpsert{{Key: key, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}}}}); err != nil {
		t.Fatal(err)
	}
	marker, _, err := BuildLedgerPeriodCompletedMessage("s", &LedgerPeriodCompleted{DatasetID: "delete-me", Frequency: "1m", PeriodTime: 1, Status: "complete", SnapshotRef: "cfg-1", CompletedAt: timestamppb.New(time.Unix(1, 0))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendDatasetMarker(context.Background(), marker); err != nil {
		t.Fatal(err)
	}
	if entries, err := store.ListOutbox(context.Background(), 0, 20); err != nil || len(entries) != 2 {
		t.Fatalf("outbox before cleanup entries=%d err=%v", len(entries), err)
	}
	if _, err := store.DeleteDatasetRows(context.Background(), "s", "delete-me"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.readCommitMarker(commitMarkerKey("s", "delete-me", "receipt-1")); err != nil || found {
		t.Fatalf("commit marker survived dataset deletion: found=%v err=%v", found, err)
	}
	if entries, err := store.ListOutbox(context.Background(), 0, 20); err != nil || len(entries) != 0 {
		t.Fatalf("outbox after cleanup entries=%d err=%v", len(entries), err)
	}
}

func TestDeleteDatasetRowsClearsPeriodProgressAndIndexes(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	deadline := time.Now().UTC().Add(time.Minute).Unix()
	exp := periodExpectationForTest(time.Now().UTC().Truncate(time.Minute), 1, "BTC-USDT")
	exp.SpaceID = "s"
	exp.DatasetID = "delete-me"
	exp.DeadlineAt = deadline
	keep := exp
	keep.DatasetID = "keep-me"
	if _, err := store.EnsureDatasetPeriod(ctx, exp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureDatasetPeriod(ctx, keep); err != nil {
		t.Fatal(err)
	}

	if _, err := store.DeleteDatasetRows(ctx, exp.SpaceID, exp.DatasetID); err != nil {
		t.Fatal(err)
	}
	if err := store.RestoreDatasetRows(ctx, exp.SpaceID, exp.DatasetID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetDatasetPeriodProgress(ctx, exp); !errors.Is(err, cpebble.ErrNotFound) {
		t.Fatalf("deleted period progress err=%v, want not found", err)
	}

	finalized, err := store.FinalizeWaitingDatasetPeriods(ctx, time.Unix(deadline+1, 0).UTC(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if finalized != 1 {
		t.Fatalf("finalized periods=%d, want only unrelated dataset", finalized)
	}
	if got := countOutboxEvent(t, store, events.DatasetPeriodCompleted.Name()); got != 1 {
		t.Fatalf("collector period markers=%d, want only unrelated dataset marker", got)
	}

	result, err := store.EnsureDatasetPeriod(ctx, exp)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "waiting" {
		t.Fatalf("recreated period status=%q, want waiting", result.Status)
	}
}

func TestRestoreDatasetRowsWithoutTombstonePreservesPeriodProgress(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	period := time.Now().UTC().Truncate(time.Minute)
	exp := periodExpectationForTest(period, 2, "BTC-USDT", "ETH-USDT")
	exp.SpaceID = "s"
	exp.DatasetID = "active"
	deadline := time.Now().UTC().Add(time.Minute).Unix()
	exp.DeadlineAt = deadline
	if _, err := store.EnsureDatasetPeriod(ctx, exp); err != nil {
		t.Fatal(err)
	}
	row := periodRowForTest(period, "BTC-USDT")
	row.GetKey().SpaceId = exp.SpaceID
	row.GetKey().DatasetId = exp.DatasetID
	if _, err := store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: row}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordDatasetPeriodFailures(ctx, exp, []uint32{1}); err != nil {
		t.Fatal(err)
	}

	if err := store.RestoreDatasetRows(ctx, exp.SpaceID, exp.DatasetID); err != nil {
		t.Fatal(err)
	}
	progress, err := store.GetDatasetPeriodProgress(ctx, exp)
	if err != nil {
		t.Fatalf("period progress after no-op restore: %v", err)
	}
	if progress.Status != "waiting" || bitmapCount(progress.Bitmap) != 1 || len(progress.FailedSeriesIndexes) != 1 || progress.FailedSeriesIndexes[0] != 1 {
		t.Fatalf("period progress after no-op restore=%+v, want bitmap and failure progress preserved", progress)
	}

	finalized, err := store.FinalizeWaitingDatasetPeriods(ctx, time.Unix(deadline+1, 0).UTC(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if finalized != 1 {
		t.Fatalf("finalized periods=%d after no-op restore, want 1", finalized)
	}
	if got := countOutboxEvent(t, store, events.DatasetPeriodCompleted.Name()); got != 1 {
		t.Fatalf("collector period markers=%d after no-op restore, want 1", got)
	}
}

func TestDeleteDatasetRowsWaitingForPublicationLockHonorsCancellation(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := &pb.RowKey{SpaceId: "s", DatasetId: "busy", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-08-16T00:00:00Z"}}}
	if err := upsertTestRows(store, context.Background(), []*pb.RowFieldUpsert{{Key: key, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}}}}); err != nil {
		t.Fatal(err)
	}
	// 模拟 Outbox 正持有同 Dataset 的发布锁等待远端 ACK
	unlock, err := store.lockOutboxDatasetPublication(context.Background(), "s", "busy")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := store.DeleteDatasetRows(ctx, "s", "busy"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("delete error = %v, want deadline exceeded", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatalf("删除等待发布锁未响应取消：%s", time.Since(started))
	}
	unlock()
	// 超时的删除不能在锁释放后迟到执行
	rows, err := store.ReadRows(context.Background(), []*pb.RowKey{key}, []string{"close"}, nil)
	if err != nil || len(rows) != 1 || len(rows[0].GetFields()) != 1 {
		t.Fatalf("超时的删除不应改动数据: rows=%v err=%v", rows, err)
	}
	if len(store.outboxPublicationLocks) != 0 {
		t.Fatalf("发布锁引用未回收: %d", len(store.outboxPublicationLocks))
	}
}
