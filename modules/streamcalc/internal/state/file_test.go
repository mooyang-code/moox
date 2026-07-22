package state

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/streamcalc/internal/aggregate"
)

func TestFileStoreRoundTrip(t *testing.T) {
	store := FileStore{Path: filepath.Join(t.TempDir(), "checkpoint.json")}
	want := aggregate.Snapshot{Windows: []aggregate.BarSnapshot{{Bar: aggregate.Bar{Key: aggregate.WindowKey{SpaceID: "crypto", Subject: "BTC-USDT", Frequency: "5m", Start: time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)}, InputIDs: map[string]struct{}{"e1": {}}}}}}
	if err := store.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Windows) != 1 || got.Windows[0].Bar.Key != want.Windows[0].Bar.Key || len(got.Windows[0].Bar.InputIDs) != 1 {
		t.Fatalf("checkpoint = %+v", got)
	}
}
