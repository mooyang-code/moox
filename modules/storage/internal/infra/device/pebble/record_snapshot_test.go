package pebble

import (
	"context"
	"strings"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestCurrentSnapshotIsStableAcrossConcurrentMutation(t *testing.T) {
	store := openRecordTestStore(t)
	ctx := context.Background()
	if _, err := store.ApplyRecordMutations(ctx, "request-1", []*pb.RecordMutation{testRecordMutation("request-1", "BTC-USDT", "old")}); err != nil {
		t.Fatal(err)
	}
	snapshotID, watermark, err := store.OpenRecordSnapshot(ctx, pb.RecordReadMode_RECORD_READ_MODE_CURRENT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.CloseRecordSnapshot(ctx, snapshotID)
	if watermark != 1 {
		t.Fatalf("snapshot watermark = %d, want 1", watermark)
	}
	if _, err := store.ApplyRecordMutations(ctx, "request-2", []*pb.RecordMutation{testRecordMutation("request-2", "BTC-USDT", "new")}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ReadRecordSnapshot(ctx, snapshotID, recordTarget(), []string{"BTC-USDT"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("snapshot rows = %+v, err=%v", rows, err)
	}
	if got := rows[0].GetColumns()[0].GetValue().GetStringValue(); got != "old" {
		t.Fatalf("snapshot value = %q, want old", got)
	}
}

func TestSnapshotWatermarkMatchesAtomicSnapshotBoundary(t *testing.T) {
	store := openRecordTestStore(t)
	ctx := context.Background()
	if _, err := store.ApplyRecordMutations(ctx, "request-1", []*pb.RecordMutation{testRecordMutation("request-1", "BTC-USDT", "one")}); err != nil {
		t.Fatal(err)
	}
	id, watermark, err := store.OpenRecordSnapshot(ctx, pb.RecordReadMode_RECORD_READ_MODE_CURRENT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.CloseRecordSnapshot(ctx, id)
	if _, err := store.ApplyRecordMutations(ctx, "request-2", []*pb.RecordMutation{testRecordMutation("request-2", "ETH-USDT", "two")}); err != nil {
		t.Fatal(err)
	}
	rows, _, err := store.ScanRecordSnapshot(ctx, id, recordTarget(), &pb.Page{Size: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || watermark != 1 || rows[0].GetKey().GetRecordId() != "BTC-USDT" {
		t.Fatalf("rows=%v watermark=%d", rows, watermark)
	}
}

func TestCurrentSnapshotPointReadsJoinSecondaryAtSameBoundary(t *testing.T) {
	store := openRecordTestStore(t)
	ctx := context.Background()
	if _, err := store.ApplyRecordMutations(ctx, "request-1", []*pb.RecordMutation{testRecordMutation("request-1", "BTC-USDT", "primary")}); err != nil {
		t.Fatal(err)
	}
	id, _, err := store.OpenRecordSnapshot(ctx, pb.RecordReadMode_RECORD_READ_MODE_CURRENT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.CloseRecordSnapshot(ctx, id)
	rows, err := store.ReadRecordSnapshot(ctx, id, recordTarget(), []string{"BTC-USDT", "MISSING"})
	if err != nil || len(rows) != 1 || rows[0].GetKey().GetRecordId() != "BTC-USDT" {
		t.Fatalf("point read = %+v, err=%v", rows, err)
	}
}

func TestHistorySnapshotUsesUpdatedAtRetentionRange(t *testing.T) {
	store := openRecordTestStore(t)
	ctx := context.Background()
	first, err := store.ApplyRecordMutations(ctx, "request-1", []*pb.RecordMutation{testRecordMutation("request-1", "BTC-USDT", "one")})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.ApplyRecordMutations(ctx, "request-2", []*pb.RecordMutation{testRecordMutation("request-2", "BTC-USDT", "two")})
	if err != nil {
		t.Fatal(err)
	}
	start := first.GetRows()[0].GetUpdatedAt()
	end := second.GetRows()[0].GetUpdatedAt()
	id, _, err := store.OpenRecordSnapshot(ctx, pb.RecordReadMode_RECORD_READ_MODE_HISTORY, &pb.TimeRange{StartTime: start, EndTime: end})
	if err != nil {
		t.Fatal(err)
	}
	defer store.CloseRecordSnapshot(ctx, id)
	rows, _, err := store.ScanRecordSnapshot(ctx, id, recordTarget(), &pb.Page{Size: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].GetRevision() != 1 || rows[1].GetRevision() != 2 {
		t.Fatalf("history rows = %+v", rows)
	}
}

func TestSnapshotCursorPagesWithoutDuplicates(t *testing.T) {
	store := openRecordTestStore(t)
	ctx := context.Background()
	for i, id := range []string{"A", "B", "C"} {
		requestID := "request-" + string(rune('1'+i))
		if _, err := store.ApplyRecordMutations(ctx, requestID, []*pb.RecordMutation{testRecordMutation(requestID, id, id)}); err != nil {
			t.Fatal(err)
		}
	}
	snapshotID, _, err := store.OpenRecordSnapshot(ctx, pb.RecordReadMode_RECORD_READ_MODE_CURRENT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.CloseRecordSnapshot(ctx, snapshotID)
	page := &pb.Page{Size: 1}
	seen := make(map[string]bool)
	for {
		rows, result, err := store.ScanRecordSnapshot(ctx, snapshotID, recordTarget(), page)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if seen[row.GetKey().GetRecordId()] {
				t.Fatalf("duplicate row %q", row.GetKey().GetRecordId())
			}
			seen[row.GetKey().GetRecordId()] = true
		}
		if !result.GetHasMore() {
			if result.GetNextCursor() != "" {
				t.Fatal("final snapshot page has cursor")
			}
			break
		}
		page = &pb.Page{Size: 1, Cursor: result.GetNextCursor()}
	}
	if len(seen) != 3 {
		t.Fatalf("seen rows = %v", seen)
	}
}

func TestSnapshotCursorRejectsTamperingOrDifferentBounds(t *testing.T) {
	store := openRecordTestStore(t)
	ctx := context.Background()
	for i, id := range []string{"A", "B"} {
		requestID := "request-" + string(rune('1'+i))
		if _, err := store.ApplyRecordMutations(ctx, requestID, []*pb.RecordMutation{testRecordMutation(requestID, id, id)}); err != nil {
			t.Fatal(err)
		}
	}
	snapshotID, _, err := store.OpenRecordSnapshot(ctx, pb.RecordReadMode_RECORD_READ_MODE_CURRENT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.CloseRecordSnapshot(ctx, snapshotID)
	_, result, err := store.ScanRecordSnapshot(ctx, snapshotID, recordTarget(), &pb.Page{Size: 1})
	if err != nil {
		t.Fatal(err)
	}
	tampered := result.GetNextCursor() + "x"
	if _, _, err := store.ScanRecordSnapshot(ctx, snapshotID, recordTarget(), &pb.Page{Size: 1, Cursor: tampered}); err == nil {
		t.Fatal("expected tampered cursor rejection")
	}
	other := recordTarget()
	other.DatasetId = "other"
	if _, _, err := store.ScanRecordSnapshot(ctx, snapshotID, other, &pb.Page{Size: 1, Cursor: result.GetNextCursor()}); err == nil {
		t.Fatal("expected changed bounds rejection")
	}
}

func TestSnapshotLeaseRenewsAndTerminalCloseReleasesResources(t *testing.T) {
	store := openRecordTestStore(t)
	ctx := context.Background()
	id, _, err := store.OpenRecordSnapshot(ctx, pb.RecordReadMode_RECORD_READ_MODE_CURRENT, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.getRecordSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	oldExpiry := snapshot.expiresAt
	if err := store.RenewRecordSnapshot(ctx, id); err != nil {
		t.Fatal(err)
	}
	if !snapshot.expiresAt.After(oldExpiry) {
		t.Fatal("snapshot lease did not renew")
	}
	if err := store.CloseRecordSnapshot(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.getRecordSnapshot(id); err == nil {
		t.Fatal("closed snapshot remained usable")
	}
}

func TestExpiredSnapshotFailsInsteadOfResuming(t *testing.T) {
	store := openRecordTestStore(t)
	ctx := context.Background()
	id, _, err := store.OpenRecordSnapshot(ctx, pb.RecordReadMode_RECORD_READ_MODE_CURRENT, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.getRecordSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.expiresAt = time.Now().Add(-time.Second)
	if _, _, err := store.ScanRecordSnapshot(ctx, id, recordTarget(), &pb.Page{Size: 1}); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired scan error = %v", err)
	}
}

func recordTarget() *pb.PrimaryStoreTarget {
	return &pb.PrimaryStoreTarget{SpaceId: "crypto", DatasetId: "symbols"}
}
