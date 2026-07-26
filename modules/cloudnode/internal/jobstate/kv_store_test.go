package jobstate

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type memoryKV struct {
	mu   sync.Mutex
	data map[string]jetstream.KVEntry
}

func newMemoryKV() *memoryKV { return &memoryKV{data: map[string]jetstream.KVEntry{}} }
func (m *memoryKV) Create(_ context.Context, key string, value []byte) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.data[key]; ok {
		return 0, jetstream.ErrKVKeyExists
	}
	m.data[key] = jetstream.KVEntry{Value: append([]byte(nil), value...), Revision: 1}
	return 1, nil
}
func (m *memoryKV) Get(_ context.Context, key string) (*jetstream.KVEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.data[key]
	if !ok {
		return nil, jetstream.ErrKVKeyNotFound
	}
	return &jetstream.KVEntry{Value: append([]byte(nil), entry.Value...), Revision: entry.Revision}, nil
}
func (m *memoryKV) Update(_ context.Context, key string, value []byte, revision uint64) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.data[key]
	if !ok {
		return 0, jetstream.ErrKVKeyNotFound
	}
	if entry.Revision != revision {
		return 0, jetstream.ErrKVKeyExists
	}
	entry.Revision++
	entry.Value = append([]byte(nil), value...)
	m.data[key] = entry
	return entry.Revision, nil
}
func (m *memoryKV) Keys(_ context.Context) ([]string, error) {
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
	item := &pb.JobItem{SpaceId: "crypto", JobId: "job-1", JobItemId: "item-1", JobType: "collect.kline"}
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

func TestCreatePendingRepublishesOnlyEnqueueFailedDuplicate(t *testing.T) {
	store := NewKVStore(newMemoryKV(), Options{})
	item := &pb.JobItem{SpaceId: "crypto", JobId: "job-1", JobItemId: "item-1", JobType: "collect.kline"}
	first, err := store.CreatePending(context.Background(), item)
	if err != nil || !first.ShouldPublish {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	duplicate, err := store.CreatePending(context.Background(), item)
	if err != nil || !duplicate.Deduplicated || duplicate.ShouldPublish {
		t.Fatalf("pending duplicate=%+v err=%v", duplicate, err)
	}
	if err := store.MarkEnqueueFailed(context.Background(), "crypto", "item-1", "publish failed"); err != nil {
		t.Fatal(err)
	}
	retry, err := store.CreatePending(context.Background(), item)
	if err != nil || !retry.Created || !retry.ShouldPublish {
		t.Fatalf("enqueue_failed retry=%+v err=%v", retry, err)
	}
	if _, _, err := store.MarkReported(context.Background(), ReportEvent{
		SpaceID: "crypto", JobItemID: "item-1", Status: StatusSuccess,
	}); err != nil {
		t.Fatal(err)
	}
	terminal, err := store.CreatePending(context.Background(), item)
	if err != nil || !terminal.Deduplicated || terminal.ShouldPublish {
		t.Fatalf("terminal duplicate=%+v err=%v", terminal, err)
	}
}

func TestCreatePendingPersistsExecuteAt(t *testing.T) {
	store := NewKVStore(newMemoryKV(), Options{})
	executeAt := time.Date(2026, 7, 26, 9, 30, 0, 123, time.FixedZone("CST", 8*60*60))
	item := &pb.JobItem{
		SpaceId: "crypto", JobId: "job-1", JobItemId: "item-1", JobType: "collect.kline",
		ExecuteAt: timestamppb.New(executeAt),
	}
	if _, err := store.CreatePending(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	state, err := store.Get(context.Background(), "crypto", "item-1")
	if err != nil {
		t.Fatal(err)
	}
	if state.ExecuteAt == nil || !state.ExecuteAt.Equal(executeAt.UTC()) || state.ExecuteAt.Location() != time.UTC {
		t.Fatalf("execute_at = %v, want %v in UTC", state.ExecuteAt, executeAt.UTC())
	}
}

func TestCreatePendingWithoutExecuteAtMeansImmediate(t *testing.T) {
	store := NewKVStore(newMemoryKV(), Options{})
	item := &pb.JobItem{
		SpaceId: "crypto", JobId: "job-1", JobItemId: "item-1", JobType: "collect.kline",
	}
	if _, err := store.CreatePending(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	state, err := store.Get(context.Background(), "crypto", "item-1")
	if err != nil {
		t.Fatal(err)
	}
	if state.ExecuteAt != nil {
		t.Fatalf("execute_at = %v, want nil immediate execution", state.ExecuteAt)
	}
}

func TestJobItemDetailReturnsExecuteAt(t *testing.T) {
	executeAt := time.Date(2026, 7, 26, 1, 30, 0, 0, time.UTC)
	detail := (State{ExecuteAt: &executeAt}).ToDetail()
	if detail.GetExecuteAt() == nil || !detail.GetExecuteAt().AsTime().Equal(executeAt) {
		t.Fatalf("execute_at = %v, want %v", detail.GetExecuteAt(), executeAt)
	}
	if immediate := (State{}).ToDetail(); immediate.GetExecuteAt() != nil {
		t.Fatalf("missing execute_at became %v", immediate.GetExecuteAt())
	}
}

func TestCreatePendingRejectsInvalidExecuteAt(t *testing.T) {
	store := NewKVStore(newMemoryKV(), Options{})
	item := &pb.JobItem{
		SpaceId: "crypto", JobId: "job-1", JobItemId: "item-1", JobType: "collect.kline",
		ExecuteAt: &timestamppb.Timestamp{Seconds: 253402300800},
	}
	if _, err := store.CreatePending(context.Background(), item); !errors.Is(err, ErrInvalid) {
		t.Fatalf("CreatePending() error = %v, want ErrInvalid", err)
	}
}
