package pebble

import (
	"context"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestSubjectSnapshotEffectiveLookupAndPeriodFreeze(t *testing.T) {
	store := openInputCommitStore(t)
	ctx := context.Background()
	scope := SubjectSnapshotScope{SpaceID: "crypto", DatasetID: "raw", Frequency: "1m"}

	first, err := store.PutSubjectSnapshot(ctx, scope, 100, []string{"ETH", "BTC", "BTC"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.GetPeriodSubjects(ctx, scope, 110)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.SubjectIDs, []string{"BTC", "ETH"}) || got.SnapshotID != first.SnapshotID || got.Frozen {
		t.Fatalf("resolved period=%+v", got)
	}

	frozen, err := store.FreezePeriodSubjects(ctx, scope, 110)
	if err != nil {
		t.Fatal(err)
	}
	if !frozen.Frozen || frozen.SnapshotID != first.SnapshotID {
		t.Fatalf("frozen period=%+v", frozen)
	}

	second, err := store.PutSubjectSnapshot(ctx, scope, 120, []string{"BTC", "SOL"})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err = store.FreezePeriodSubjects(ctx, scope, 110)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.SnapshotID != first.SnapshotID {
		t.Fatalf("frozen period changed to %s after newer snapshot %s", frozen.SnapshotID, second.SnapshotID)
	}

	next, err := store.FreezePeriodSubjects(ctx, scope, 120)
	if err != nil {
		t.Fatal(err)
	}
	if next.SnapshotID != second.SnapshotID || !reflect.DeepEqual(next.SubjectIDs, []string{"BTC", "SOL"}) {
		t.Fatalf("next period=%+v", next)
	}
}

func TestSubjectSnapshotRejectsMissingHistoryAndConflictingEffectivePeriod(t *testing.T) {
	store := openInputCommitStore(t)
	ctx := context.Background()
	scope := SubjectSnapshotScope{SpaceID: "crypto", DatasetID: "raw", Frequency: "1m"}
	if _, err := store.GetPeriodSubjects(ctx, scope, 100); err == nil {
		t.Fatal("missing snapshot must not resolve to an empty subject set")
	}
	if _, err := store.FreezePeriodSubjects(ctx, scope, 100); err == nil {
		t.Fatal("period cannot freeze without an applicable snapshot")
	}
	if _, err := store.PutSubjectSnapshot(ctx, scope, 100, []string{}); err != nil {
		t.Fatal(err)
	}
	empty, err := store.GetPeriodSubjects(ctx, scope, 100)
	if err != nil || empty.SnapshotID == "" || len(empty.SubjectIDs) != 0 {
		t.Fatalf("explicit empty snapshot=%+v err=%v", empty, err)
	}
	if _, err := store.PutSubjectSnapshot(ctx, scope, 100, []string{"BTC"}); err == nil {
		t.Fatal("a conflicting snapshot at the same effective period must be rejected")
	}
}

func TestSubjectSnapshotAndFrozenReferenceSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	store, err := Open(Options{Path: path, NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	scope := SubjectSnapshotScope{SpaceID: "crypto", DatasetID: "raw", Frequency: "1m"}
	if _, err := store.PutSubjectSnapshot(context.Background(), scope, 100, []string{"BTC", "ETH"}); err != nil {
		t.Fatal(err)
	}
	before, err := store.FreezePeriodSubjects(context.Background(), scope, 110)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(Options{Path: path, NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	after, err := store.FreezePeriodSubjects(context.Background(), scope, 110)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Frozen || after.SnapshotID != before.SnapshotID || !reflect.DeepEqual(after.SubjectIDs, before.SubjectIDs) {
		t.Fatalf("reopened period=%+v want %+v", after, before)
	}
}

func TestSubjectSnapshotConcurrentFreezeReturnsOneReference(t *testing.T) {
	store := openInputCommitStore(t)
	ctx := context.Background()
	scope := SubjectSnapshotScope{SpaceID: "crypto", DatasetID: "raw", Frequency: "1m"}
	if _, err := store.PutSubjectSnapshot(ctx, scope, 100, []string{"BTC", "ETH"}); err != nil {
		t.Fatal(err)
	}
	const workers = 12
	var wg sync.WaitGroup
	ids := make(chan string, workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			period, err := store.FreezePeriodSubjects(ctx, scope, 110)
			if err != nil {
				errs <- err
				return
			}
			ids <- period.SnapshotID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var got string
	for id := range ids {
		if got == "" {
			got = id
		} else if got != id {
			t.Fatalf("concurrent freezes returned %q and %q", got, id)
		}
	}
}
