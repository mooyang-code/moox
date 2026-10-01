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
	if err := store.UpsertFields(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{SpaceId: "s", DatasetId: "delete-me", Page: &pb.Page{Page: 1, Size: 10}}); err != nil {
		t.Fatal(err)
	}
	if deleted, err := store.DeleteDatasetRows(context.Background(), "s", "delete-me"); err != nil || deleted == 0 {
		t.Fatalf("deleted ranges=%d err=%v", deleted, err)
	}
	removed, err := store.ReadFields(context.Background(), []*pb.RowKey{rows[0].GetKey()}, []string{"close"}, nil)
	if err != nil || len(removed) != 1 || len(removed[0].GetFields()) != 0 {
		t.Fatalf("deleted dataset still has fields: rows=%v err=%v", removed, err)
	}
	remaining, err := store.ReadFields(context.Background(), []*pb.RowKey{rows[1].GetKey()}, []string{"close"}, nil)
	if err != nil || len(remaining) != 1 || len(remaining[0].GetFields()) != 1 {
		t.Fatalf("other dataset was removed: rows=%v err=%v", remaining, err)
	}
	history, err := store.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{SpaceId: "s", DatasetId: "delete-me", Page: &pb.Page{Page: 1, Size: 10}})
	if err != nil || len(history.GetRows()) != 0 {
		t.Fatalf("deleted dataset still has history: rows=%v err=%v", history, err)
	}
}

func TestDeleteDatasetRowsClearsSourceEventDedupe(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	row := &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: "s", DatasetId: "delete-me", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-08-16T00:00:00Z"}}}, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}}}
	build := func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return BuildDatasetRowsUpsertedMessage("node-1", spaceID, datasetID, rows)
	}
	if entries, err := store.UpsertFieldsEventWithSource(context.Background(), []*pb.RowFieldUpsert{row}, "source-event-1", build); err != nil || len(entries) != 1 {
		t.Fatalf("first source write entries=%d err=%v", len(entries), err)
	}
	if _, err := store.DeleteDatasetRows(context.Background(), "s", "delete-me"); err != nil {
		t.Fatal(err)
	}
	if err := store.RestoreDatasetRows(context.Background(), "s", "delete-me"); err != nil {
		t.Fatal(err)
	}
	if entries, err := store.UpsertFieldsEventWithSource(context.Background(), []*pb.RowFieldUpsert{row}, "source-event-1", build); err != nil || len(entries) != 1 {
		t.Fatalf("recreated source write entries=%d err=%v", len(entries), err)
	}
}

func TestDeleteDatasetRowsTombstoneBlocksWritesUntilRestore(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	row := &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: "s", DatasetId: "delete-me", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}, Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "v"}}}}}
	if err := store.UpsertFields(context.Background(), []*pb.RowFieldUpsert{row}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteDatasetRows(context.Background(), "s", "delete-me"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFields(context.Background(), []*pb.RowFieldUpsert{row}); err != ErrDatasetDeleted {
		t.Fatalf("write after delete err=%v, want ErrDatasetDeleted", err)
	}
	if err := store.RestoreDatasetRows(context.Background(), "s", "delete-me"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFields(context.Background(), []*pb.RowFieldUpsert{row}); err != nil {
		t.Fatalf("write after restore: %v", err)
	}
}

func TestDeleteDatasetRowsRemovesReceiptsMarkersAndOutbox(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := &pb.RowKey{SpaceId: "s", DatasetId: "delete-me", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}
	if _, err := store.CommitInput(context.Background(), InputCommit{CommitID: "receipt-1", RequiredFields: []string{"close"}, Row: &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}}}, WriteKind: WriteKindInputCommit}); err != nil {
		t.Fatal(err)
	}
	marker, _, err := BuildCollectorPeriodCompletedMessage("s", &pb.CollectorPeriodCompletedMarker{DatasetId: "delete-me", Frequency: "1m", PeriodTime: 1, Status: "complete", BatchId: "batch-1", ConfigSnapshotId: "cfg-1", ExpectedScopeRef: "scope-1", CollectedAt: timestamppb.New(time.Unix(1, 0))})
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
	if _, err := store.LookupWriteReceipt(context.Background(), "receipt-1"); err == nil {
		t.Fatal("write receipt survived dataset deletion")
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
	if got := countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()); got != 1 {
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
	if _, err := store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: row}}, "source-btc", "collector"); err != nil {
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
	if got := countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()); got != 1 {
		t.Fatalf("collector period markers=%d after no-op restore, want 1", got)
	}
}
