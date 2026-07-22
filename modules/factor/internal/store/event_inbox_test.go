package store

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestEventInboxPersistsAcrossRestartAndCommitsDuplicateIDsIdempotently(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/factor.db"
	now := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	event := &storagepb.RowsUpserted{SpaceId: "crypto", DatasetId: "kline"}

	first, err := Open(&Options{Path: path, MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	claimed, err := first.ClaimPendingEvent(ctx, "message-1", event, now)
	if err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Fatal("first claim did not win")
	}
	claimed, err = first.ClaimPendingEvent(ctx, "message-1", &storagepb.RowsUpserted{SpaceId: "wrong"}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("duplicate claim won")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := Open(&Options{Path: path, MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if err := restarted.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	var loaded int
	if err := restarted.LoadPendingEvents(ctx, func(id string, got *storagepb.RowsUpserted, receivedAt time.Time) error {
		loaded++
		if id != "message-1" || got.GetSpaceId() != "crypto" || !receivedAt.Equal(now) {
			t.Fatalf("loaded event = id=%q event=%v received=%s", id, got, receivedAt)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if loaded != 1 {
		t.Fatalf("loaded pending events = %d, want 1", loaded)
	}
	if err := restarted.CommitPendingEvents(ctx, []string{"message-1", "message-1"}); err != nil {
		t.Fatal(err)
	}
	loaded = 0
	if err := restarted.LoadPendingEvents(ctx, func(string, *storagepb.RowsUpserted, time.Time) error {
		loaded++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if loaded != 0 {
		t.Fatalf("pending events after commit = %d, want 0", loaded)
	}

	claimed, err = restarted.ClaimPendingEvent(ctx, "message-1", event, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("processed redelivery claimed")
	}
	loaded = 0
	if err := restarted.LoadPendingEvents(ctx, func(string, *storagepb.RowsUpserted, time.Time) error {
		loaded++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if loaded != 0 {
		t.Fatalf("redelivery reappeared after processed commit: %d", loaded)
	}
}

func TestEventInboxConcurrentDuplicateClaimHasOneWinner(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/factor.db"
	db, err := Open(&Options{Path: path, MaxOpenConns: 8, MaxIdleConns: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	event := &storagepb.RowsUpserted{SpaceId: "crypto", DatasetId: "kline"}
	var wg sync.WaitGroup
	results := make(chan bool, 16)
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, err := db.ClaimPendingEvent(ctx, "same-message", event, time.Now().UTC())
			results <- claimed
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	winners := 0
	for claimed := range results {
		if claimed {
			winners++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("claim winners = %d, want 1", winners)
	}
}
