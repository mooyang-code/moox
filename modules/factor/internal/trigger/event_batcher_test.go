package trigger

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

type pendingStoreFake struct {
	mu        sync.Mutex
	rows      map[string]pendingStoreRow
	processed map[string]struct{}
	commitErr error
}

type pendingStoreRow struct {
	event      *storagepb.RowsUpserted
	receivedAt time.Time
}

func (s *pendingStoreFake) ClaimPendingEvent(_ context.Context, id string, event *storagepb.RowsUpserted, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows == nil {
		s.rows = map[string]pendingStoreRow{}
	}
	if _, ok := s.processed[id]; ok {
		return false, nil
	}
	if _, ok := s.rows[id]; ok {
		return false, nil
	}
	s.rows[id] = pendingStoreRow{event: proto.Clone(event).(*storagepb.RowsUpserted), receivedAt: at}
	return true, nil
}

func (s *pendingStoreFake) LoadPendingEvents(_ context.Context, visit func(string, *storagepb.RowsUpserted, time.Time) error) error {
	for id, row := range s.rows {
		if err := visit(id, proto.Clone(row.event).(*storagepb.RowsUpserted), row.receivedAt); err != nil {
			return err
		}
	}
	return nil
}

func (s *pendingStoreFake) CommitPendingEvents(_ context.Context, ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.commitErr != nil {
		return s.commitErr
	}
	if s.processed == nil {
		s.processed = map[string]struct{}{}
	}
	for _, id := range ids {
		s.processed[id] = struct{}{}
		delete(s.rows, id)
	}
	return nil
}

func TestDurableEventBatcherClaimsDuplicateMessageOnlyOnce(t *testing.T) {
	now := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	inbox := &pendingStoreFake{}
	d := NewDurableEventBatcher(2*time.Second, []domain.FactorBinding{binding("bias", "binance_spot_kline", domain.SubjectModeAll, "[]")}, inbox)
	e := event("crypto", "binance_spot_kline", "BTC-USDT", "1m", now)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := d.IngestMessage(context.Background(), "same-message", e, now); err != nil {
				t.Errorf("claim duplicate event: %v", err)
			}
		}()
	}
	wg.Wait()
	tasks, err := d.FlushPending(context.Background(), now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || len(tasks[0].PendingEventIDs) != 1 {
		t.Fatalf("tasks = %+v, want one task with one pending event", tasks)
	}
}

func TestDurableEventBatcherPersistsAndReplaysBeforeFlush(t *testing.T) {
	now := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	bindings := []domain.FactorBinding{binding("bias", "binance_spot_kline", domain.SubjectModeAll, "[]")}
	inbox := &pendingStoreFake{}
	e := event("crypto", "binance_spot_kline", "BTC-USDT", "1m", now)
	first := NewDurableEventBatcher(2*time.Second, bindings, inbox)
	if err := first.IngestMessage(context.Background(), "message-1", e, now); err != nil {
		t.Fatal(err)
	}
	if len(inbox.rows) != 1 {
		t.Fatalf("pending rows = %d, want 1", len(inbox.rows))
	}
	restarted := NewDurableEventBatcher(2*time.Second, bindings, inbox)
	if err := restarted.Replay(context.Background()); err != nil {
		t.Fatal(err)
	}
	tasks, err := restarted.FlushPending(context.Background(), now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || len(inbox.rows) != 1 {
		t.Fatalf("tasks=%+v pending=%d, want one task and retained inbox", tasks, len(inbox.rows))
	}
	if err := restarted.CommitPending(context.Background(), tasks...); err != nil {
		t.Fatal(err)
	}
	if len(inbox.rows) != 0 || len(inbox.processed) != 1 {
		t.Fatalf("pending=%d processed=%d, want committed event", len(inbox.rows), len(inbox.processed))
	}
}

func TestDurableEventBatcherRestoresPendingWhenCommitFails(t *testing.T) {
	now := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	inbox := &pendingStoreFake{commitErr: errors.New("delete failed")}
	d := NewDurableEventBatcher(2*time.Second, []domain.FactorBinding{binding("bias", "binance_spot_kline", domain.SubjectModeAll, "[]")}, inbox)
	if err := d.IngestMessage(context.Background(), "message-1", event("crypto", "binance_spot_kline", "BTC-USDT", "1m", now), now); err != nil {
		t.Fatal(err)
	}
	tasks, err := d.FlushPending(context.Background(), now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CommitPending(context.Background(), tasks...); !errors.Is(err, inbox.commitErr) {
		t.Fatalf("commit error = %v, want %v", err, inbox.commitErr)
	}
	if err := d.RestorePending(context.Background()); err != nil {
		t.Fatal(err)
	}
	restored, err := d.FlushPending(context.Background(), now.Add(6*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 || len(restored[0].PendingEventIDs) != 1 {
		t.Fatalf("restored tasks = %+v, want pending event restored", restored)
	}
}

func TestDurableEventBatcherSkipsProcessedRedelivery(t *testing.T) {
	now := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	inbox := &pendingStoreFake{}
	d := NewDurableEventBatcher(2*time.Second, []domain.FactorBinding{binding("bias", "binance_spot_kline", domain.SubjectModeAll, "[]")}, inbox)
	e := event("crypto", "binance_spot_kline", "BTC-USDT", "1m", now)
	if err := d.IngestMessage(context.Background(), "message-1", e, now); err != nil {
		t.Fatal(err)
	}
	tasks, err := d.FlushPending(context.Background(), now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CommitPending(context.Background(), tasks...); err != nil {
		t.Fatal(err)
	}
	if err := d.IngestMessage(context.Background(), "message-1", e, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if tasks, err := d.FlushPending(context.Background(), now.Add(6*time.Second)); err != nil || len(tasks) != 0 {
		t.Fatalf("redelivery tasks=%+v err=%v, want no task", tasks, err)
	}
}

func TestEventBatcherDropsNonBoundAndResultDatasets(t *testing.T) {
	now := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	d := NewEventBatcher(2*time.Second, []domain.FactorBinding{
		binding("bias", "binance_spot_kline", domain.SubjectModeAll, "[]"),
	})

	d.Ingest(event("crypto", "unbound_kline", "BTC-USDT", "1m", now), now)
	d.Ingest(event("crypto", "binance_spot_factor", "BTC-USDT", "1m", now), now)

	if tasks := d.Flush(now.Add(3 * time.Second)); len(tasks) != 0 {
		t.Fatalf("tasks = %+v", tasks)
	}
}

func TestEventBatcherGroupsByScopeAtMaxDataTime(t *testing.T) {
	now := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	d := NewEventBatcher(2*time.Second, []domain.FactorBinding{
		binding("bias", "binance_spot_kline", domain.SubjectModeAll, "[]"),
		binding("cci", "binance_spot_kline", domain.SubjectModeAll, "[]"),
	})

	d.Ingest(event("crypto", "binance_spot_kline", "BTC-USDT", "1m", now), now)
	d.Ingest(event("crypto", "binance_spot_kline", "BTC-USDT", "1m", now.Add(time.Minute)), now.Add(time.Second))

	tasks := d.Flush(now.Add(3 * time.Second))
	if len(tasks) != 1 {
		t.Fatalf("tasks len = %d", len(tasks))
	}
	task := tasks[0]
	if task.BarTime != now.Add(time.Minute) {
		t.Fatalf("bar time = %s", task.BarTime)
	}
	if task.SpaceID != "crypto" || task.SourceDataset != "binance_spot_kline" || task.SubjectID != "BTC-USDT" || task.Freq != "1m" {
		t.Fatalf("task scope = %+v", task)
	}
	if len(task.FactorIDs) != 2 || task.FactorIDs[0] != "bias" || task.FactorIDs[1] != "cci" {
		t.Fatalf("factor ids = %#v", task.FactorIDs)
	}
}

func TestEventBatcherMarksDataOlderThanClosedWindowAsLateRecompute(t *testing.T) {
	start := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	batcher := NewEventBatcher(time.Second, []domain.FactorBinding{binding("bias", "binance_spot_kline", domain.SubjectModeAll, "[]")})
	batcher.Ingest(event("crypto", "binance_spot_kline", "BTC-USDT", "1m", start), start)
	first := batcher.Flush(start.Add(time.Second))
	if len(first) != 1 || first[0].LateData {
		t.Fatalf("first task=%+v", first)
	}
	batcher.Ingest(event("crypto", "binance_spot_kline", "BTC-USDT", "1m", start.Add(-time.Minute)), start.Add(2*time.Second))
	late := batcher.Flush(start.Add(3 * time.Second))
	if len(late) != 1 || !late[0].LateData || late[0].LateDataPolicy != LateDataPolicyRecompute {
		t.Fatalf("late task=%+v", late)
	}
}

func TestEventBatcherHonorsIncludeModeSubjects(t *testing.T) {
	now := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	d := NewEventBatcher(2*time.Second, []domain.FactorBinding{
		binding("bias", "binance_spot_kline", domain.SubjectModeInclude, `["ETH-USDT"]`),
	})

	d.Ingest(event("crypto", "binance_spot_kline", "BTC-USDT", "1m", now), now)
	d.Ingest(event("crypto", "binance_spot_kline", "ETH-USDT", "1m", now), now)

	tasks := d.Flush(now.Add(3 * time.Second))
	if len(tasks) != 1 || tasks[0].SubjectID != "ETH-USDT" {
		t.Fatalf("tasks = %+v", tasks)
	}
}

func TestEventBatcherUsesFixedWindowFromFirstEvent(t *testing.T) {
	now := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	window := 2 * time.Second
	d := NewEventBatcher(window, []domain.FactorBinding{
		binding("bias", "binance_spot_kline", domain.SubjectModeAll, "[]"),
	})

	d.Ingest(event("crypto", "binance_spot_kline", "BTC-USDT", "1m", now), now)
	d.Ingest(event("crypto", "binance_spot_kline", "BTC-USDT", "1m", now.Add(time.Minute)), now.Add(window-time.Millisecond))

	tasks := d.Flush(now.Add(window))
	if len(tasks) != 1 {
		t.Fatalf("tasks len = %d, want 1", len(tasks))
	}
	if tasks[0].BarTime != now.Add(time.Minute) {
		t.Fatalf("bar time = %s", tasks[0].BarTime)
	}
}

func binding(factorID string, sourceDataset string, subjectMode string, subjectsJSON string) domain.FactorBinding {
	return domain.FactorBinding{
		BindingID:     "bind-" + factorID,
		FactorID:      factorID,
		SpaceID:       "crypto",
		SourceDataset: sourceDataset,
		Freq:          "1m",
		SubjectMode:   subjectMode,
		SubjectsJSON:  subjectsJSON,
		TargetDataset: "binance_spot_factor",
		Status:        domain.BindingStatusEnabled,
	}
}

func event(spaceID string, datasetID string, subjectID string, freq string, dataTime time.Time) *storagepb.RowsUpserted {
	return &storagepb.RowsUpserted{SpaceId: spaceID, DatasetId: datasetID, Rows: []*storagepb.RowFieldUpsert{{Key: &storagepb.RowKey{SpaceId: spaceID, DatasetId: datasetID, Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: subjectID, Freq: freq, DataTime: dataTime.Format(time.RFC3339)}}}}}}
}
