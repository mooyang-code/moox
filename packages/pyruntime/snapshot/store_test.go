package snapshot

import (
	"context"
	"github.com/mooyang-code/moox/packages/pyruntime/transport"
	"github.com/stretchr/testify/assert"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAcquireSharesContentAddressedFile(t *testing.T) {
	s := NewStore(t.TempDir())
	k := Key{Namespace: "factor", DataRevision: "r1", SchemaHash: "s1"}
	a, e := s.Acquire(context.Background(), k, []byte("data"), 1)
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.Acquire(context.Background(), k, []byte("data"), 1)
	if e != nil || a.Path != b.Path {
		t.Fatalf("a=%+v b=%+v err=%v", a, b, e)
	}
	_ = a.Release()
	_ = b.Release()
}

func TestHandleReleaseIsIdempotentConcurrently(t *testing.T) {
	s := NewStore(t.TempDir())
	h, err := s.Acquire(context.Background(), Key{Namespace: "factor", DataRevision: "r1"}, []byte("x"), 1)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := h.Release(); err != nil {
				t.Errorf("release: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestReapRemovesOldOrphanSnapshot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "orphan.snapshot")
	if err := os.WriteFile(path, []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := NewStore(root).Reap(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("orphan snapshot still exists: %v", err)
	}
}

func TestAcquireArrowAndOpenMapped(t *testing.T) {
	s := NewStore(t.TempDir())
	want := transport.Table{Columns: []string{"close", "volume"}, Rows: [][]any{{1.25, int64(10)}, {2.5, int64(11)}}}
	h, err := s.AcquireArrow(context.Background(), Key{Namespace: "factor", DataRevision: "r1", SchemaHash: "schema"}, want)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Release()
	if h.Encoding != transport.ArrowMMap || h.Rows != 2 {
		t.Fatalf("unexpected handle: %+v", h)
	}
	mapped, err := s.Open(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	reader := mapped.Reader()
	if reader.NumRecords() != 1 || reader.Schema().Field(0).Name != "close" {
		_ = mapped.Close()
		t.Fatalf("unexpected mapped reader: records=%d schema=%v", reader.NumRecords(), reader.Schema())
	}
	record, err := reader.RecordBatchAt(0)
	if err != nil {
		_ = mapped.Close()
		t.Fatal(err)
	}
	if record.NumRows() != 2 {
		record.Release()
		_ = mapped.Close()
		t.Fatalf("unexpected row count: %d", record.NumRows())
	}
	record.Release()
	if err := h.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(h.Path); err != nil {
		t.Fatalf("mapped reference should keep snapshot alive: %v", err)
	}
	if err := mapped.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(h.Path); !os.IsNotExist(err) {
		t.Fatalf("snapshot should be removed after mapped close: %v", err)
	}
}

func TestMappedSchemaNilReceiver_ShouldReturnNil(t *testing.T) {
	var mapped *Mapped
	assert.Nil(t, mapped.Schema())
	assert.Nil(t, mapped.Reader())
}

func TestHandleAndMappedNilGuards(t *testing.T) {
	var h *Handle
	assert.NoError(t, h.Release())

	var m *Mapped
	assert.Nil(t, m.Reader())
	assert.Nil(t, m.Schema())
	assert.NoError(t, m.Close())
}
