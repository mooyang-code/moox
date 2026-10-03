package pebble

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	cpebble "github.com/cockroachdb/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestDatasetPeriodCommitBitmapIsIdempotentAndCompletesOnce(t *testing.T) {
	store := newPeriodTestStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 28, 4, 0, 0, 0, time.UTC)
	exp := periodExpectationForTest(period, 2)
	status, err := store.EnsureDatasetPeriod(ctx, exp)
	require.NoError(t, err)
	require.Equal(t, "waiting", status.Status)
	status, err = store.EnsureDatasetPeriod(ctx, exp)
	require.NoError(t, err)
	require.Equal(t, "waiting", status.Status)

	status, err = store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: periodRowForTest(period, "BTC-USDT")}}, "source-btc", "collector")
	require.NoError(t, err)
	require.Equal(t, "waiting", status.Status)
	status, err = store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: periodRowForTest(period, "BTC-USDT")}}, "source-btc", "collector")
	require.NoError(t, err)
	require.Equal(t, "waiting", status.Status)
	progress, err := store.GetDatasetPeriodProgress(ctx, exp)
	require.NoError(t, err)
	require.Equal(t, 1, bitmapCount(progress.Bitmap))

	status, err = store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 1, Row: periodRowForTest(period, "ETH-USDT")}}, "source-eth", "collector")
	require.NoError(t, err)
	require.Equal(t, "complete", status.Status)
	progress, err = store.GetDatasetPeriodProgress(ctx, exp)
	require.NoError(t, err)
	require.Equal(t, "complete", progress.Status)
	require.Equal(t, 2, bitmapCount(progress.Bitmap))

	// A completed period is terminal; repeated reports neither change the
	// bitmap nor append another completion marker.
	status, err = store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 1, Row: periodRowForTest(period, "ETH-USDT")}}, "source-eth-retry", "collector")
	require.NoError(t, err)
	require.Equal(t, "complete", status.Status)
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

func TestDatasetPeriodWaitingReservationIsExclusiveAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node")
	store, err := Open(Options{Path: path, NodeID: "node-a"})
	require.NoError(t, err)
	period := time.Now().UTC().Truncate(time.Second)
	exp := periodExpectationForTest(period, 1, "BTC-USDT")
	exp.ReservationID = "release-a"
	_, err = store.EnsureDatasetPeriod(context.Background(), exp)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	store, err = Open(Options{Path: path, NodeID: "node-a"})
	require.NoError(t, err)
	defer store.Close()

	competing := exp
	competing.ReservationID = "release-b"
	_, err = store.EnsureDatasetPeriod(context.Background(), competing)
	var conflict PeriodConflictError
	require.ErrorAs(t, err, &conflict, "a second publisher must not join the waiting canary period")

	_, err = store.CommitTimeSeriesBatch(context.Background(), competing, []TimeSeriesBatchItem{{
		SeriesIndex: 0, Row: periodRowForTest(period, "BTC-USDT"),
	}}, "release-b", "collector")
	require.ErrorAs(t, err, &conflict, "a different reservation must not commit data into the canary period")
	_, err = store.GetDatasetPeriodStatus(context.Background(), competing)
	require.ErrorAs(t, err, &conflict, "a different reservation must not inspect a waiting canary period")
	_, err = store.RecordDatasetPeriodFailures(context.Background(), competing, []uint32{0})
	require.ErrorAs(t, err, &conflict, "a different reservation must not report failures into the canary period")

	status, err := store.GetDatasetPeriodStatus(context.Background(), exp)
	require.NoError(t, err)
	require.Equal(t, "waiting", status.Status)

	statusResult, err := store.CommitTimeSeriesBatch(context.Background(), exp, []TimeSeriesBatchItem{{
		SeriesIndex: 0, Row: periodRowForTest(period, "BTC-USDT"),
	}}, "release-a", "collector")
	require.NoError(t, err)
	require.Equal(t, "complete", statusResult.Status)

	// Reservation ownership only fences a waiting period. Once terminal, normal
	// tokenless status reads and retry receipts remain compatible with the data identity.
	ordinary := exp
	ordinary.ReservationID = ""
	terminal, err := store.GetDatasetPeriodStatus(context.Background(), ordinary)
	require.NoError(t, err)
	require.Equal(t, "complete", terminal.Status)
	terminalResult, err := store.CommitTimeSeriesBatch(context.Background(), ordinary, []TimeSeriesBatchItem{{
		SeriesIndex: 0, Row: periodRowForTest(period, "BTC-USDT"),
	}}, "normal-retry", "collector")
	require.NoError(t, err)
	require.Equal(t, "complete", terminalResult.Status)
}

func TestDatasetPeriodEnsureRejectsNonPositiveDeadlineWithoutSideEffects(t *testing.T) {
	for _, deadlineAt := range []int64{0, -1} {
		t.Run(fmt.Sprint(deadlineAt), func(t *testing.T) {
			store := newPeriodTestStore(t)
			exp := periodExpectationForTest(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), 1, "BTC-USDT")
			exp.DeadlineAt = deadlineAt

			_, err := store.EnsureDatasetPeriod(context.Background(), exp)
			require.ErrorContains(t, err, "deadline_at must be positive")
			query := exp
			query.DeadlineAt = 0
			_, err = store.GetDatasetPeriodStatus(context.Background(), query)
			require.ErrorIs(t, err, cpebble.ErrNotFound)
			require.Zero(t, countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()))
		})
	}
}

func TestDatasetPeriodEnsureRejectsMarkerOverEventLimit(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "node"), NodeID: "node-a", MaxEventBytes: 1})
	require.NoError(t, err)
	defer store.Close()

	exp := periodExpectationForTest(time.Now().UTC().Truncate(time.Minute), 1, "BTC-USDT")
	_, err = store.EnsureDatasetPeriod(context.Background(), exp)
	require.ErrorContains(t, err, "exceeds limit")
	_, err = store.GetDatasetPeriodStatus(context.Background(), exp)
	require.ErrorIs(t, err, cpebble.ErrNotFound, "an unpublishable period must not be persisted")
	require.Zero(t, countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()))
}

func TestDatasetPeriodEnsurePreflightIncludesMaximumTimestampNanos(t *testing.T) {
	exp := periodExpectationForTest(time.Date(2026, 9, 28, 4, 1, 0, 0, time.UTC), 1, "BTC-USDT")
	maxTimestamp := time.Date(9999, time.December, 31, 23, 59, 59, 999999999, time.UTC)
	raw := periodMarkerPayloadForTest(t, exp, maxTimestamp)
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "node"), NodeID: "node-a", MaxEventBytes: len(raw) - 1})
	require.NoError(t, err)
	defer store.Close()

	_, err = store.EnsureDatasetPeriod(context.Background(), exp)
	require.ErrorContains(t, err, "exceeds limit", "Ensure must reserve enough space for non-zero protobuf Timestamp nanos")
}

func TestDatasetPeriodEnsureExistingStateIgnoresCurrentMarkerLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node")
	period := time.Now().UTC().Truncate(time.Minute)
	subject := fmt.Sprintf("%0400d", 0)
	exp := periodExpectationForTest(period, 1, subject)
	exp.DeadlineAt = period.Add(time.Hour).Unix()
	store, err := Open(Options{Path: path, NodeID: "node-a"})
	require.NoError(t, err)
	defer func() {
		if store != nil {
			_ = store.Close()
		}
	}()
	created, err := store.EnsureDatasetPeriod(context.Background(), exp)
	require.NoError(t, err)
	completed, err := store.CommitTimeSeriesBatch(context.Background(), exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: periodRowForTest(period, subject)}}, "source-large", "collector")
	require.NoError(t, err)
	require.Equal(t, "complete", completed.Status)
	require.NoError(t, store.Close())
	store = nil

	store, err = Open(Options{Path: path, NodeID: "node-a", MaxEventBytes: 1})
	require.NoError(t, err)

	replayed, err := store.EnsureDatasetPeriod(context.Background(), exp)
	require.NoError(t, err, "existing period state must be checked before applying a creation-only marker limit")
	require.Equal(t, completed.Status, replayed.Status)
	require.Equal(t, created.DeadlineAt, replayed.DeadlineAt)
}

func TestDatasetPeriodFinalizerContinuesAfterOversizedPeriod(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node")
	store, err := Open(Options{Path: path, NodeID: "node-a"})
	require.NoError(t, err)

	largeSubjects := make([]string, 30)
	for index := range largeSubjects {
		largeSubjects[index] = fmt.Sprintf("LONG-ASSET-SYMBOL-%02d-ABCDEFGHIJKLMNOPQRSTUVWXYZ", index)
	}
	large := periodExpectationForTest(time.Unix(1, 0).UTC(), uint32(len(largeSubjects)), largeSubjects...)
	large.SpaceID, large.DatasetID, large.DeadlineAt = "crypto", "large", 10
	small := periodExpectationForTest(time.Unix(2, 0).UTC(), 1, "BTC-USDT")
	small.SpaceID, small.DatasetID, small.DeadlineAt = "crypto", "small", 10
	_, err = store.EnsureDatasetPeriod(context.Background(), large)
	require.NoError(t, err)
	_, err = store.EnsureDatasetPeriod(context.Background(), small)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	store = nil

	now := time.Unix(100, 123456789).UTC()
	smallRaw := periodMarkerPayloadForTest(t, small, now)
	largeRaw := periodMarkerPayloadForTest(t, large, now)
	require.Greater(t, len(largeRaw), len(smallRaw))
	store, err = Open(Options{Path: path, NodeID: "node-a", MaxEventBytes: len(smallRaw)})
	require.NoError(t, err)
	defer store.Close()

	finalized, err := store.FinalizeWaitingDatasetPeriods(context.Background(), now, 10)
	require.ErrorContains(t, err, "exceeds limit", "the unpublishable legacy period must remain observable")
	require.Equal(t, 1, finalized, "one oversized period must not block a later publishable period")

	largeStatus, err := store.GetDatasetPeriodProgress(context.Background(), large)
	require.NoError(t, err)
	require.Equal(t, "waiting", largeStatus.Status)
	smallStatus, err := store.GetDatasetPeriodProgress(context.Background(), small)
	require.NoError(t, err)
	require.Equal(t, "degraded", smallStatus.Status)
	require.Equal(t, 1, countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()))
}

func TestDatasetPeriodFinalizerRotatesPastOversizedDeadlineBatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node")
	store, err := Open(Options{Path: path, NodeID: "node-a"})
	require.NoError(t, err)
	defer func() {
		if store != nil {
			_ = store.Close()
		}
	}()

	largeSubject := fmt.Sprintf("%0400d", 0)
	deadline := time.Now().UTC().Add(time.Hour).Truncate(time.Second).Unix()
	largePeriods := []DatasetPeriodExpectation{
		periodExpectationForTest(time.Unix(1, 0).UTC(), 1, largeSubject),
		periodExpectationForTest(time.Unix(2, 0).UTC(), 1, largeSubject),
	}
	for index := range largePeriods {
		largePeriods[index].SpaceID = "crypto"
		largePeriods[index].DatasetID = fmt.Sprintf("%d-large", index)
		largePeriods[index].DeadlineAt = deadline
		_, err = store.EnsureDatasetPeriod(context.Background(), largePeriods[index])
		require.NoError(t, err)
	}
	small := periodExpectationForTest(time.Unix(3, 0).UTC(), 1, "BTC-USDT")
	small.SpaceID, small.DatasetID, small.DeadlineAt = "crypto", "z-small", deadline
	_, err = store.EnsureDatasetPeriod(context.Background(), small)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	now := time.Unix(deadline+90, 123456789).UTC()
	smallRaw := periodMarkerPayloadForTest(t, small, now)
	for _, exp := range largePeriods {
		largeRaw := periodMarkerPayloadForTest(t, exp, now)
		require.Greater(t, len(largeRaw), len(smallRaw))
	}
	store, err = Open(Options{Path: path, NodeID: "node-a", MaxEventBytes: len(smallRaw)})
	require.NoError(t, err)

	finalized, err := store.FinalizeWaitingDatasetPeriods(context.Background(), now, 2)
	require.ErrorContains(t, err, "exceeds limit")
	require.Zero(t, finalized)
	require.NoError(t, store.Close())
	store = nil

	var smallStatus string
	for attempt := 0; attempt < 10; attempt++ {
		store, err = Open(Options{Path: path, NodeID: "node-a", MaxEventBytes: len(smallRaw)})
		require.NoError(t, err)
		finalized, finalErr := store.FinalizeWaitingDatasetPeriods(context.Background(), now, 2)
		if finalized > 0 {
			require.Equal(t, 1, finalized)
		}
		if finalErr != nil {
			require.ErrorContains(t, finalErr, "exceeds limit")
		}
		progress, progressErr := store.GetDatasetPeriodProgress(context.Background(), small)
		require.NoError(t, progressErr)
		smallStatus = progress.Status
		require.NoError(t, store.Close())
		store = nil
		if smallStatus == "degraded" {
			break
		}
	}
	require.Equal(t, "degraded", smallStatus, "a persisted scan cursor must let a later publishable deadline period make progress after restart")
	store, err = Open(Options{Path: path, NodeID: "node-a", MaxEventBytes: len(smallRaw)})
	require.NoError(t, err)
	progress, err := store.GetDatasetPeriodProgress(context.Background(), small)
	require.NoError(t, err)
	require.Equal(t, "degraded", progress.Status)
}

func TestDatasetPeriodFinalizerDoesNotAppendOversizedOutboxEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node")
	store, err := Open(Options{Path: path, NodeID: "node-a"})
	require.NoError(t, err)
	exp := periodExpectationForTest(time.Now().UTC().Truncate(time.Minute), 1, "BTC-USDT")
	exp.DeadlineAt = time.Now().UTC().Add(-time.Minute).Unix()
	_, err = store.EnsureDatasetPeriod(context.Background(), exp)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	store, err = Open(Options{Path: path, NodeID: "node-a", MaxEventBytes: 1})
	require.NoError(t, err)
	defer store.Close()
	_, err = store.FinalizeWaitingDatasetPeriods(context.Background(), time.Now().UTC(), 10)
	require.ErrorContains(t, err, "exceeds limit")
	status, err := store.GetDatasetPeriodStatus(context.Background(), exp)
	require.NoError(t, err)
	require.Equal(t, "waiting", status.Status)
	require.Zero(t, countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()))
}

func TestDatasetPeriodFinalizerRetriesOnlyFailedCursorAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node")
	exp := periodExpectationForTest(time.Unix(1, 0).UTC(), 1, "BTC-USDT")
	exp.SpaceID, exp.DatasetID = "crypto", "only-period"
	exp.DeadlineAt = time.Unix(2, 0).Unix()
	store, err := Open(Options{Path: path, NodeID: "node-a"})
	require.NoError(t, err)
	_, err = store.EnsureDatasetPeriod(context.Background(), exp)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	now := time.Unix(3, 0).UTC()
	markerSize := len(periodMarkerPayloadForTest(t, exp, now))
	store, err = Open(Options{Path: path, NodeID: "node-a", MaxEventBytes: 1})
	require.NoError(t, err)
	_, err = store.FinalizeWaitingDatasetPeriods(context.Background(), now, 10)
	require.ErrorContains(t, err, "exceeds limit")
	require.NoError(t, store.Close())

	store, err = Open(Options{Path: path, NodeID: "node-a", MaxEventBytes: markerSize})
	require.NoError(t, err)
	defer store.Close()
	finalized, err := store.FinalizeWaitingDatasetPeriods(context.Background(), now, 10)
	require.NoError(t, err)
	require.Equal(t, 1, finalized, "the only failed cursor entry must be retried after restart")
	status, err := store.GetDatasetPeriodStatus(context.Background(), exp)
	require.NoError(t, err)
	require.Equal(t, "degraded", status.Status)
}

func TestDatasetPeriodExpectationSeriesSnapshotIsValidatedAndImmutable(t *testing.T) {
	store := newPeriodTestStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 28, 4, 4, 0, 0, time.UTC)
	exp := periodExpectationForTest(period, 2)
	_, err := store.EnsureDatasetPeriod(ctx, exp)
	require.NoError(t, err)
	base := periodBase(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime)
	raw, err := store.readPeriodValue(periodFieldKey(base, "series_snapshot"))
	require.NoError(t, err)
	var persisted []DatasetPeriodSeries
	require.NoError(t, json.Unmarshal(raw, &persisted))
	require.Equal(t, exp.SeriesSnapshot, persisted)
	_, err = store.readPeriodValue(periodFieldKey(base, "roster"))
	require.ErrorIs(t, err, cpebble.ErrNotFound, "the renamed persisted field must not also write the old name")
	encoded, err := json.Marshal(exp)
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &fields))
	require.Contains(t, fields, "series_snapshot")
	require.NotContains(t, fields, "Roster")
	require.NotContains(t, fields, "roster")
	field := (&pb.DatasetPeriodExpectation{}).ProtoReflect().Descriptor().Fields().ByNumber(8)
	require.Equal(t, "series_snapshot", string(field.Name()), "the snapshot rename preserves Proto field number 8")

	changed := exp
	changed.SeriesSnapshot = append([]DatasetPeriodSeries(nil), exp.SeriesSnapshot...)
	changed.SeriesSnapshot[1].SubjectID = "SOL-USDT"
	_, err = store.EnsureDatasetPeriod(ctx, changed)
	var conflict PeriodConflictError
	require.ErrorAs(t, err, &conflict)

	invalid := exp
	invalid.SeriesSnapshot = append([]DatasetPeriodSeries(nil), exp.SeriesSnapshot...)
	invalid.SeriesSnapshot[1].SeriesIndex = 2
	_, err = newPeriodTestStore(t).EnsureDatasetPeriod(ctx, invalid)
	require.Error(t, err)
}

func TestDatasetPeriodExpectationDoesNotReadOldSnapshotField(t *testing.T) {
	store := newPeriodTestStore(t)
	exp := periodExpectationForTest(time.Date(2026, 9, 28, 4, 4, 0, 0, time.UTC), 2)
	_, err := store.EnsureDatasetPeriod(context.Background(), exp)
	require.NoError(t, err)
	base := periodBase(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime)
	raw, err := json.Marshal(exp.SeriesSnapshot)
	require.NoError(t, err)
	batch := store.db.NewBatch()
	defer batch.Close()
	require.NoError(t, batch.Set(periodFieldKey(base, "roster"), raw, store.writeOptions))
	require.NoError(t, batch.Delete(periodFieldKey(base, "series_snapshot"), store.writeOptions))
	require.NoError(t, batch.Commit(store.writeOptions))

	loaded, err := store.readPeriodExpectation(base)
	require.NoError(t, err)
	require.Empty(t, loaded.SeriesSnapshot, "old snapshot fields are not an implicit compatibility source")
}

func TestDatasetPeriodFailureWaitsForDeadlineAndEmitsOneDegradedMarker(t *testing.T) {
	store := newPeriodTestStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 28, 4, 5, 0, 0, time.UTC)
	exp := periodExpectationForTest(period, 3, "BTC-USDT", "ETH-USDT", "ETH-USDT")
	exp.SeriesSnapshot[2].SeriesTag = "alternate"
	deadline := time.Now().UTC().Add(time.Minute)
	exp.DeadlineAt = deadline.Unix()
	_, err := store.EnsureDatasetPeriod(ctx, exp)
	require.NoError(t, err)

	_, err = store.RecordDatasetPeriodFailures(ctx, exp, []uint32{3})
	require.Error(t, err, "failure indices outside the persisted series snapshot must be rejected")
	status, err := store.RecordDatasetPeriodFailures(ctx, exp, []uint32{1, 2, 2})
	require.NoError(t, err)
	require.Equal(t, "waiting", status.Status)
	status, err = store.RecordDatasetPeriodFailures(ctx, exp, []uint32{1, 2})
	require.NoError(t, err)
	require.Equal(t, "waiting", status.Status, "repeated failure reports remain idempotent")
	progress, err := store.GetDatasetPeriodProgress(ctx, exp)
	require.NoError(t, err)
	require.Equal(t, "waiting", progress.Status)
	require.Zero(t, bitmapCount(progress.Bitmap), "failures must not set success bits")
	require.Equal(t, []uint32{1, 2}, progress.FailedSeriesIndexes)

	status.Status, err = store.FinalizeDatasetPeriod(ctx, exp, deadline.Add(-time.Second))
	require.NoError(t, err)
	require.Equal(t, "waiting", status.Status, "failure recording must not finalize before the deadline")

	status.Status, err = store.FinalizeDatasetPeriod(ctx, exp, deadline)
	require.NoError(t, err)
	require.Equal(t, "degraded", status.Status)
	status.Status, err = store.FinalizeDatasetPeriod(ctx, exp, deadline.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, "degraded", status.Status)
	require.Equal(t, 1, countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()))

	marker := collectorPeriodMarkerFromOutbox(t, store)
	require.Equal(t, "degraded", marker.GetStatus())
	require.Len(t, exp.SeriesSnapshot, 3)
	require.Len(t, marker.GetUniverseSubjectIds(), 2)
	require.Equal(t, []string{"BTC-USDT", "ETH-USDT"}, marker.GetUniverseSubjectIds(), "universe must deduplicate provider series")
	require.Equal(t, []string{"ETH-USDT"}, marker.GetFailedSubjects())
}

func TestDatasetPeriodDeadlineWinsAgainstConcurrentLateCommit(t *testing.T) {
	store := newPeriodTestStore(t)
	ctx := context.Background()
	period := time.Now().UTC().Truncate(time.Minute)
	exp := periodExpectationForTest(period, 1, "BTC-USDT")
	exp.DeadlineAt = time.Now().UTC().Add(-time.Second).Unix()
	_, err := store.EnsureDatasetPeriod(ctx, exp)
	require.NoError(t, err)

	// Hold the outbox mutex so a late commit and explicit finalizer contend on
	// the same period state before either can publish its terminal marker.
	store.outboxMu.Lock()
	commitStarted := make(chan struct{})
	commitResult := make(chan string, 1)
	commitErr := make(chan error, 1)
	go func() {
		close(commitStarted)
		status, commitErrValue := store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: periodRowForTest(period, "BTC-USDT")}}, "source-late", "collector")
		commitResult <- status.Status
		commitErr <- commitErrValue
	}()
	<-commitStarted
	finalizeStarted := make(chan struct{})
	finalizeResult := make(chan string, 1)
	finalizeErr := make(chan error, 1)
	go func() {
		close(finalizeStarted)
		status, finalizeErrValue := store.FinalizeDatasetPeriod(ctx, exp, time.Now().UTC())
		finalizeResult <- status
		finalizeErr <- finalizeErrValue
	}()
	<-finalizeStarted
	store.outboxMu.Unlock()

	select {
	case err := <-commitErr:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("late commit did not finish")
	}
	select {
	case err := <-finalizeErr:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("period finalizer did not finish")
	}
	require.Equal(t, "degraded", <-commitResult)
	require.Equal(t, "degraded", <-finalizeResult)
	progress, err := store.GetDatasetPeriodProgress(ctx, exp)
	require.NoError(t, err)
	require.Equal(t, "degraded", progress.Status)
	require.Zero(t, bitmapCount(progress.Bitmap), "a late commit must not change the terminal period result")
	require.Equal(t, 1, countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()))
}

func TestDatasetPeriodLateSuccessClearsFailureAndCompletes(t *testing.T) {
	store := newPeriodTestStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 28, 4, 6, 0, 0, time.UTC)
	exp := periodExpectationForTest(period, 2)
	exp.DeadlineAt = time.Now().UTC().Add(time.Hour).Unix()
	_, err := store.EnsureDatasetPeriod(ctx, exp)
	require.NoError(t, err)
	_, err = store.RecordDatasetPeriodFailures(ctx, exp, []uint32{1})
	require.NoError(t, err)
	status, err := store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: periodRowForTest(period, "BTC-USDT")}}, "source-btc", "collector")
	require.NoError(t, err)
	require.Equal(t, "waiting", status.Status)
	status, err = store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 1, Row: periodRowForTest(period, "ETH-USDT")}}, "source-eth-late", "collector")
	require.NoError(t, err)
	require.Equal(t, "complete", status.Status)

	progress, err := store.GetDatasetPeriodProgress(ctx, exp)
	require.NoError(t, err)
	require.Empty(t, progress.FailedSeriesIndexes)
	require.Equal(t, 2, bitmapCount(progress.Bitmap))
	marker := collectorPeriodMarkerFromOutbox(t, store)
	require.Equal(t, "complete", marker.GetStatus())
	require.Empty(t, marker.GetFailedSubjects())
	require.Equal(t, []string{"BTC-USDT", "ETH-USDT"}, marker.GetUniverseSubjectIds())
	require.Equal(t, 1, countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()))
}

func TestDatasetPeriodEnsureRetryKeepsOriginalDeadline(t *testing.T) {
	store := newPeriodTestStore(t)
	ctx := context.Background()
	exp := periodExpectationForTest(time.Now().UTC().Truncate(time.Second), 2)
	originalDeadline := exp.DeadlineAt
	status, err := store.EnsureDatasetPeriod(ctx, exp)
	require.NoError(t, err)
	require.Equal(t, "waiting", status.Status)

	retry := exp
	retry.DeadlineAt += int64(time.Minute.Seconds())
	status, err = store.EnsureDatasetPeriod(ctx, retry)
	require.NoError(t, err)
	require.Equal(t, "waiting", status.Status)

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
			_, commitErr := store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: index, Row: periodRowForTest(period, exp.SeriesSnapshot[index].SubjectID)}}, "", "collector")
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
	// CommitTimeSeriesBatch writes the bitmap merge and durable complete index
	// atomically before invoking the follow-up finalizer. Reproduce that exact
	// crash window rather than mutating the bitmap alone.
	batchWrite := store.db.NewBatch()
	require.NoError(t, batchWrite.Merge(periodFieldKey(base, "bitmap"), delta, store.writeOptions))
	require.NoError(t, batchWrite.Set(periodCompleteKey(base), []byte(base), store.writeOptions))
	require.NoError(t, batchWrite.Commit(store.writeOptions))
	require.NoError(t, batchWrite.Close())
	require.NoError(t, store.Close())

	store, err = Open(Options{Path: path, NodeID: "node-a"})
	require.NoError(t, err)
	defer store.Close()
	finalized, err := store.FinalizeWaitingDatasetPeriods(ctx, period.Add(time.Second), 10)
	require.NoError(t, err)
	require.LessOrEqual(t, finalized, 1, "the background finalizer may win the restart race")
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
	deadline := time.Now().UTC().Add(time.Minute)
	exp.DeadlineAt = deadline.Unix()
	_, err := store.EnsureDatasetPeriod(ctx, exp)
	require.NoError(t, err)
	_, err = store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: periodRowForTest(period, "BTC-USDT")}}, "", "collector")
	require.NoError(t, err)
	status, err := store.FinalizeDatasetPeriod(ctx, exp, deadline.Add(time.Minute))
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

func periodExpectationForTest(period time.Time, expected uint32, subjects ...string) DatasetPeriodExpectation {
	if len(subjects) == 0 {
		for index := uint32(0); index < expected; index++ {
			if expected == 2 {
				subjects = append(subjects, []string{"BTC-USDT", "ETH-USDT"}[index])
			} else {
				subjects = append(subjects, fmt.Sprintf("S-%d", index))
			}
		}
	}
	seriesSnapshot := make([]DatasetPeriodSeries, 0, len(subjects))
	for index, subject := range subjects {
		seriesSnapshot = append(seriesSnapshot, DatasetPeriodSeries{SeriesIndex: uint32(index), SubjectID: subject})
	}
	return DatasetPeriodExpectation{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period.Unix(), SeriesHash: "series-hash", ExpectedCount: expected, DeadlineAt: time.Now().UTC().Add(time.Hour).Unix(), SeriesSnapshot: seriesSnapshot}
}

func collectorPeriodMarkerFromOutbox(t *testing.T, store *Store) *storageeventpb.CollectorPeriodCompleted {
	t.Helper()
	entries, err := store.ListOutbox(context.Background(), 0, 1000)
	require.NoError(t, err)
	for _, entry := range entries {
		message := &eventpb.EventMessage{}
		require.NoError(t, proto.Unmarshal(entry.Data, message))
		if message.GetEventName() != events.CollectorPeriodCompleted.Name() {
			continue
		}
		marker := &storageeventpb.CollectorPeriodCompleted{}
		require.NoError(t, proto.Unmarshal(message.GetPayload(), marker))
		return marker
	}
	t.Fatal("collector period marker not found in outbox")
	return nil
}

func periodMarkerPayloadForTest(t *testing.T, exp DatasetPeriodExpectation, collectedAt time.Time) []byte {
	t.Helper()
	universe, failed := periodMarkerSubjects(exp, nil, nil)
	raw, _, err := BuildCollectorPeriodCompletedMessage(exp.SpaceID, &pb.CollectorPeriodCompletedMarker{
		DatasetId: exp.DatasetID, Frequency: exp.Frequency, PeriodTime: exp.PeriodTime, Status: "degraded",
		BatchId:          periodCompletionID(periodBase(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime)),
		ConfigSnapshotId: exp.SeriesHash, ExpectedScopeRef: exp.SeriesHash,
		UniverseSubjectIds: universe, FailedSubjects: failed, CollectedAt: timestamppb.New(collectedAt),
	})
	require.NoError(t, err)
	return raw
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
