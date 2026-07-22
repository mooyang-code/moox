package trigger

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

type pendingStoreFake struct {
	rows map[string]pendingStoreRow
}

type pendingStoreRow struct {
	event      *storagepb.DatasetFieldsChanged
	receivedAt time.Time
}

func (s *pendingStoreFake) PutPendingEvent(_ context.Context, id string, event *storagepb.DatasetFieldsChanged, at time.Time) error {
	if s.rows == nil {
		s.rows = map[string]pendingStoreRow{}
	}
	if _, ok := s.rows[id]; !ok {
		s.rows[id] = pendingStoreRow{event: proto.Clone(event).(*storagepb.DatasetFieldsChanged), receivedAt: at}
	}
	return nil
}

func (s *pendingStoreFake) LoadPendingEvents(_ context.Context, visit func(string, *storagepb.DatasetFieldsChanged, time.Time) error) error {
	for id, row := range s.rows {
		if err := visit(id, proto.Clone(row.event).(*storagepb.DatasetFieldsChanged), row.receivedAt); err != nil {
			return err
		}
	}
	return nil
}

func (s *pendingStoreFake) DeletePendingEvents(_ context.Context, ids []string) error {
	for _, id := range ids {
		delete(s.rows, id)
	}
	return nil
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
	if len(tasks) != 1 || len(inbox.rows) != 0 {
		t.Fatalf("tasks=%+v pending=%d, want one task and empty inbox", tasks, len(inbox.rows))
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

func event(spaceID string, datasetID string, subjectID string, freq string, dataTime time.Time) *storagepb.DatasetFieldsChanged {
	return &storagepb.DatasetFieldsChanged{SpaceId: spaceID, DatasetId: datasetID, Rows: []*storagepb.RowFieldUpsert{{Key: &storagepb.RowKey{SpaceId: spaceID, DatasetId: datasetID, Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: subjectID, Freq: freq, DataTime: dataTime.Format(time.RFC3339)}}}}}}
}
