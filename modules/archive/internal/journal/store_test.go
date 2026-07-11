package journal

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/domain"
)

func TestAppendEventIsAtomicAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t, dir)
	batch := fixtureBatch("message-1", twoPartitions())
	result, err := store.Append(context.Background(), batch)
	if err != nil || result.Seq != 1 || result.Duplicate {
		t.Fatalf("Append() = %#v, %v", result, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store = openTestStore(t, dir)
	states, err := store.DirtyPartitions(context.Background(), 10)
	if err != nil || len(states) != 2 || states[0].HighWaterSeq != 1 || states[1].HighWaterSeq != 1 {
		t.Fatalf("DirtyPartitions() = %#v, %v", states, err)
	}
}

func TestCompleteOnlyDeletesPendingThroughCapturedHighWater(t *testing.T) {
	store := openTestStore(t, t.TempDir())
	key := fixturePartition()
	first, err := store.Append(context.Background(), fixtureBatch("m1", []domain.PartitionKey{key}))
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := store.BeginMaterialization(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Append(context.Background(), fixtureBatch("m2", []domain.PartitionKey{key}))
	if err != nil {
		t.Fatal(err)
	}
	if first.Seq != attempt.ThroughSeq || second.Seq <= attempt.ThroughSeq {
		t.Fatalf("sequence order first=%d through=%d second=%d", first.Seq, attempt.ThroughSeq, second.Seq)
	}
	if err := store.Complete(context.Background(), key, attempt.ThroughSeq); err != nil {
		t.Fatal(err)
	}
	pending, err := store.Pending(context.Background(), key, math.MaxUint64)
	if err != nil || len(pending) != 1 || pending[0].Seq != second.Seq {
		t.Fatalf("pending after Complete = %#v, %v", pending, err)
	}
}

func TestAppendDuplicateMessageReturnsReceiptWithoutNewPending(t *testing.T) {
	store := openTestStore(t, t.TempDir())
	batch := fixtureBatch("same-message", []domain.PartitionKey{fixturePartition()})
	first, err := store.Append(context.Background(), batch)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Append(context.Background(), batch)
	if err != nil || !second.Duplicate || second.Seq != first.Seq || len(second.Partitions) != 0 {
		t.Fatalf("duplicate Append() = %#v, %v", second, err)
	}
	pending, err := store.Pending(context.Background(), fixturePartition(), math.MaxUint64)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %#v, %v", pending, err)
	}
}

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func fixturePartition() domain.PartitionKey {
	return domain.PartitionKey{SpaceID: "crypto_binance", DatasetID: "spot_kline", SubjectID: "BTC-USDT", Freq: "1m", Month: "202606"}
}

func twoPartitions() []domain.PartitionKey {
	return []domain.PartitionKey{fixturePartition(), {SpaceID: "crypto_binance", DatasetID: "spot_kline", SubjectID: "BTC-USDT", Freq: "1h", Month: "202606"}}
}

func fixtureBatch(messageID string, partitions []domain.PartitionKey) domain.EventBatch {
	rows := make([]domain.RowPatch, 0, len(partitions))
	for i, partition := range partitions {
		value := float64(i + 1)
		rows = append(rows, domain.RowPatch{Partition: partition, DataTime: time.Date(2026, 6, 1, 0, i, 0, 0, time.UTC), DimensionsJSON: "{}", Attributes: map[string]string{}, WrittenAt: time.Now().UTC(), Columns: map[string]domain.Scalar{"close": {Type: 3, Double: &value}}})
	}
	return domain.EventBatch{MessageID: messageID, Rows: rows}
}
