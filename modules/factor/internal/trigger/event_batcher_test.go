package trigger

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	storagepb "github.com/mooyang-code/moox/packages/storagepb"
)

func TestEventBatcherDropsNonBoundAndResultDatasets(t *testing.T) {
	now := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	d := NewEventBatcher(2*time.Second, []domain.FactorBinding{
		binding("bias", "binance_spot_kline", domain.SubjectModeAll, "[]"),
	})

	d.Add(event("crypto", "unbound_kline", "BTC-USDT", "1m", now), now)
	d.Add(event("crypto", "binance_spot_factor", "BTC-USDT", "1m", now), now)

	if tasks := flushPending(t, d, now.Add(3*time.Second)); len(tasks) != 0 {
		t.Fatalf("tasks = %+v", tasks)
	}
}

func TestEventBatcherKeepsCompleteScopeSeparate(t *testing.T) {
	now := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	d := NewEventBatcher(2*time.Second, []domain.FactorBinding{
		{
			FactorID: "bias", SpaceID: "crypto", SourceDataset: "source-a",
			TargetDataset: "target-a", Freq: "1m", SubjectMode: domain.SubjectModeAll,
			Status: domain.BindingStatusEnabled,
		},
		{
			FactorID: "cci", SpaceID: "crypto", SourceDataset: "source-a",
			TargetDataset: "target-b", Freq: "1m", SubjectMode: domain.SubjectModeAll,
			Status: domain.BindingStatusEnabled,
		},
		{
			FactorID: "rsi", SpaceID: "equities", SourceDataset: "source-b",
			TargetDataset: "target-c", Freq: "1m", SubjectMode: domain.SubjectModeAll,
			Status: domain.BindingStatusEnabled,
		},
	})

	d.Add(event("crypto", "source-a", "BTC-USDT", "1m", now), now)
	d.Add(event("equities", "source-b", "BTC-USDT", "1m", now), now)

	tasks := flushPending(t, d, now.Add(3*time.Second))
	if len(tasks) != 3 {
		t.Fatalf("tasks=%+v, want three complete scopes", tasks)
	}
	got := make(map[string]Task, len(tasks))
	for _, task := range tasks {
		key := task.SpaceID + "/" + task.SourceDataset + "/" + task.TargetDataset
		got[key] = task
	}
	want := map[string]string{
		"crypto/source-a/target-a":   "bias",
		"crypto/source-a/target-b":   "cci",
		"equities/source-b/target-c": "rsi",
	}
	for key, factorID := range want {
		task, ok := got[key]
		if !ok || task.SubjectID != "BTC-USDT" || task.Freq != "1m" ||
			len(task.FactorIDs) != 1 || task.FactorIDs[0] != factorID {
			t.Fatalf("scope %q task=%+v", key, task)
		}
	}
}

func TestEventBatcherSchedulesOlderBarWithoutExtraLateState(t *testing.T) {
	start := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	batcher := NewEventBatcher(time.Second, []domain.FactorBinding{binding("bias", "binance_spot_kline", domain.SubjectModeAll, "[]")})
	batcher.Add(event("crypto", "binance_spot_kline", "BTC-USDT", "1m", start), start)
	if tasks := flushPending(t, batcher, start.Add(time.Second)); len(tasks) != 1 {
		t.Fatalf("first tasks=%+v", tasks)
	}
	older := start.Add(-time.Minute)
	batcher.Add(event("crypto", "binance_spot_kline", "BTC-USDT", "1m", older), start.Add(2*time.Second))
	tasks := flushPending(t, batcher, start.Add(3*time.Second))
	if len(tasks) != 1 || !tasks[0].BarTime.Equal(older) {
		t.Fatalf("older-bar tasks=%+v", tasks)
	}
}

func TestEventBatcherHonorsIncludeModeSubjects(t *testing.T) {
	now := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	d := NewEventBatcher(2*time.Second, []domain.FactorBinding{
		binding("bias", "binance_spot_kline", domain.SubjectModeInclude, `["ETH-USDT"]`),
	})

	d.Add(event("crypto", "binance_spot_kline", "BTC-USDT", "1m", now), now)
	d.Add(event("crypto", "binance_spot_kline", "ETH-USDT", "1m", now), now)

	tasks := flushPending(t, d, now.Add(3*time.Second))
	if len(tasks) != 1 || tasks[0].SubjectID != "ETH-USDT" {
		t.Fatalf("tasks = %+v", tasks)
	}
}

func TestEventBatcherDiscardsBucketAfterBindingIsDisabled(t *testing.T) {
	now := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	active := binding("bias", "binance_spot_kline", domain.SubjectModeAll, "[]")
	d := NewEventBatcher(time.Second, []domain.FactorBinding{active})

	d.Add(event("crypto", "binance_spot_kline", "BTC-USDT", "1m", now), now)
	active.Status = domain.BindingStatusDisabled
	d.SetBindings([]domain.FactorBinding{active})

	if tasks := flushPending(t, d, now.Add(2*time.Second)); len(tasks) != 0 {
		t.Fatalf("tasks = %+v", tasks)
	}
}

func TestEventBatcherUsesFixedWindowFromFirstEvent(t *testing.T) {
	now := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	window := 2 * time.Second
	d := NewEventBatcher(window, []domain.FactorBinding{
		binding("bias", "binance_spot_kline", domain.SubjectModeAll, "[]"),
	})

	d.Add(event("crypto", "binance_spot_kline", "BTC-USDT", "1m", now), now)
	d.Add(event("crypto", "binance_spot_kline", "BTC-USDT", "1m", now.Add(time.Minute)), now.Add(window-time.Millisecond))

	tasks := flushPending(t, d, now.Add(window))
	if len(tasks) != 1 {
		t.Fatalf("tasks len = %d, want 1", len(tasks))
	}
	if tasks[0].BarTime != now.Add(time.Minute) {
		t.Fatalf("bar time = %s", tasks[0].BarTime)
	}
}

func flushPending(t *testing.T, batcher *EventBatcher, now time.Time) []Task {
	t.Helper()
	return batcher.Flush(now)
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

func event(spaceID string, datasetID string, subjectID string, freq string, dataTime time.Time) *storagepb.DatasetRowsUpserted {
	return &storagepb.DatasetRowsUpserted{SpaceId: spaceID, DatasetId: datasetID, Rows: []*storagepb.RowUpsert{{Key: &storagepb.RowKey{SpaceId: spaceID, DatasetId: datasetID, Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: subjectID, Freq: freq, DataTime: dataTime.Format(time.RFC3339)}}}}}}
}
