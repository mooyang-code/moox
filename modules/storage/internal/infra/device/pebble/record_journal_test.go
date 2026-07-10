package pebble

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestScanRecordJournalExclusiveLowerInclusiveUpper(t *testing.T) {
	store := openRecordTestStore(t)
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		requestID := "request-" + string(rune('0'+i))
		if _, err := store.ApplyRecordMutations(ctx, requestID, []*pb.RecordMutation{testRecordMutation(requestID, "R", requestID)}); err != nil {
			t.Fatal(err)
		}
	}
	events, scanned, result, err := store.ScanRecordJournal(ctx, 1, 3, &pb.Page{Size: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].GetCommitSeq() != 2 || events[1].GetCommitSeq() != 3 || scanned != 3 || result.GetHasMore() {
		t.Fatalf("events=%v scanned=%d result=%v", events, scanned, result)
	}
	if len(events[0].GetRows()) != 1 || events[0].GetRows()[0].GetCommitSeq() != 2 {
		t.Fatalf("journal event does not contain complete committed row: %+v", events[0])
	}
}

func TestScanRecordJournalPagesAndBindsCursor(t *testing.T) {
	store := openRecordTestStore(t)
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		requestID := "request-" + string(rune('0'+i))
		if _, err := store.ApplyRecordMutations(ctx, requestID, []*pb.RecordMutation{testRecordMutation(requestID, "R", requestID)}); err != nil {
			t.Fatal(err)
		}
	}
	first, scanned, result, err := store.ScanRecordJournal(ctx, 0, 3, &pb.Page{Size: 1})
	if err != nil || len(first) != 1 || scanned != 1 || !result.GetHasMore() {
		t.Fatalf("first page events=%v scanned=%d result=%v err=%v", first, scanned, result, err)
	}
	second, scanned, result, err := store.ScanRecordJournal(ctx, 0, 3, &pb.Page{Size: 1, Cursor: result.GetNextCursor()})
	if err != nil || len(second) != 1 || second[0].GetCommitSeq() != 2 || scanned != 2 || !result.GetHasMore() {
		t.Fatalf("second page events=%v scanned=%d result=%v err=%v", second, scanned, result, err)
	}
	if _, _, _, err := store.ScanRecordJournal(ctx, 0, 2, &pb.Page{Size: 1, Cursor: result.GetNextCursor()}); err == nil {
		t.Fatal("expected changed through to reject cursor")
	}
	third, scanned, result, err := store.ScanRecordJournal(ctx, 0, 3, &pb.Page{Size: 1, Cursor: result.GetNextCursor()})
	if err != nil || len(third) != 1 || third[0].GetCommitSeq() != 3 || scanned != 3 || result.GetHasMore() || result.GetNextCursor() != "" {
		t.Fatalf("third page events=%v scanned=%d result=%v err=%v", third, scanned, result, err)
	}
}

func TestScanRecordJournalRejectsTampering(t *testing.T) {
	store := openRecordTestStore(t)
	ctx := context.Background()
	if _, err := store.ApplyRecordMutations(ctx, "request-1", []*pb.RecordMutation{testRecordMutation("request-1", "R", "one")}); err != nil {
		t.Fatal(err)
	}
	_, _, result, err := store.ScanRecordJournal(ctx, 0, 1, &pb.Page{Size: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.ScanRecordJournal(ctx, 0, 1, &pb.Page{Size: 1, Cursor: result.GetNextCursor() + "x"}); err == nil {
		t.Fatal("expected tampered cursor rejection")
	}
}

func TestRecordWatermarkReturnsPersistentSource(t *testing.T) {
	store := openRecordTestStore(t)
	ctx := context.Background()
	source, watermark, err := store.RecordWatermark(ctx)
	if err != nil || source == "" || watermark != 0 {
		t.Fatalf("initial watermark source=%q seq=%d err=%v", source, watermark, err)
	}
	if _, err := store.ApplyRecordMutations(ctx, "request-1", []*pb.RecordMutation{testRecordMutation("request-1", "R", "one")}); err != nil {
		t.Fatal(err)
	}
	sourceAfter, watermark, err := store.RecordWatermark(ctx)
	if err != nil || sourceAfter != source || watermark != 1 {
		t.Fatalf("watermark source=%q seq=%d err=%v", sourceAfter, watermark, err)
	}
}
