package store

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestEventInboxPersistsAcrossRestartAndCommitsDuplicateIDsIdempotently(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/factor.db"
	now := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	event := &storagepb.DatasetFieldsChanged{SpaceId: "crypto", DatasetId: "kline"}

	first, err := Open(&Options{Path: path, MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	if err := first.PutPendingEvent(ctx, "message-1", event, now); err != nil {
		t.Fatal(err)
	}
	if err := first.PutPendingEvent(ctx, "message-1", &storagepb.DatasetFieldsChanged{SpaceId: "wrong"}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
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
	if err := restarted.LoadPendingEvents(ctx, func(id string, got *storagepb.DatasetFieldsChanged, receivedAt time.Time) error {
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
	processed, err := restarted.IsProcessedEvent(ctx, "message-1")
	if err != nil {
		t.Fatal(err)
	}
	if processed {
		t.Fatal("message is processed before commit")
	}

	if err := restarted.CommitPendingEvents(ctx, []string{"message-1", "message-1"}); err != nil {
		t.Fatal(err)
	}
	processed, err = restarted.IsProcessedEvent(ctx, "message-1")
	if err != nil {
		t.Fatal(err)
	}
	if !processed {
		t.Fatal("message is not marked processed")
	}
	loaded = 0
	if err := restarted.LoadPendingEvents(ctx, func(string, *storagepb.DatasetFieldsChanged, time.Time) error {
		loaded++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if loaded != 0 {
		t.Fatalf("pending events after commit = %d, want 0", loaded)
	}

	if err := restarted.PutPendingEvent(ctx, "message-1", event, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	loaded = 0
	if err := restarted.LoadPendingEvents(ctx, func(string, *storagepb.DatasetFieldsChanged, time.Time) error {
		loaded++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if loaded != 0 {
		t.Fatalf("redelivery reappeared after processed commit: %d", loaded)
	}
}
