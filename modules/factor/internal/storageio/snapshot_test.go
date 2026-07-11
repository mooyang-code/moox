package storageio

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
)

func TestSnapshotStoreDeduplicatesAndValidatesFrame(t *testing.T) {
	store := NewSnapshotStore(t.TempDir())
	frame := &engine.DataFrame{Columns: []string{"close"}, Rows: [][]any{{1.0}, {2.0}}, DataTimes: []time.Time{time.Unix(0, 0), time.Unix(60, 0)}}
	task := engine.FactorTask{TaskID: "p1", SubjectID: "BTC", Freq: "1m"}
	a, err := store.AcquireFrame(context.Background(), task, frame)
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.AcquireFrame(context.Background(), task, frame)
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash != b.Hash || a.Path != b.Path {
		t.Fatalf("snapshot not deduplicated: %+v %+v", a, b)
	}
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	if err := b.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireFrame(context.Background(), task, &engine.DataFrame{Columns: []string{"close"}, Rows: [][]any{{1.0}}, DataTimes: nil}); err == nil {
		t.Fatal("expected rows/times mismatch")
	}
}
