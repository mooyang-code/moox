package pebble

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cpebble "github.com/cockroachdb/pebble"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestInspectPeriodLedgerReadOnlyReportsScopedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node")
	clock := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	store, err := Open(Options{Path: path, NodeID: "node-a", PeriodNow: func() time.Time { return clock }})
	require.NoError(t, err)

	completeAt := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	complete := periodExpectationForTest(completeAt, 1, "BTC-USDT")
	complete.DeadlineAt = clock.Add(time.Hour).Unix()
	_, err = store.EnsureDatasetPeriod(context.Background(), complete)
	require.NoError(t, err)
	_, err = store.CommitTimeSeriesBatch(context.Background(), complete, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: periodRowForTest(completeAt, "BTC-USDT")}}, "commit-complete", "collector")
	require.NoError(t, err)

	degradedAt := time.Date(2026, 9, 30, 13, 0, 0, 0, time.UTC)
	degraded := periodExpectationForTest(degradedAt, 2, "BTC-USDT", "ETH-USDT")
	degraded.DeadlineAt = clock.Add(time.Hour).Unix()
	_, err = store.EnsureDatasetPeriod(context.Background(), degraded)
	require.NoError(t, err)
	store.datasetWriteMu.RLock()
	store.periodMu.Lock()
	_, err = store.finalizePeriodBaseLocked(context.Background(), periodBase(degraded.SpaceID, degraded.DatasetID, degraded.Frequency, degraded.PeriodTime), time.Unix(degraded.DeadlineAt, 0))
	store.periodMu.Unlock()
	store.datasetWriteMu.RUnlock()
	require.NoError(t, err)

	waitingAt := time.Date(2026, 9, 30, 14, 0, 0, 0, time.UTC)
	waiting := periodExpectationForTest(waitingAt, 2, "SOL-USDT", "XRP-USDT")
	waiting.DeadlineAt = clock.Add(2 * time.Hour).Unix()
	_, err = store.EnsureDatasetPeriod(context.Background(), waiting)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	before := pebbleTreeDigest(t, path)
	scope := PeriodLedgerScope{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTimeMin: complete.PeriodTime, PeriodTimeMax: waiting.PeriodTime}
	got, err := InspectPeriodLedger(path, "node-a", scope)
	require.NoError(t, err)
	after := pebbleTreeDigest(t, path)
	if before != after {
		t.Logf("before=%x after=%x", before, after)
	}
	require.Equal(t, before, after, "inventory must not modify Pebble files")
	require.Equal(t, 3, got.PeriodCount)
	require.Equal(t, 1, got.CompleteCount)
	require.Equal(t, 1, got.DegradedCount)
	require.Equal(t, 1, got.WaitingCount)
	require.Len(t, got.Periods, 3)
	require.Equal(t, "series_snapshot", got.Periods[0].SnapshotFormat)
	require.Equal(t, 1, got.Periods[0].MarkerRecordCount)
	require.Equal(t, 1, got.Periods[0].OutboxCount)
	require.True(t, got.Periods[2].WaitingIndexPresent)
	require.True(t, got.Periods[2].DeadlineIndexPresent)
	require.Empty(t, got.IntegrityErrors)
}

func pebbleTreeDigest(t *testing.T, root string) [32]byte {
	t.Helper()
	hash := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Pebble must acquire its process lock; that OS lock file is not DB state.
		if entry.IsDir() || entry.Name() == "LOCK" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(hash, "%s:%d:%d\n", filepath.Base(path), info.Size(), info.ModTime().UnixNano())
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	require.NoError(t, err)
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}

func TestInspectPeriodLedgerRejectsLivePebbleDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node")
	store, err := Open(Options{Path: path, NodeID: "node-a"})
	require.NoError(t, err)
	defer store.Close()

	_, err = InspectPeriodLedger(path, "node-a", PeriodLedgerScope{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTimeMin: 1, PeriodTimeMax: 2})
	require.Error(t, err)
}

func TestInspectPeriodLedgerRequiresBoundedIdentityScope(t *testing.T) {
	_, err := InspectPeriodLedger("unused", "node-a", PeriodLedgerScope{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTimeMin: 2, PeriodTimeMax: 1})
	require.ErrorContains(t, err, "period_time")
}

func TestInspectPeriodLedgerLabelsLegacySnapshotWithoutMigratingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node")
	store, err := Open(Options{Path: path, NodeID: "node-a"})
	require.NoError(t, err)
	period := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	exp := periodExpectationForTest(period, 1, "BTC-USDT")
	_, err = store.EnsureDatasetPeriod(context.Background(), exp)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	db, err := cpebble.Open(path, &cpebble.Options{Merger: BitmapORMerger})
	require.NoError(t, err)
	base := periodBase(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime)
	value, closer, err := db.Get(periodFieldKey(base, "series_snapshot"))
	require.NoError(t, err)
	raw := append([]byte(nil), value...)
	require.NoError(t, closer.Close())
	require.NoError(t, db.Set(periodFieldKey(base, "roster"), raw, cpebble.Sync))
	require.NoError(t, db.Delete(periodFieldKey(base, "series_snapshot"), cpebble.Sync))
	require.NoError(t, db.Close())

	got, err := InspectPeriodLedger(path, "node-a", PeriodLedgerScope{SpaceID: exp.SpaceID, DatasetID: exp.DatasetID, Frequency: exp.Frequency, PeriodTimeMin: exp.PeriodTime, PeriodTimeMax: exp.PeriodTime})
	require.NoError(t, err)
	require.Equal(t, "legacy_roster", got.Periods[0].SnapshotFormat)
	require.Contains(t, got.Periods[0].IntegrityErrors, "legacy roster state requires an explicitly approved rebuild")
	readOnlyDB, err := cpebble.Open(path, &cpebble.Options{ReadOnly: true, Merger: BitmapORMerger})
	require.NoError(t, err, "inspection leaves the existing legacy key layout untouched")
	require.NoError(t, readOnlyDB.Close())
}

func TestInspectPeriodLedgerRejectsMismatchedSecondaryIndexKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node")
	store, err := Open(Options{Path: path, NodeID: "node-a"})
	require.NoError(t, err)
	period := time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC)
	exp := periodExpectationForTest(period, 1, "BTC-USDT")
	_, err = store.EnsureDatasetPeriod(context.Background(), exp)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	db, err := cpebble.Open(path, &cpebble.Options{Merger: BitmapORMerger})
	require.NoError(t, err)
	base := periodBase(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime)
	require.NoError(t, db.Delete(periodWaitingKey(base), cpebble.Sync))
	require.NoError(t, db.Set([]byte(periodWaitingPrefix+"deadbeef"), []byte(base), cpebble.Sync))
	require.NoError(t, db.Set([]byte(periodWaitingPrefix+strings.ToUpper(hex.EncodeToString([]byte(base)))), []byte(base), cpebble.Sync))
	require.NoError(t, db.Delete(periodDeadlineKey(exp.DeadlineAt, base), cpebble.Sync))
	require.NoError(t, db.Set(periodDeadlineKey(exp.DeadlineAt+60, base), []byte(base), cpebble.Sync))
	require.NoError(t, db.Close())

	got, err := InspectPeriodLedger(path, "node-a", PeriodLedgerScope{SpaceID: exp.SpaceID, DatasetID: exp.DatasetID, Frequency: exp.Frequency, PeriodTimeMin: exp.PeriodTime, PeriodTimeMax: exp.PeriodTime})
	require.NoError(t, err)
	require.Contains(t, got.Periods[0].IntegrityErrors, "waiting index key/value mismatch")
	require.Contains(t, got.Periods[0].IntegrityErrors, "deadline index timestamp does not match persisted deadline")
	require.Contains(t, got.IntegrityErrors, "period waiting index key is not canonical or does not match its value")
	require.NotEmpty(t, got.IntegrityErrors)
}

func TestInspectPeriodLedgerRejectsBitmapPaddingAndOverlap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node")
	store, err := Open(Options{Path: path, NodeID: "node-a"})
	require.NoError(t, err)
	period := time.Date(2026, 9, 30, 16, 0, 0, 0, time.UTC)
	exp := periodExpectationForTest(period, 2, "BTC-USDT", "ETH-USDT")
	_, err = store.EnsureDatasetPeriod(context.Background(), exp)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	db, err := cpebble.Open(path, &cpebble.Options{Merger: BitmapORMerger})
	require.NoError(t, err)
	base := periodBase(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime)
	require.NoError(t, db.Set(periodFieldKey(base, "bitmap"), []byte{0x81}, cpebble.Sync))
	require.NoError(t, db.Set(periodFieldKey(base, "failures"), []byte{0x01}, cpebble.Sync))
	require.NoError(t, db.Close())

	got, err := InspectPeriodLedger(path, "node-a", PeriodLedgerScope{SpaceID: exp.SpaceID, DatasetID: exp.DatasetID, Frequency: exp.Frequency, PeriodTimeMin: exp.PeriodTime, PeriodTimeMax: exp.PeriodTime})
	require.NoError(t, err)
	require.Contains(t, got.Periods[0].IntegrityErrors, "success bitmap has bits outside expected_count")
	require.Contains(t, got.Periods[0].IntegrityErrors, "success and failure bitmaps overlap")
}

func TestInspectPeriodLedgerRejectsAmbiguousSnapshotKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node")
	store, err := Open(Options{Path: path, NodeID: "node-a"})
	require.NoError(t, err)
	period := time.Date(2026, 9, 30, 17, 0, 0, 0, time.UTC)
	exp := periodExpectationForTest(period, 1, "BTC-USDT")
	_, err = store.EnsureDatasetPeriod(context.Background(), exp)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	db, err := cpebble.Open(path, &cpebble.Options{Merger: BitmapORMerger})
	require.NoError(t, err)
	base := periodBase(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime)
	value, closer, err := db.Get(periodFieldKey(base, "series_snapshot"))
	require.NoError(t, err)
	raw := append([]byte(nil), value...)
	require.NoError(t, closer.Close())
	require.NoError(t, db.Set(periodFieldKey(base, "roster"), raw, cpebble.Sync))
	require.NoError(t, db.Close())

	got, err := InspectPeriodLedger(path, "node-a", PeriodLedgerScope{SpaceID: exp.SpaceID, DatasetID: exp.DatasetID, Frequency: exp.Frequency, PeriodTimeMin: exp.PeriodTime, PeriodTimeMax: exp.PeriodTime})
	require.NoError(t, err)
	require.Contains(t, got.Periods[0].IntegrityErrors, "both series_snapshot and legacy roster keys are present")
}

func TestInspectPeriodLedgerCrossChecksMarkerAgainstSnapshotAndBitmap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node")
	store, err := Open(Options{Path: path, NodeID: "node-a"})
	require.NoError(t, err)
	period := time.Date(2026, 9, 30, 18, 0, 0, 0, time.UTC)
	exp := periodExpectationForTest(period, 1, "BTC-USDT")
	_, err = store.EnsureDatasetPeriod(context.Background(), exp)
	require.NoError(t, err)
	_, err = store.CommitTimeSeriesBatch(context.Background(), exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: periodRowForTest(period, "BTC-USDT")}}, "commit-marker-check", "collector")
	require.NoError(t, err)
	require.NoError(t, store.Close())

	db, err := cpebble.Open(path, &cpebble.Options{Merger: BitmapORMerger})
	require.NoError(t, err)
	iter, err := db.NewIter(&cpebble.IterOptions{LowerBound: []byte(markerRecordPrefix), UpperBound: nextPrefix([]byte(markerRecordPrefix))})
	require.NoError(t, err)
	require.True(t, iter.First())
	markerKey := append([]byte(nil), iter.Key()...)
	markerRaw := append([]byte(nil), iter.Value()...)
	require.NoError(t, iter.Close())

	message := &eventpb.EventMessage{}
	require.NoError(t, proto.Unmarshal(markerRaw, message))
	payload := &storageeventpb.CollectorPeriodCompleted{}
	require.NoError(t, proto.Unmarshal(message.GetPayload(), payload))
	payload.ConfigSnapshotId = "wrong-hash"
	message.Payload, err = proto.MarshalOptions{Deterministic: true}.Marshal(payload)
	require.NoError(t, err)
	corruptRaw, err := proto.MarshalOptions{Deterministic: true}.Marshal(message)
	require.NoError(t, err)
	require.NoError(t, db.Delete(markerKey, cpebble.Sync))
	wrongMarkerKey := []byte(markerRecordPrefix + strings.Repeat("0", 64))
	require.NoError(t, db.Set(wrongMarkerKey, corruptRaw, cpebble.Sync))
	// Keep the copies identical so only the persisted snapshot cross-check catches the drift.
	outboxIter, err := db.NewIter(&cpebble.IterOptions{LowerBound: []byte(outboxPrefix), UpperBound: nextPrefix([]byte(outboxPrefix))})
	require.NoError(t, err)
	var outboxKey []byte
	for valid := outboxIter.First(); valid; valid = outboxIter.Next() {
		candidate := &eventpb.EventMessage{}
		require.NoError(t, proto.Unmarshal(outboxIter.Value(), candidate))
		if candidate.GetEventId() == message.GetEventId() {
			outboxKey = append([]byte(nil), outboxIter.Key()...)
			break
		}
	}
	require.NotEmpty(t, outboxKey)
	require.NoError(t, outboxIter.Close())
	require.NoError(t, db.Set(outboxKey, corruptRaw, cpebble.Sync))
	require.NoError(t, db.Close())

	got, err := InspectPeriodLedger(path, "node-a", PeriodLedgerScope{SpaceID: exp.SpaceID, DatasetID: exp.DatasetID, Frequency: exp.Frequency, PeriodTimeMin: exp.PeriodTime, PeriodTimeMax: exp.PeriodTime})
	require.NoError(t, err)
	require.Contains(t, got.Periods[0].IntegrityErrors, "marker config_snapshot_id or expected_scope_ref does not match series_hash")
	require.Contains(t, got.Periods[0].IntegrityErrors, "marker record key does not match event_id")
}

func TestInspectPeriodLedgerCrossChecksFailedSubjectsAgainstFailureBitmap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node")
	clock := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	store, err := Open(Options{Path: path, NodeID: "node-a", PeriodNow: func() time.Time { return clock }})
	require.NoError(t, err)
	period := time.Date(2026, 9, 30, 20, 0, 0, 0, time.UTC)
	exp := periodExpectationForTest(period, 2, "BTC-USDT", "ETH-USDT")
	exp.DeadlineAt = clock.Add(time.Minute).Unix()
	_, err = store.EnsureDatasetPeriod(context.Background(), exp)
	require.NoError(t, err)
	_, err = store.CommitTimeSeriesBatch(context.Background(), exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: periodRowForTest(period, "BTC-USDT")}}, "commit-degraded-marker", "collector")
	require.NoError(t, err)
	base := periodBase(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime)
	store.periodMu.Lock()
	require.NoError(t, store.db.Set(periodFieldKey(base, "failures"), []byte{0x02}, store.writeOptions))
	store.periodMu.Unlock()
	store.datasetWriteMu.RLock()
	store.periodMu.Lock()
	_, err = store.finalizePeriodBaseLocked(context.Background(), base, time.Unix(exp.DeadlineAt, 0))
	store.periodMu.Unlock()
	store.datasetWriteMu.RUnlock()
	require.NoError(t, err)
	require.NoError(t, store.Close())

	scope := PeriodLedgerScope{SpaceID: exp.SpaceID, DatasetID: exp.DatasetID, Frequency: exp.Frequency, PeriodTimeMin: exp.PeriodTime, PeriodTimeMax: exp.PeriodTime}
	got, err := InspectPeriodLedger(path, "node-a", scope)
	require.NoError(t, err)
	require.Empty(t, got.Periods[0].IntegrityErrors)

	db, err := cpebble.Open(path, &cpebble.Options{Merger: BitmapORMerger})
	require.NoError(t, err)
	markerIter, err := db.NewIter(&cpebble.IterOptions{LowerBound: []byte(markerRecordPrefix), UpperBound: nextPrefix([]byte(markerRecordPrefix))})
	require.NoError(t, err)
	require.True(t, markerIter.First())
	markerKey := append([]byte(nil), markerIter.Key()...)
	message := &eventpb.EventMessage{}
	require.NoError(t, proto.Unmarshal(markerIter.Value(), message))
	require.NoError(t, markerIter.Close())
	payload := &storageeventpb.CollectorPeriodCompleted{}
	require.NoError(t, proto.Unmarshal(message.GetPayload(), payload))
	require.Equal(t, []string{"ETH-USDT"}, payload.GetFailedSubjects())
	payload.FailedSubjects = nil
	message.Payload, err = proto.MarshalOptions{Deterministic: true}.Marshal(payload)
	require.NoError(t, err)
	corruptRaw, err := proto.MarshalOptions{Deterministic: true}.Marshal(message)
	require.NoError(t, err)
	require.NoError(t, db.Set(markerKey, corruptRaw, cpebble.Sync))
	outboxIter, err := db.NewIter(&cpebble.IterOptions{LowerBound: []byte(outboxPrefix), UpperBound: nextPrefix([]byte(outboxPrefix))})
	require.NoError(t, err)
	var outboxKey []byte
	for valid := outboxIter.First(); valid; valid = outboxIter.Next() {
		candidate := &eventpb.EventMessage{}
		require.NoError(t, proto.Unmarshal(outboxIter.Value(), candidate))
		if candidate.GetEventId() == message.GetEventId() {
			outboxKey = append([]byte(nil), outboxIter.Key()...)
			break
		}
	}
	require.NotEmpty(t, outboxKey)
	require.NoError(t, outboxIter.Close())
	require.NoError(t, db.Set(outboxKey, corruptRaw, cpebble.Sync))
	require.NoError(t, db.Close())

	got, err = InspectPeriodLedger(path, "node-a", scope)
	require.NoError(t, err)
	require.Contains(t, got.Periods[0].IntegrityErrors, "marker failed_subjects do not match period failure bitmap")
}

func TestInspectPeriodLedgerRequiresDataNodeLayoutAndIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-storage-node")
	db, err := cpebble.Open(path, &cpebble.Options{Merger: BitmapORMerger})
	require.NoError(t, err)
	require.NoError(t, db.Close())
	_, err = InspectPeriodLedger(path, "node-a", PeriodLedgerScope{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTimeMin: 1, PeriodTimeMax: 1})
	require.ErrorContains(t, err, "layout marker")
}

func TestInspectPeriodLedgerReportsVerifiedSourceIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node")
	store, err := Open(Options{Path: path, NodeID: "node-a"})
	require.NoError(t, err)
	storeID := store.sourceStoreID
	require.NoError(t, store.Close())

	scope := PeriodLedgerScope{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTimeMin: 1, PeriodTimeMax: 1}
	got, err := InspectPeriodLedger(path, "node-a", scope)
	require.NoError(t, err)
	require.Equal(t, "node-a", got.SourceDataNodeID)
	require.Equal(t, storeID, got.SourceStoreID)
	require.Equal(t, strings.TrimSpace(layoutVersion), got.SourceLayoutVersion)
	require.Equal(t, path, got.SourcePath)
	_, err = InspectPeriodLedger(path, "other-node", scope)
	require.ErrorContains(t, err, "does not match requested DataNode ID")
}

func TestInspectPeriodLedgerRejectsMarkerAndOutboxPayloadDivergence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node")
	store, err := Open(Options{Path: path, NodeID: "node-a"})
	require.NoError(t, err)
	period := time.Date(2026, 9, 30, 19, 0, 0, 0, time.UTC)
	exp := periodExpectationForTest(period, 1, "BTC-USDT")
	_, err = store.EnsureDatasetPeriod(context.Background(), exp)
	require.NoError(t, err)
	_, err = store.CommitTimeSeriesBatch(context.Background(), exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: periodRowForTest(period, "BTC-USDT")}}, "commit-marker-divergence", "collector")
	require.NoError(t, err)
	require.NoError(t, store.Close())

	db, err := cpebble.Open(path, &cpebble.Options{Merger: BitmapORMerger})
	require.NoError(t, err)
	iter, err := db.NewIter(&cpebble.IterOptions{LowerBound: []byte(outboxPrefix), UpperBound: nextPrefix([]byte(outboxPrefix))})
	require.NoError(t, err)
	var key []byte
	var message *eventpb.EventMessage
	for valid := iter.First(); valid; valid = iter.Next() {
		candidate := &eventpb.EventMessage{}
		require.NoError(t, proto.Unmarshal(iter.Value(), candidate))
		if candidate.GetEventName() == "event.storage.collector.period.completed" {
			key = append([]byte(nil), iter.Key()...)
			message = candidate
			break
		}
	}
	require.NotNil(t, message)
	require.NoError(t, iter.Close())
	payload := &storageeventpb.CollectorPeriodCompleted{}
	require.NoError(t, proto.Unmarshal(message.GetPayload(), payload))
	payload.ConfigSnapshotId = "diverged"
	message.Payload, err = proto.MarshalOptions{Deterministic: true}.Marshal(payload)
	require.NoError(t, err)
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(message)
	require.NoError(t, err)
	require.NoError(t, db.Set(key, raw, cpebble.Sync))
	require.NoError(t, db.Close())

	got, err := InspectPeriodLedger(path, "node-a", PeriodLedgerScope{SpaceID: exp.SpaceID, DatasetID: exp.DatasetID, Frequency: exp.Frequency, PeriodTimeMin: exp.PeriodTime, PeriodTimeMax: exp.PeriodTime})
	require.NoError(t, err)
	require.Contains(t, got.Periods[0].IntegrityErrors, "marker record and outbox payloads differ")
}

func TestInspectPeriodLedgerRejectsOutboxKeyThatRelayCannotRead(t *testing.T) {
	require.False(t, isCanonicalOutboxKey([]byte(outboxKey(0))))
	require.True(t, isCanonicalOutboxKey([]byte(outboxKey(1))))
	path := filepath.Join(t.TempDir(), "node")
	store, err := Open(Options{Path: path, NodeID: "node-a"})
	require.NoError(t, err)
	period := time.Date(2026, 9, 30, 21, 0, 0, 0, time.UTC)
	exp := periodExpectationForTest(period, 1, "BTC-USDT")
	_, err = store.EnsureDatasetPeriod(context.Background(), exp)
	require.NoError(t, err)
	_, err = store.CommitTimeSeriesBatch(context.Background(), exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: periodRowForTest(period, "BTC-USDT")}}, "commit-outbox-key", "collector")
	require.NoError(t, err)
	require.NoError(t, store.Close())

	db, err := cpebble.Open(path, &cpebble.Options{Merger: BitmapORMerger})
	require.NoError(t, err)
	iter, err := db.NewIter(&cpebble.IterOptions{LowerBound: []byte(outboxPrefix), UpperBound: nextPrefix([]byte(outboxPrefix))})
	require.NoError(t, err)
	var key, raw []byte
	for valid := iter.First(); valid; valid = iter.Next() {
		message := &eventpb.EventMessage{}
		require.NoError(t, proto.Unmarshal(iter.Value(), message))
		if message.GetEventName() == "event.storage.collector.period.completed" {
			key = append([]byte(nil), iter.Key()...)
			raw = append([]byte(nil), iter.Value()...)
			break
		}
	}
	require.NotEmpty(t, key)
	require.NoError(t, iter.Close())
	require.NoError(t, db.Delete(key, cpebble.Sync))
	require.NoError(t, db.Set([]byte(outboxPrefix+"not-a-number"), raw, cpebble.Sync))
	require.NoError(t, db.Close())

	got, err := InspectPeriodLedger(path, "node-a", PeriodLedgerScope{SpaceID: exp.SpaceID, DatasetID: exp.DatasetID, Frequency: exp.Frequency, PeriodTimeMin: exp.PeriodTime, PeriodTimeMax: exp.PeriodTime})
	require.NoError(t, err)
	require.NotEmpty(t, got.IntegrityErrors)
}

func TestInspectPeriodLedgerRejectsDegradedPeriodWithAllSuccessBits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node")
	clock := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	store, err := Open(Options{Path: path, NodeID: "node-a", PeriodNow: func() time.Time { return clock }})
	require.NoError(t, err)
	period := time.Date(2026, 9, 30, 22, 0, 0, 0, time.UTC)
	exp := periodExpectationForTest(period, 1, "BTC-USDT")
	exp.DeadlineAt = clock.Add(time.Minute).Unix()
	_, err = store.EnsureDatasetPeriod(context.Background(), exp)
	require.NoError(t, err)
	base := periodBase(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime)
	store.datasetWriteMu.RLock()
	store.periodMu.Lock()
	_, err = store.finalizePeriodBaseLocked(context.Background(), base, time.Unix(exp.DeadlineAt, 0))
	store.periodMu.Unlock()
	store.datasetWriteMu.RUnlock()
	require.NoError(t, err)
	require.NoError(t, store.Close())

	db, err := cpebble.Open(path, &cpebble.Options{Merger: BitmapORMerger})
	require.NoError(t, err)
	require.NoError(t, db.Set(periodFieldKey(base, "bitmap"), []byte{0x01}, cpebble.Sync))
	require.NoError(t, db.Close())

	got, err := InspectPeriodLedger(path, "node-a", PeriodLedgerScope{SpaceID: exp.SpaceID, DatasetID: exp.DatasetID, Frequency: exp.Frequency, PeriodTimeMin: exp.PeriodTime, PeriodTimeMax: exp.PeriodTime})
	require.NoError(t, err)
	require.Contains(t, got.Periods[0].IntegrityErrors, "degraded period has all success bits")
}

func TestInspectPeriodLedgerRejectsDegradedPeriodWithoutPositiveDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node")
	clock := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	store, err := Open(Options{Path: path, NodeID: "node-a", PeriodNow: func() time.Time { return clock }})
	require.NoError(t, err)
	period := time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC)
	exp := periodExpectationForTest(period, 1, "BTC-USDT")
	exp.DeadlineAt = clock.Add(time.Minute).Unix()
	_, err = store.EnsureDatasetPeriod(context.Background(), exp)
	require.NoError(t, err)
	base := periodBase(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime)
	store.datasetWriteMu.RLock()
	store.periodMu.Lock()
	_, err = store.finalizePeriodBaseLocked(context.Background(), base, time.Unix(exp.DeadlineAt, 0))
	store.periodMu.Unlock()
	store.datasetWriteMu.RUnlock()
	require.NoError(t, err)
	require.NoError(t, store.Close())

	db, err := cpebble.Open(path, &cpebble.Options{Merger: BitmapORMerger})
	require.NoError(t, err)
	require.NoError(t, db.Set(periodFieldKey(base, "deadline"), int64Bytes(0), cpebble.Sync))
	require.NoError(t, db.Close())

	got, err := InspectPeriodLedger(path, "node-a", PeriodLedgerScope{SpaceID: exp.SpaceID, DatasetID: exp.DatasetID, Frequency: exp.Frequency, PeriodTimeMin: exp.PeriodTime, PeriodTimeMax: exp.PeriodTime})
	require.NoError(t, err)
	require.Contains(t, got.Periods[0].IntegrityErrors, "degraded period has no positive deadline")
}

func TestInspectPeriodLedgerRejectsEventIDNotBoundToMarkerPayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node")
	store, err := Open(Options{Path: path, NodeID: "node-a"})
	require.NoError(t, err)
	period := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	exp := periodExpectationForTest(period, 1, "BTC-USDT")
	_, err = store.EnsureDatasetPeriod(context.Background(), exp)
	require.NoError(t, err)
	_, err = store.CommitTimeSeriesBatch(context.Background(), exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: periodRowForTest(period, "BTC-USDT")}}, "commit-forged-event-id", "collector")
	require.NoError(t, err)
	require.NoError(t, store.Close())

	db, err := cpebble.Open(path, &cpebble.Options{Merger: BitmapORMerger})
	require.NoError(t, err)
	markerIter, err := db.NewIter(&cpebble.IterOptions{LowerBound: []byte(markerRecordPrefix), UpperBound: nextPrefix([]byte(markerRecordPrefix))})
	require.NoError(t, err)
	require.True(t, markerIter.First())
	oldKey := append([]byte(nil), markerIter.Key()...)
	message := &eventpb.EventMessage{}
	require.NoError(t, proto.Unmarshal(markerIter.Value(), message))
	require.NoError(t, markerIter.Close())
	message.EventId = "forged-period-event-id"
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(message)
	require.NoError(t, err)
	require.NoError(t, db.Delete(oldKey, cpebble.Sync))
	require.NoError(t, db.Set(datasetMarkerRecordKey(message.GetEventId()), raw, cpebble.Sync))
	outboxIter, err := db.NewIter(&cpebble.IterOptions{LowerBound: []byte(outboxPrefix), UpperBound: nextPrefix([]byte(outboxPrefix))})
	require.NoError(t, err)
	var outboxKey []byte
	for valid := outboxIter.First(); valid; valid = outboxIter.Next() {
		candidate := &eventpb.EventMessage{}
		require.NoError(t, proto.Unmarshal(outboxIter.Value(), candidate))
		if candidate.GetEventName() == "event.storage.collector.period.completed" {
			outboxKey = append([]byte(nil), outboxIter.Key()...)
			break
		}
	}
	require.NotEmpty(t, outboxKey)
	require.NoError(t, outboxIter.Close())
	require.NoError(t, db.Set(outboxKey, raw, cpebble.Sync))
	require.NoError(t, db.Close())

	got, err := InspectPeriodLedger(path, "node-a", PeriodLedgerScope{SpaceID: exp.SpaceID, DatasetID: exp.DatasetID, Frequency: exp.Frequency, PeriodTimeMin: exp.PeriodTime, PeriodTimeMax: exp.PeriodTime})
	require.NoError(t, err)
	require.Contains(t, got.Periods[0].IntegrityErrors, "marker event_id does not match deterministic payload hash")
	require.Equal(t, 1, strings.Count(strings.Join(got.Periods[0].IntegrityErrors, "\n"), "marker event_id does not match deterministic payload hash"))

	db, err = cpebble.Open(path, &cpebble.Options{Merger: BitmapORMerger})
	require.NoError(t, err)
	markerIter, err = db.NewIter(&cpebble.IterOptions{LowerBound: []byte(markerRecordPrefix), UpperBound: nextPrefix([]byte(markerRecordPrefix))})
	require.NoError(t, err)
	require.True(t, markerIter.First())
	markerKey := append([]byte(nil), markerIter.Key()...)
	require.NoError(t, proto.Unmarshal(markerIter.Value(), message))
	require.NoError(t, markerIter.Close())
	message.OccurredAt.Seconds++
	raw, err = proto.MarshalOptions{Deterministic: true}.Marshal(message)
	require.NoError(t, err)
	require.NoError(t, db.Set(markerKey, raw, cpebble.Sync))
	outboxIter, err = db.NewIter(&cpebble.IterOptions{LowerBound: []byte(outboxPrefix), UpperBound: nextPrefix([]byte(outboxPrefix))})
	require.NoError(t, err)
	var outboxMarkerKey []byte
	for valid := outboxIter.First(); valid; valid = outboxIter.Next() {
		candidate := &eventpb.EventMessage{}
		require.NoError(t, proto.Unmarshal(outboxIter.Value(), candidate))
		if candidate.GetEventId() == message.GetEventId() {
			outboxMarkerKey = append([]byte(nil), outboxIter.Key()...)
			break
		}
	}
	require.NoError(t, outboxIter.Close())
	require.NotEmpty(t, outboxMarkerKey)
	require.NoError(t, db.Set(outboxMarkerKey, raw, cpebble.Sync))
	require.NoError(t, db.Close())
	got, err = InspectPeriodLedger(path, "node-a", PeriodLedgerScope{SpaceID: exp.SpaceID, DatasetID: exp.DatasetID, Frequency: exp.Frequency, PeriodTimeMin: exp.PeriodTime, PeriodTimeMax: exp.PeriodTime})
	require.NoError(t, err)
	require.Contains(t, got.Periods[0].IntegrityErrors, "marker occurred_at does not match collected_at")
	require.Equal(t, 1, strings.Count(strings.Join(got.Periods[0].IntegrityErrors, "\n"), "marker occurred_at does not match collected_at"))
}

func TestInspectPeriodLedgerDoesNotExposeSubjectValuesInMarkerErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node")
	store, err := Open(Options{Path: path, NodeID: "node-a"})
	require.NoError(t, err)
	period := time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC)
	exp := periodExpectationForTest(period, 1, "SECRET-SUBJECT")
	_, err = store.EnsureDatasetPeriod(context.Background(), exp)
	require.NoError(t, err)
	_, err = store.CommitTimeSeriesBatch(context.Background(), exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: periodRowForTest(period, "SECRET-SUBJECT")}}, "commit-sensitive-marker", "collector")
	require.NoError(t, err)
	require.NoError(t, store.Close())

	db, err := cpebble.Open(path, &cpebble.Options{Merger: BitmapORMerger})
	require.NoError(t, err)
	iter, err := db.NewIter(&cpebble.IterOptions{LowerBound: []byte(markerRecordPrefix), UpperBound: nextPrefix([]byte(markerRecordPrefix))})
	require.NoError(t, err)
	require.True(t, iter.First())
	key := append([]byte(nil), iter.Key()...)
	message := &eventpb.EventMessage{}
	require.NoError(t, proto.Unmarshal(iter.Value(), message))
	require.NoError(t, iter.Close())
	payload := &storageeventpb.CollectorPeriodCompleted{}
	require.NoError(t, proto.Unmarshal(message.GetPayload(), payload))
	payload.FailedSubjects = []string{"PRIVATE-UNIVERSE-LEAK"}
	message.Payload, err = proto.MarshalOptions{Deterministic: true}.Marshal(payload)
	require.NoError(t, err)
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(message)
	require.NoError(t, err)
	require.NoError(t, db.Set(key, raw, cpebble.Sync))
	require.NoError(t, db.Close())

	_, err = InspectPeriodLedger(path, "node-a", PeriodLedgerScope{SpaceID: exp.SpaceID, DatasetID: exp.DatasetID, Frequency: exp.Frequency, PeriodTimeMin: exp.PeriodTime, PeriodTimeMax: exp.PeriodTime})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "PRIVATE-UNIVERSE-LEAK")
}

func TestPeriodInventoryTestScopeJSONDoesNotExposeSnapshotSubjects(t *testing.T) {
	entry := PeriodLedgerEntry{SnapshotFormat: "series_snapshot", SnapshotEntryCount: 1, SnapshotSubjectCount: 1}
	raw, err := json.Marshal(entry)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "subject_id")
}
