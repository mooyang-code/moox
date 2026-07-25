package jobstate

import (
	"context"
	"sync"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/packages/jetstream"
)

type memoryEntry struct {
	value []byte
	rev   uint64
}

func (e memoryEntry) Value() []byte    { return append([]byte(nil), e.value...) }
func (e memoryEntry) Revision() uint64 { return e.rev }

type memoryKV struct {
	mu   sync.Mutex
	data map[string]memoryEntry
}

func newMemoryKV() *memoryKV { return &memoryKV{data: map[string]memoryEntry{}} }
func (m *memoryKV) Create(key string, value []byte) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.data[key]; ok {
		return 0, jetstream.ErrKVKeyExists
	}
	m.data[key] = memoryEntry{value: append([]byte(nil), value...), rev: 1}
	return 1, nil
}
func (m *memoryKV) Get(key string) (jetstream.LegacyKVEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.data[key]
	if !ok {
		return nil, jetstream.ErrKVKeyNotFound
	}
	return entry, nil
}
func (m *memoryKV) Update(key string, value []byte, revision uint64) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.data[key]
	if !ok {
		return 0, jetstream.ErrKVKeyNotFound
	}
	if entry.rev != revision {
		return 0, jetstream.ErrKVKeyExists
	}
	entry.rev++
	entry.value = append([]byte(nil), value...)
	m.data[key] = entry
	return entry.rev, nil
}
func (m *memoryKV) Keys() ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.data))
	for key := range m.data {
		keys = append(keys, key)
	}
	return keys, nil
}

func TestMarkReportedFirstTerminalWins(t *testing.T) {
	store := NewKVStore(newMemoryKV(), Options{})
	item := &pb.JobItem{SpaceId: "crypto", JobId: "job-1", JobItemId: "item-1", JobType: "collect.kline", CodePackageId: "pkg"}
	if _, err := store.CreatePending(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	finished := time.Unix(100, 0).UTC()
	state, changed, err := store.MarkReported(context.Background(), ReportEvent{
		SpaceID: "crypto", JobItemID: "item-1", NodeID: "node-1", Status: StatusFailed,
		DurationMS: 25, Time: finished,
	})
	if err != nil || !changed || state.Status != StatusFailed {
		t.Fatalf("first report state=%+v changed=%v err=%v", state, changed, err)
	}
	state, changed, err = store.MarkReported(context.Background(), ReportEvent{
		SpaceID: "crypto", JobItemID: "item-1", NodeID: "node-2", Status: StatusSuccess, DurationMS: 50,
	})
	if err != nil || changed || state.Status != StatusFailed || state.ExecutionNode != "node-1" || state.DurationMS != 25 {
		t.Fatalf("late report state=%+v changed=%v err=%v", state, changed, err)
	}
}

func TestMarkReportedMissingIsIdempotent(t *testing.T) {
	store := NewKVStore(newMemoryKV(), Options{})
	state, changed, err := store.MarkReported(context.Background(), ReportEvent{
		SpaceID: "crypto", JobItemID: "missing", Status: StatusSuccess,
	})
	if err != nil || changed || state != nil {
		t.Fatalf("state=%+v changed=%v err=%v", state, changed, err)
	}
}
