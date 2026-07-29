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

func TestEventBatcherSchedulesOlderRowWithoutExtraLateState(t *testing.T) {
	start := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	batcher := NewEventBatcher(time.Second, []domain.FactorBinding{binding("bias", "binance_spot_kline", domain.SubjectModeAll, "[]")})
	batcher.Add(event("crypto", "binance_spot_kline", "BTC-USDT", "1m", start), start)
	if tasks := flushPending(t, batcher, start.Add(time.Second)); len(tasks) != 1 {
		t.Fatalf("first tasks=%+v", tasks)
	}
	older := start.Add(-time.Minute)
	batcher.Add(event("crypto", "binance_spot_kline", "BTC-USDT", "1m", older), start.Add(2*time.Second))
	tasks := flushPending(t, batcher, start.Add(3*time.Second))
	if len(tasks) != 1 || !tasks[0].StartTime.Equal(older) || !tasks[0].EndTime.Equal(older.Add(time.Nanosecond)) {
		t.Fatalf("older-row tasks=%+v", tasks)
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
	if !tasks[0].StartTime.Equal(now) || !tasks[0].EndTime.Equal(now.Add(time.Minute).Add(time.Nanosecond)) {
		t.Fatalf("range = [%s,%s)", tasks[0].StartTime, tasks[0].EndTime)
	}
}

func TestEventBatcherMergesMultiRowEventIntoHalfOpenRange(t *testing.T) {
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	first := now
	middle := now.Add(time.Minute)
	last := now.Add(2 * time.Minute)
	batcher := NewEventBatcher(time.Second, []domain.FactorBinding{
		binding("bias", "binance_spot_kline", domain.SubjectModeAll, "[]"),
	})

	batcher.Add(multiRowEvent("crypto", "binance_spot_kline", "BTC-USDT", "1m", first, middle, last), now)
	tasks := flushPending(t, batcher, now.Add(time.Second))

	if len(tasks) != 1 {
		t.Fatalf("tasks=%+v, want one merged range", tasks)
	}
	if !tasks[0].StartTime.Equal(first) || !tasks[0].EndTime.Equal(last.Add(time.Nanosecond)) {
		t.Fatalf("range=[%s,%s), want [%s,%s)", tasks[0].StartTime, tasks[0].EndTime, first, last.Add(time.Nanosecond))
	}
}

func TestEventBatcherMergesDifferentTagsWithoutAddingTagToTaskScope(t *testing.T) {
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	payload := multiRowEvent(
		"crypto", "binance_spot_kline", "BTC-USDT", "1m",
		now, now.Add(time.Minute),
	)
	payload.Rows[0].Key.GetTimeSeries().SeriesTag = "venue:binance"
	payload.Rows[1].Key.GetTimeSeries().SeriesTag = "venue:okx"
	batcher := NewEventBatcher(time.Second, []domain.FactorBinding{
		binding("bias", "binance_spot_kline", domain.SubjectModeAll, "[]"),
	})

	batcher.Add(payload, now)
	tasks := flushPending(t, batcher, now.Add(time.Second))

	if len(tasks) != 1 {
		t.Fatalf("tasks=%+v, want one task across tags", tasks)
	}
	if !tasks[0].StartTime.Equal(now) ||
		!tasks[0].EndTime.Equal(now.Add(time.Minute).Add(time.Nanosecond)) {
		t.Fatalf("range=[%s,%s), want tag-independent event range", tasks[0].StartTime, tasks[0].EndTime)
	}
}

func TestEventBatcherDuplicateRowDoesNotExpandRangeOrCreateTask(t *testing.T) {
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	batcher := NewEventBatcher(time.Second, []domain.FactorBinding{
		binding("bias", "binance_spot_kline", domain.SubjectModeAll, "[]"),
	})

	batcher.Add(multiRowEvent("crypto", "binance_spot_kline", "BTC-USDT", "1m", now, now), now)
	tasks := flushPending(t, batcher, now.Add(time.Second))

	if len(tasks) != 1 {
		t.Fatalf("tasks=%+v, want one task", tasks)
	}
	if !tasks[0].StartTime.Equal(now) || !tasks[0].EndTime.Equal(now.Add(time.Nanosecond)) {
		t.Fatalf("range=[%s,%s), want duplicate timestamp not to expand it", tasks[0].StartTime, tasks[0].EndTime)
	}
	if extra := flushPending(t, batcher, now.Add(2*time.Second)); len(extra) != 0 {
		t.Fatalf("extra tasks=%+v", extra)
	}
}

func TestEventBatcherSkipsZeroTimeWithoutPoisoningNormalRange(t *testing.T) {
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	zero := time.Time{}
	normal := now.Add(time.Minute)
	batcher := NewEventBatcher(time.Second, []domain.FactorBinding{
		binding("bias", "binance_spot_kline", domain.SubjectModeAll, "[]"),
	})

	batcher.Add(multiRowEvent("crypto", "binance_spot_kline", "BTC-USDT", "1m", zero, normal), now)
	tasks := flushPending(t, batcher, now.Add(time.Second))

	if len(tasks) != 1 {
		t.Fatalf("tasks=%+v, want only the normal timestamp", tasks)
	}
	if !tasks[0].StartTime.Equal(normal) || !tasks[0].EndTime.Equal(normal.Add(time.Nanosecond)) {
		t.Fatalf("range=[%s,%s), want [%s,%s)", tasks[0].StartTime, tasks[0].EndTime, normal, normal.Add(time.Nanosecond))
	}
	if got := batcher.RejectedTimeCount(); got != 1 {
		t.Fatalf("rejected time count=%d, want 1", got)
	}
}

func TestEventBatcherSkipsTimestampWhoseExclusiveEndExceedsRFC3339(t *testing.T) {
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	upper := time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)
	batcher := NewEventBatcher(time.Second, []domain.FactorBinding{
		binding("bias", "binance_spot_kline", domain.SubjectModeAll, "[]"),
	})

	batcher.Add(event("crypto", "binance_spot_kline", "BTC-USDT", "1m", upper), now)

	if tasks := flushPending(t, batcher, now.Add(time.Second)); len(tasks) != 0 {
		t.Fatalf("tasks=%+v, want invalid exclusive upper bound ignored", tasks)
	}
	if got := batcher.RejectedTimeCount(); got != 1 {
		t.Fatalf("rejected time count=%d, want 1", got)
	}
}

func TestEventBatcherDoesNotCountMalformedTimestampAsUnsupportedTime(t *testing.T) {
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	batcher := NewEventBatcher(time.Second, []domain.FactorBinding{
		binding("bias", "binance_spot_kline", domain.SubjectModeAll, "[]"),
	})
	payload := event("crypto", "binance_spot_kline", "BTC-USDT", "1m", now)
	payload.Rows[0].GetKey().GetTimeSeries().DataTime = "not-a-time"

	batcher.Add(payload, now)

	if tasks := flushPending(t, batcher, now.Add(time.Second)); len(tasks) != 0 {
		t.Fatalf("tasks=%+v, want malformed timestamp ignored", tasks)
	}
	if got := batcher.RejectedTimeCount(); got != 0 {
		t.Fatalf("rejected time count=%d, want malformed input excluded from supported-time counter", got)
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
	return multiRowEvent(spaceID, datasetID, subjectID, freq, dataTime)
}

func multiRowEvent(spaceID string, datasetID string, subjectID string, freq string, dataTimes ...time.Time) *storagepb.DatasetRowsUpserted {
	rows := make([]*storagepb.RowUpsert, 0, len(dataTimes))
	for _, dataTime := range dataTimes {
		rows = append(rows, &storagepb.RowUpsert{Key: &storagepb.RowKey{
			SpaceId: spaceID, DatasetId: datasetID,
			Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
				SubjectId: subjectID, Freq: freq, DataTime: dataTime.Format(time.RFC3339Nano),
			}},
		}})
	}
	return &storagepb.DatasetRowsUpserted{SpaceId: spaceID, DatasetId: datasetID, Rows: rows}
}
