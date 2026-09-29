package pebble

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	cpebble "github.com/cockroachdb/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestDatasetPeriodCommitBitmapIsIdempotentAndCompletesOnce(t *testing.T) {
	store := newPeriodTestStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 28, 4, 0, 0, 0, time.UTC)
	exp := periodExpectationForTest(period, 2)
	status, err := store.EnsureDatasetPeriod(ctx, exp)
	require.NoError(t, err)
	require.Equal(t, "waiting", status)
	status, err = store.EnsureDatasetPeriod(ctx, exp)
	require.NoError(t, err)
	require.Equal(t, "waiting", status)

	status, err = store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: periodRowForTest(period, "BTC-USDT")}}, "source-btc", "collector")
	require.NoError(t, err)
	require.Equal(t, "waiting", status)
	status, err = store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: periodRowForTest(period, "BTC-USDT")}}, "source-btc", "collector")
	require.NoError(t, err)
	require.Equal(t, "waiting", status)
	progress, err := store.GetDatasetPeriodProgress(ctx, exp)
	require.NoError(t, err)
	require.Equal(t, 1, bitmapCount(progress.Bitmap))

	status, err = store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 1, Row: periodRowForTest(period, "ETH-USDT")}}, "source-eth", "collector")
	require.NoError(t, err)
	require.Equal(t, "complete", status)
	progress, err = store.GetDatasetPeriodProgress(ctx, exp)
	require.NoError(t, err)
	require.Equal(t, "complete", progress.Status)
	require.Equal(t, 2, bitmapCount(progress.Bitmap))

	// A completed period is terminal; repeated reports neither change the
	// bitmap nor append another completion marker.
	status, err = store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 1, Row: periodRowForTest(period, "ETH-USDT")}}, "source-eth-retry", "collector")
	require.NoError(t, err)
	require.Equal(t, "complete", status)
	require.Equal(t, 1, countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()))
}

func TestDatasetPeriodExpectationConflictIsRejected(t *testing.T) {
	store := newPeriodTestStore(t)
	ctx := context.Background()
	exp := periodExpectationForTest(time.Now().UTC().Truncate(time.Second), 2)
	_, err := store.EnsureDatasetPeriod(ctx, exp)
	require.NoError(t, err)
	changed := exp
	changed.SeriesHash = "other-hash"
	_, err = store.EnsureDatasetPeriod(ctx, changed)
	var conflict PeriodConflictError
	require.ErrorAs(t, err, &conflict)
}

func TestDatasetPeriodEnsureRetryKeepsOriginalDeadline(t *testing.T) {
	store := newPeriodTestStore(t)
	ctx := context.Background()
	exp := periodExpectationForTest(time.Now().UTC().Truncate(time.Second), 2)
	originalDeadline := exp.DeadlineAt
	status, err := store.EnsureDatasetPeriod(ctx, exp)
	require.NoError(t, err)
	require.Equal(t, "waiting", status)

	retry := exp
	retry.DeadlineAt += int64(time.Minute.Seconds())
	status, err = store.EnsureDatasetPeriod(ctx, retry)
	require.NoError(t, err)
	require.Equal(t, "waiting", status)

	progress, err := store.GetDatasetPeriodProgress(ctx, exp)
	require.NoError(t, err)
	require.Equal(t, originalDeadline, progress.DeadlineAt)
}

func TestDatasetPeriodConcurrentBitmapMergeDoesNotLoseBits(t *testing.T) {
	store := newPeriodTestStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 28, 4, 1, 0, 0, time.UTC)
	exp := periodExpectationForTest(period, 32)
	_, err := store.EnsureDatasetPeriod(ctx, exp)
	require.NoError(t, err)

	var wg sync.WaitGroup
	errs := make(chan error, exp.ExpectedCount)
	for i := uint32(0); i < exp.ExpectedCount; i++ {
		wg.Add(1)
		go func(index uint32) {
			defer wg.Done()
			_, commitErr := store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: index, Row: periodRowForTest(period, "S"+string(rune('A'+index)))}}, "", "collector")
			errs <- commitErr
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	progress, err := store.GetDatasetPeriodProgress(ctx, exp)
	require.NoError(t, err)
	require.Equal(t, int(exp.ExpectedCount), bitmapCount(progress.Bitmap))
	require.Equal(t, "complete", progress.Status)
	require.Equal(t, 1, countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()))
}

func TestDatasetPeriodFinalizerRecoversFullWaitingBitmapAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node")
	store, err := Open(Options{Path: path, NodeID: "node-a"})
	require.NoError(t, err)
	ctx := context.Background()
	period := time.Date(2026, 9, 28, 4, 2, 0, 0, time.UTC)
	exp := periodExpectationForTest(period, 2)
	_, err = store.EnsureDatasetPeriod(ctx, exp)
	require.NoError(t, err)
	base := periodBase(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime)
	delta := []byte{0b00000011}
	require.NoError(t, store.db.Merge(periodFieldKey(base, "bitmap"), delta, store.writeOptions))
	require.NoError(t, store.Close())

	store, err = Open(Options{Path: path, NodeID: "node-a"})
	require.NoError(t, err)
	defer store.Close()
	finalized, err := store.FinalizeWaitingDatasetPeriods(ctx, period.Add(time.Second), 10)
	require.NoError(t, err)
	require.Equal(t, 1, finalized)
	progress, err := store.GetDatasetPeriodProgress(ctx, exp)
	require.NoError(t, err)
	require.Equal(t, "complete", progress.Status)
	require.Equal(t, 1, countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()))
}

func TestDatasetPeriodFinalizerMarksDeadlineDegraded(t *testing.T) {
	store := newPeriodTestStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 28, 4, 3, 0, 0, time.UTC)
	exp := periodExpectationForTest(period, 2)
	exp.DeadlineAt = period.Add(time.Minute).Unix()
	_, err := store.EnsureDatasetPeriod(ctx, exp)
	require.NoError(t, err)
	_, err = store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: periodRowForTest(period, "BTC-USDT")}}, "", "collector")
	require.NoError(t, err)
	status, err := store.FinalizeDatasetPeriod(ctx, exp, period.Add(2*time.Minute))
	require.NoError(t, err)
	require.Equal(t, "degraded", status)
	progress, err := store.GetDatasetPeriodProgress(ctx, exp)
	require.NoError(t, err)
	require.Equal(t, "degraded", progress.Status)
	require.Equal(t, 1, countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()))
}

func TestBitmapORMergerSurvivesCompactAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node")
	store, err := Open(Options{Path: path, NodeID: "node-a"})
	require.NoError(t, err)
	key := []byte("bitmap-merge-test")
	require.NoError(t, store.db.Set(key, []byte{0}, store.writeOptions))
	require.NoError(t, store.db.Merge(key, []byte{1}, store.writeOptions))
	require.NoError(t, store.db.Merge(key, []byte{2}, store.writeOptions))
	require.NoError(t, store.db.Compact(key, append(append([]byte(nil), key...), 0xff), true))
	value, closer, err := store.db.Get(key)
	require.NoError(t, err)
	require.Equal(t, byte(3), value[0])
	require.NoError(t, closer.Close())
	require.NoError(t, store.Close())
	store, err = Open(Options{Path: path, NodeID: "node-a"})
	require.NoError(t, err)
	defer store.Close()
	value, closer, err = store.db.Get(key)
	require.NoError(t, err)
	require.Equal(t, byte(3), value[0])
	require.NoError(t, closer.Close())
}

func newPeriodTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "node"), NodeID: "node-a"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func periodExpectationForTest(period time.Time, expected uint32) DatasetPeriodExpectation {
	return DatasetPeriodExpectation{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period.Unix(), SeriesHash: "series-hash", ExpectedCount: expected, DeadlineAt: time.Now().UTC().Add(time.Hour).Unix()}
}

func periodRowForTest(period time.Time, subject string) *pb.RowFieldUpsert {
	return &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: "crypto", DatasetId: "bars", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: subject, Freq: "1m", DataTime: period.Format(time.RFC3339Nano)}}}, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}}}
}

func countOutboxEvent(t *testing.T, store *Store, eventName string) int {
	t.Helper()
	entries, err := store.ListOutbox(context.Background(), 0, 1000)
	require.NoError(t, err)
	count := 0
	for _, entry := range entries {
		message := &eventpb.EventMessage{}
		require.NoError(t, proto.Unmarshal(entry.Data, message))
		if message.GetEventName() == eventName {
			count++
		}
	}
	return count
}

var _ = errors.Is
var _ = cpebble.ErrNotFound
