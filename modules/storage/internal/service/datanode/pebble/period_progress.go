package pebble

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"strconv"
	"strings"
	"time"

	cpebble "github.com/cockroachdb/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	periodProgressPrefix = "__dataset_period/"
	periodWaitingPrefix  = "__dataset_period_waiting/"
	periodDeadlinePrefix = "__dataset_period_deadline/"
	periodCompletePrefix = "__dataset_period_complete/"
	periodFinalizeBatch  = 256
)

// BitmapORMerger intentionally keeps Pebble's historical merger name. Existing
// MooX DataNode stores were created with pebble.concatenate, and Pebble refuses
// to reopen a DB when the configured merger name changes. Before this feature
// MooX never issued Merge operations (bitmap progress is the first use), so
// there are no legacy concatenate operands whose semantics could be changed.
// Keeping the persisted name lets existing row-only stores reopen while all new
// Merge operands use the bitmap OR semantics below.
var BitmapORMerger = &cpebble.Merger{
	Name: cpebble.DefaultMerger.Name,
	Merge: func(_ []byte, value []byte) (cpebble.ValueMerger, error) {
		return &bitmapORValueMerger{value: append([]byte(nil), value...)}, nil
	},
}

type bitmapORValueMerger struct{ value []byte }

func (m *bitmapORValueMerger) merge(value []byte) {
	if len(value) > len(m.value) {
		m.value = append(m.value, make([]byte, len(value)-len(m.value))...)
	}
	for i := range value {
		m.value[i] |= value[i]
	}
}
func (m *bitmapORValueMerger) MergeNewer(value []byte) error { m.merge(value); return nil }
func (m *bitmapORValueMerger) MergeOlder(value []byte) error { m.merge(value); return nil }
func (m *bitmapORValueMerger) Finish(bool) ([]byte, io.Closer, error) {
	return m.value, nil, nil
}

type DatasetPeriodSeries struct {
	SeriesIndex uint32 `json:"series_index"`
	SubjectID   string `json:"subject_id"`
}

type DatasetPeriodExpectation struct {
	SpaceID       string
	DatasetID     string
	Frequency     string
	PeriodTime    int64
	SeriesHash    string
	ExpectedCount uint32
	DeadlineAt    int64
	Roster        []DatasetPeriodSeries
}

type TimeSeriesBatchItem struct {
	SeriesIndex uint32
	Row         *pb.RowFieldUpsert
}

type DatasetPeriodProgress struct {
	DatasetPeriodExpectation
	Status              string
	Bitmap              []byte
	FailedSeriesIndexes []uint32
}

func (s *Store) EnsureDatasetPeriod(ctx context.Context, exp DatasetPeriodExpectation) (string, error) {
	exp, err := normalizePeriodExpectationWithRoster(exp)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.datasetWriteMu.RLock()
	defer s.datasetWriteMu.RUnlock()
	deleted, err := s.isDatasetDeleted(exp.SpaceID, exp.DatasetID)
	if err != nil {
		return "", err
	}
	if deleted {
		return "", ErrDatasetDeleted
	}
	s.periodMu.Lock()
	defer s.periodMu.Unlock()
	base := periodBase(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime)
	status, found, err := s.readPeriodStatus(base)
	if err != nil {
		return "", err
	}
	if found {
		current, err := s.readPeriodExpectation(base)
		if err != nil {
			return "", err
		}
		// DeadlineAt is a scheduling/finalization hint, not part of the immutable
		// Dataset period identity. A retry in a later scheduler tick can carry a
		// newer deadline for the same series snapshot; keep the first persisted
		// deadline so retries cannot indefinitely extend a waiting period.
		if !samePeriodCommitIdentity(current, exp) || !samePeriodRoster(current.Roster, exp.Roster) {
			return "", PeriodConflictError{SpaceID: exp.SpaceID, DatasetID: exp.DatasetID, Frequency: exp.Frequency, PeriodTime: exp.PeriodTime}
		}
		return status, nil
	}
	batch := s.db.NewBatch()
	defer batch.Close()
	if err := batch.Set(periodFieldKey(base, "series_hash"), []byte(exp.SeriesHash), s.writeOptions); err != nil {
		return "", err
	}
	if err := batch.Set(periodFieldKey(base, "expected_count"), uint32Bytes(exp.ExpectedCount), s.writeOptions); err != nil {
		return "", err
	}
	rosterRaw, err := json.Marshal(exp.Roster)
	if err != nil {
		return "", err
	}
	if err := batch.Set(periodFieldKey(base, "roster"), rosterRaw, s.writeOptions); err != nil {
		return "", err
	}
	if err := batch.Set(periodFieldKey(base, "deadline"), int64Bytes(exp.DeadlineAt), s.writeOptions); err != nil {
		return "", err
	}
	if err := batch.Set(periodFieldKey(base, "status"), []byte("waiting"), s.writeOptions); err != nil {
		return "", err
	}
	if err := batch.Set(periodFieldKey(base, "bitmap"), make([]byte, bitmapSize(exp.ExpectedCount)), s.writeOptions); err != nil {
		return "", err
	}
	if err := batch.Set(periodFieldKey(base, "failures"), make([]byte, bitmapSize(exp.ExpectedCount)), s.writeOptions); err != nil {
		return "", err
	}
	if err := batch.Set(periodWaitingKey(base), []byte(base), s.writeOptions); err != nil {
		return "", err
	}
	if exp.DeadlineAt > 0 {
		if err := batch.Set(periodDeadlineKey(exp.DeadlineAt, base), []byte(base), s.writeOptions); err != nil {
			return "", err
		}
	}
	if err := batch.Commit(s.writeOptions); err != nil {
		return "", err
	}
	return "waiting", nil
}

func (s *Store) CommitTimeSeriesBatch(ctx context.Context, exp DatasetPeriodExpectation, items []TimeSeriesBatchItem, sourceEventID, writeSource string) (string, error) {
	exp, err := normalizePeriodExpectation(exp)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "", invalid("time-series batch items are required")
	}
	rows := make([]*pb.RowFieldUpsert, 0, len(items))
	for _, item := range items {
		if item.Row == nil {
			return "", invalid("period batch row is required")
		}
		rows = append(rows, item.Row)
	}
	normalizedRows, err := s.normalizeWriteRows(ctx, rows)
	if err != nil {
		return "", err
	}
	base := periodBase(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime)
	s.datasetWriteMu.RLock()
	defer s.datasetWriteMu.RUnlock()
	s.periodMu.Lock()
	current, err := s.readPeriodExpectation(base)
	if err != nil {
		s.periodMu.Unlock()
		return "", err
	}
	if !samePeriodCommitIdentity(current, exp) {
		s.periodMu.Unlock()
		return "", PeriodConflictError{SpaceID: exp.SpaceID, DatasetID: exp.DatasetID, Frequency: exp.Frequency, PeriodTime: exp.PeriodTime}
	}
	exp = current
	status, found, err := s.readPeriodStatus(base)
	if err != nil {
		s.periodMu.Unlock()
		return "", err
	}
	if !found {
		s.periodMu.Unlock()
		return "", invalid("dataset period is not initialized")
	}
	if status != "waiting" {
		s.periodMu.Unlock()
		return status, nil
	}
	if exp.DeadlineAt > 0 && time.Now().UTC().Unix() >= exp.DeadlineAt {
		// Resolve a missed deadline while still serialized with every commit.
		// Otherwise a late write could set the final success bit before the
		// background finalizer runs and change a due degraded marker to complete.
		s.periodMu.Unlock()
		return s.finalizePeriodBase(ctx, base, time.Now().UTC())
	}
	delta := make([]byte, bitmapSize(exp.ExpectedCount))
	for _, item := range items {
		if item.SeriesIndex >= exp.ExpectedCount {
			s.periodMu.Unlock()
			return "", invalidf("series_index %d exceeds expected_count %d", item.SeriesIndex, exp.ExpectedCount)
		}
		isTarget, err := validatePeriodRow(exp, item.SeriesIndex, item.Row)
		if err != nil {
			s.periodMu.Unlock()
			return "", err
		}
		if isTarget {
			setBitmapBit(delta, item.SeriesIndex)
		}
	}
	failures, err := s.readPeriodFailureBitmap(base, exp.ExpectedCount)
	if err != nil {
		s.periodMu.Unlock()
		return "", err
	}
	for _, item := range items {
		if item.SeriesIndex < exp.ExpectedCount && bitmapBitSet(delta, item.SeriesIndex) {
			clearBitmapBit(failures, item.SeriesIndex)
		}
	}
	success, err := s.readPeriodBitmap(base)
	if err != nil {
		s.periodMu.Unlock()
		return "", err
	}
	for index := uint32(0); index < exp.ExpectedCount; index++ {
		if bitmapBitSet(delta, index) {
			setBitmapBit(success, index)
		}
	}
	allSucceeded := bitmapAllSet(success, exp.ExpectedCount)
	writeEvent := func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		if strings.TrimSpace(sourceEventID) == "" {
			return BuildDatasetRowsUpsertedMessageWithSource(s.nodeID, writeSource, spaceID, datasetID, rows)
		}
		return BuildDatasetRowsUpsertedMessageForSourceWithWriteSource(s.nodeID, sourceEventID, writeSource, spaceID, datasetID, rows)
	}
	s.outboxMu.Lock()
	_, err = s.writeFieldsEventLocked(ctx, normalizedRows, strings.TrimSpace(sourceEventID), writeEvent, func(batch *cpebble.Batch, _ []*OutboxEntry) error {
		if err := batch.Merge(periodFieldKey(base, "bitmap"), delta, s.writeOptions); err != nil {
			return err
		}
		if err := batch.Set(periodFieldKey(base, "failures"), failures, s.writeOptions); err != nil {
			return err
		}
		if allSucceeded {
			return batch.Set(periodCompleteKey(base), []byte(base), s.writeOptions)
		}
		return nil
	})
	s.outboxMu.Unlock()
	s.periodMu.Unlock()
	if err != nil {
		return "", err
	}
	return s.FinalizeDatasetPeriod(ctx, exp, time.Now().UTC())
}

// RecordDatasetPeriodFailures persistently marks exhausted series without
// advancing the success bitmap or finalizing the period. Repeated indexes are
// idempotent; an already successful series is never reintroduced as failed.
func (s *Store) RecordDatasetPeriodFailures(ctx context.Context, exp DatasetPeriodExpectation, seriesIndexes []uint32) (string, error) {
	exp, err := normalizePeriodExpectation(exp)
	if err != nil {
		return "", err
	}
	if len(seriesIndexes) == 0 {
		return "", invalid("failed series_indexes are required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	base := periodBase(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime)
	s.datasetWriteMu.RLock()
	defer s.datasetWriteMu.RUnlock()
	s.periodMu.Lock()
	defer s.periodMu.Unlock()
	current, err := s.readPeriodExpectation(base)
	if err != nil {
		return "", err
	}
	if !samePeriodCommitIdentity(current, exp) {
		return "", PeriodConflictError{SpaceID: exp.SpaceID, DatasetID: exp.DatasetID, Frequency: exp.Frequency, PeriodTime: exp.PeriodTime}
	}
	status, found, err := s.readPeriodStatus(base)
	if err != nil {
		return "", err
	}
	if !found {
		return "", invalid("dataset period is not initialized")
	}
	if status != "waiting" {
		return status, nil
	}
	if len(current.Roster) != int(current.ExpectedCount) {
		return "", invalid("dataset period roster is unavailable for failure reporting")
	}
	success, err := s.readPeriodBitmap(base)
	if err != nil {
		return "", err
	}
	failures, err := s.readPeriodFailureBitmap(base, current.ExpectedCount)
	if err != nil {
		return "", err
	}
	for _, index := range seriesIndexes {
		if index >= current.ExpectedCount || int(index) >= len(current.Roster) || current.Roster[index].SeriesIndex != index || strings.TrimSpace(current.Roster[index].SubjectID) == "" {
			return "", invalidf("series_index %d is not in the period roster", index)
		}
		if bitmapBitSet(success, index) {
			continue
		}
		setBitmapBit(failures, index)
	}
	if err := s.db.Set(periodFieldKey(base, "failures"), failures, s.writeOptions); err != nil {
		return "", err
	}
	return "waiting", nil
}

func (s *Store) FinalizeDatasetPeriod(ctx context.Context, exp DatasetPeriodExpectation, now time.Time) (string, error) {
	exp, err := normalizePeriodExpectation(exp)
	if err != nil {
		return "", err
	}
	return s.finalizePeriodBase(ctx, periodBase(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime), now)
}

func (s *Store) finalizePeriodBase(ctx context.Context, base string, now time.Time) (string, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.periodMu.Lock()
	defer s.periodMu.Unlock()
	status, found, err := s.readPeriodStatus(base)
	if err != nil || !found || status != "waiting" {
		return status, err
	}
	exp, err := s.readPeriodExpectation(base)
	if err != nil {
		return "", err
	}
	bitmap, err := s.readPeriodBitmap(base)
	if err != nil {
		return "", err
	}
	failures, err := s.readPeriodFailureBitmap(base, exp.ExpectedCount)
	if err != nil {
		return "", err
	}
	complete := bitmapAllSet(bitmap, exp.ExpectedCount)
	terminal := ""
	if complete {
		terminal = "complete"
	} else if exp.DeadlineAt > 0 && now.Unix() >= exp.DeadlineAt {
		terminal = "degraded"
	} else {
		return "waiting", nil
	}
	universe, failed := periodMarkerSubjects(exp, bitmap, failures)
	marker := &pb.CollectorPeriodCompletedMarker{
		DatasetId: exp.DatasetID, Frequency: exp.Frequency, PeriodTime: exp.PeriodTime,
		Status: terminal, BatchId: periodCompletionID(base), ConfigSnapshotId: exp.SeriesHash,
		ExpectedScopeRef: exp.SeriesHash, UniverseSubjectIds: universe, FailedSubjects: failed, CollectedAt: timestamppb.New(now.UTC()),
	}
	raw, _, err := BuildCollectorPeriodCompletedMessage(exp.SpaceID, marker)
	if err != nil {
		return "", err
	}
	message := &eventpb.EventMessage{}
	if err := proto.Unmarshal(raw, message); err != nil {
		return "", err
	}
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()
	recordKey := datasetMarkerRecordKey(message.GetEventId())
	if existing, exists, err := s.readMarkerRecord(recordKey); err != nil {
		return "", err
	} else if exists && !sameMarkerPayload(existing, message) {
		return "", ConflictError{EventID: message.GetEventId()}
	} else if exists {
		return terminal, nil
	}
	nextID, err := s.nextOutboxID()
	if err != nil {
		return "", err
	}
	batch := s.db.NewBatch()
	defer batch.Close()
	if err := batch.Set(periodFieldKey(base, "status"), []byte(terminal), s.writeOptions); err != nil {
		return "", err
	}
	if err := batch.Delete(periodWaitingKey(base), s.writeOptions); err != nil {
		return "", err
	}
	if err := batch.Delete(periodCompleteKey(base), s.writeOptions); err != nil {
		return "", err
	}
	if exp.DeadlineAt > 0 {
		if err := batch.Delete(periodDeadlineKey(exp.DeadlineAt, base), s.writeOptions); err != nil {
			return "", err
		}
	}
	if err := batch.Set(recordKey, raw, s.writeOptions); err != nil {
		return "", err
	}
	if err := batch.Set([]byte(outboxKey(nextID)), raw, s.writeOptions); err != nil {
		return "", err
	}
	if err := s.setNextOutboxID(batch, nextID+1); err != nil {
		return "", err
	}
	if err := batch.Commit(s.writeOptions); err != nil {
		return "", err
	}
	s.noteOutboxCommitted(1, now.UTC())
	return terminal, nil
}

func (s *Store) startPeriodFinalizer() {
	if s == nil || s.historyCtx == nil {
		return
	}
	s.maintenanceWG.Add(1)
	go func() {
		defer s.maintenanceWG.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-s.historyCtx.Done():
				return
			case now := <-ticker.C:
				_, _ = s.FinalizeWaitingDatasetPeriods(s.historyCtx, now.UTC(), periodFinalizeBatch)
			}
		}
	}()
}

func (s *Store) FinalizeWaitingDatasetPeriods(ctx context.Context, now time.Time, limit int) (int, error) {
	if limit <= 0 || limit > periodFinalizeBatch {
		limit = periodFinalizeBatch
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	bases := make([]string, 0, limit)
	seen := make(map[string]struct{}, limit)
	appendFromIndex := func(prefix string, dueDeadline bool) error {
		iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: []byte(prefix), UpperBound: nextPrefix([]byte(prefix))})
		if err != nil {
			return err
		}
		defer iter.Close()
		for valid := iter.First(); valid && len(bases) < limit; valid = iter.Next() {
			if dueDeadline {
				key := string(iter.Key())
				suffix := strings.TrimPrefix(key, periodDeadlinePrefix)
				deadlinePart, _, ok := strings.Cut(suffix, "/")
				deadline, parseErr := strconv.ParseInt(deadlinePart, 10, 64)
				if !ok || parseErr != nil {
					return errors.New("dataset period deadline index is corrupted")
				}
				if deadline > now.UTC().Unix() {
					break
				}
			}
			base := string(append([]byte(nil), iter.Value()...))
			if _, exists := seen[base]; exists {
				continue
			}
			seen[base] = struct{}{}
			bases = append(bases, base)
		}
		return iter.Error()
	}
	// Complete periods are normally finalized by CommitTimeSeriesBatch. This
	// durable index also recovers a process crash between the atomic row/bitmap
	// commit and the follow-up finalizer call.
	if err := appendFromIndex(periodCompletePrefix, false); err != nil {
		return 0, err
	}
	// Deadline keys are ordered by deadline, so even a large queue of future
	// periods cannot starve an expired one.
	if len(bases) < limit {
		if err := appendFromIndex(periodDeadlinePrefix, true); err != nil {
			return 0, err
		}
	}
	// The waiting index is the recovery source of truth. Scan it after ready
	// and due entries so a prior build's full bitmap (or a process crash before
	// writing the ready hint) can still converge without starving deadlines.
	if len(bases) < limit {
		if err := appendFromIndex(periodWaitingPrefix, false); err != nil {
			return 0, err
		}
	}
	finalized := 0
	for _, base := range bases {
		status, err := s.finalizePeriodBase(ctx, base, now)
		if err != nil {
			return finalized, err
		}
		if status == "complete" || status == "degraded" {
			finalized++
		}
	}
	return finalized, nil
}

func (s *Store) GetDatasetPeriodProgress(ctx context.Context, exp DatasetPeriodExpectation) (*DatasetPeriodProgress, error) {
	exp, err := normalizePeriodExpectation(exp)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	base := periodBase(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime)
	current, err := s.readPeriodExpectation(base)
	if err != nil {
		return nil, err
	}
	status, found, err := s.readPeriodStatus(base)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, cpebble.ErrNotFound
	}
	bitmap, err := s.readPeriodBitmap(base)
	if err != nil {
		return nil, err
	}
	failures, err := s.readPeriodFailureBitmap(base, current.ExpectedCount)
	if err != nil {
		return nil, err
	}
	failedIndexes := make([]uint32, 0)
	for index := uint32(0); index < current.ExpectedCount; index++ {
		if bitmapBitSet(failures, index) && !bitmapBitSet(bitmap, index) {
			failedIndexes = append(failedIndexes, index)
		}
	}
	return &DatasetPeriodProgress{DatasetPeriodExpectation: current, Status: status, Bitmap: bitmap, FailedSeriesIndexes: failedIndexes}, nil
}

func normalizePeriodExpectation(exp DatasetPeriodExpectation) (DatasetPeriodExpectation, error) {
	exp.SpaceID = strings.TrimSpace(exp.SpaceID)
	exp.DatasetID = strings.TrimSpace(exp.DatasetID)
	exp.Frequency = strings.ToLower(strings.TrimSpace(exp.Frequency))
	exp.SeriesHash = strings.TrimSpace(exp.SeriesHash)
	if exp.SpaceID == "" || exp.DatasetID == "" || exp.Frequency == "" || exp.PeriodTime <= 0 || exp.SeriesHash == "" || exp.ExpectedCount == 0 {
		return exp, invalid("space_id, dataset_id, frequency, period_time, series_hash and expected_count are required")
	}
	return exp, nil
}

func normalizePeriodExpectationWithRoster(exp DatasetPeriodExpectation) (DatasetPeriodExpectation, error) {
	exp, err := normalizePeriodExpectation(exp)
	if err != nil {
		return exp, err
	}
	if len(exp.Roster) != int(exp.ExpectedCount) {
		return exp, invalid("period roster size must equal expected_count")
	}
	for index := range exp.Roster {
		exp.Roster[index].SubjectID = strings.TrimSpace(exp.Roster[index].SubjectID)
		if exp.Roster[index].SeriesIndex != uint32(index) {
			return exp, invalid("period roster series_index must be dense from zero")
		}
		if exp.Roster[index].SubjectID == "" {
			return exp, invalid("period roster subject_id is required")
		}
	}
	return exp, nil
}

func samePeriodRoster(left, right []DatasetPeriodSeries) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].SeriesIndex != right[i].SeriesIndex || strings.TrimSpace(left[i].SubjectID) != strings.TrimSpace(right[i].SubjectID) {
			return false
		}
	}
	return true
}

func validatePeriodRow(exp DatasetPeriodExpectation, seriesIndex uint32, row *pb.RowFieldUpsert) (bool, error) {
	if row == nil || row.GetKey() == nil || row.GetKey().GetTimeSeries() == nil {
		return false, invalid("period batch rows must be time-series rows")
	}
	key := row.GetKey()
	series := key.GetTimeSeries()
	if strings.TrimSpace(key.GetSpaceId()) != exp.SpaceID || strings.TrimSpace(key.GetDatasetId()) != exp.DatasetID || !strings.EqualFold(strings.TrimSpace(series.GetFreq()), exp.Frequency) {
		return false, invalid("period batch row identity does not match expectation")
	}
	if len(exp.Roster) > 0 {
		if int(seriesIndex) >= len(exp.Roster) || exp.Roster[seriesIndex].SeriesIndex != seriesIndex {
			return false, invalidf("series_index %d is not in the period roster", seriesIndex)
		}
		if strings.TrimSpace(series.GetSubjectId()) != strings.TrimSpace(exp.Roster[seriesIndex].SubjectID) {
			return false, invalidf("series_index %d subject_id does not match the period roster", seriesIndex)
		}
	}
	at, err := time.Parse(time.RFC3339Nano, series.GetDataTime())
	if err != nil {
		return false, invalidf("period batch data_time is invalid: %v", err)
	}
	return at.UTC().Unix() == exp.PeriodTime, nil
}

func samePeriodCommitIdentity(left, right DatasetPeriodExpectation) bool {
	return left.SpaceID == right.SpaceID && left.DatasetID == right.DatasetID && left.Frequency == right.Frequency && left.PeriodTime == right.PeriodTime && left.SeriesHash == right.SeriesHash && left.ExpectedCount == right.ExpectedCount
}

func (s *Store) readPeriodExpectation(base string) (DatasetPeriodExpectation, error) {
	spaceID, datasetID, frequency, periodTime, err := parsePeriodBase(base)
	if err != nil {
		return DatasetPeriodExpectation{}, err
	}
	seriesHash, err := s.readPeriodValue(periodFieldKey(base, "series_hash"))
	if err != nil {
		return DatasetPeriodExpectation{}, err
	}
	expectedRaw, err := s.readPeriodValue(periodFieldKey(base, "expected_count"))
	if err != nil {
		return DatasetPeriodExpectation{}, err
	}
	deadlineRaw, err := s.readPeriodValue(periodFieldKey(base, "deadline"))
	if err != nil {
		return DatasetPeriodExpectation{}, err
	}
	if len(expectedRaw) != 4 || len(deadlineRaw) != 8 {
		return DatasetPeriodExpectation{}, errors.New("dataset period expectation is corrupted")
	}
	exp := DatasetPeriodExpectation{SpaceID: spaceID, DatasetID: datasetID, Frequency: frequency, PeriodTime: periodTime, SeriesHash: string(seriesHash), ExpectedCount: binary.BigEndian.Uint32(expectedRaw), DeadlineAt: int64(binary.BigEndian.Uint64(deadlineRaw))}
	rosterRaw, rosterErr := s.readPeriodValue(periodFieldKey(base, "roster"))
	if rosterErr == nil {
		if err := json.Unmarshal(rosterRaw, &exp.Roster); err != nil {
			return DatasetPeriodExpectation{}, fmt.Errorf("dataset period roster is corrupted: %w", err)
		}
	} else if !errors.Is(rosterErr, cpebble.ErrNotFound) {
		return DatasetPeriodExpectation{}, rosterErr
	}
	return exp, nil
}

func (s *Store) readPeriodStatus(base string) (string, bool, error) {
	value, closer, err := s.db.Get(periodFieldKey(base, "status"))
	if errors.Is(err, cpebble.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	status := string(append([]byte(nil), value...))
	if err := closer.Close(); err != nil {
		return "", false, err
	}
	return status, true, nil
}

func (s *Store) readPeriodBitmap(base string) ([]byte, error) {
	return s.readPeriodValue(periodFieldKey(base, "bitmap"))
}

func (s *Store) readPeriodFailureBitmap(base string, expectedCount uint32) ([]byte, error) {
	value, err := s.readPeriodValue(periodFieldKey(base, "failures"))
	if errors.Is(err, cpebble.ErrNotFound) {
		return make([]byte, bitmapSize(expectedCount)), nil
	}
	if err != nil {
		return nil, err
	}
	if len(value) != bitmapSize(expectedCount) {
		return nil, errors.New("dataset period failure bitmap is corrupted")
	}
	return value, nil
}

func (s *Store) readPeriodValue(key []byte) ([]byte, error) {
	value, closer, err := s.db.Get(key)
	if err != nil {
		return nil, err
	}
	copyValue := append([]byte(nil), value...)
	if err := closer.Close(); err != nil {
		return nil, err
	}
	return copyValue, nil
}

func periodBase(spaceID, datasetID, frequency string, periodTime int64) string {
	return periodProgressPrefix + hex.EncodeToString([]byte(spaceID)) + "/" + hex.EncodeToString([]byte(datasetID)) + "/" + hex.EncodeToString([]byte(strings.ToLower(frequency))) + "/" + fmt.Sprintf("%020d", periodTime) + "/"
}

func parsePeriodBase(base string) (string, string, string, int64, error) {
	if !strings.HasPrefix(base, periodProgressPrefix) {
		return "", "", "", 0, errors.New("invalid dataset period key")
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(base, periodProgressPrefix), "/"), "/")
	if len(parts) != 4 {
		return "", "", "", 0, errors.New("invalid dataset period key")
	}
	decoded := make([]string, 3)
	for i := 0; i < 3; i++ {
		value, err := hex.DecodeString(parts[i])
		if err != nil {
			return "", "", "", 0, err
		}
		decoded[i] = string(value)
	}
	periodTime, err := strconv.ParseInt(parts[3], 10, 64)
	return decoded[0], decoded[1], decoded[2], periodTime, err
}

func periodCompletionID(base string) string {
	hash := sha256.Sum256([]byte(base))
	return "dataset-period-" + hex.EncodeToString(hash[:16])
}

func periodFieldKey(base, field string) []byte { return []byte(base + field) }
func periodWaitingKey(base string) []byte {
	return []byte(periodWaitingPrefix + hex.EncodeToString([]byte(base)))
}
func periodDeadlineKey(deadline int64, base string) []byte {
	return []byte(periodDeadlinePrefix + fmt.Sprintf("%020d", deadline) + "/" + hex.EncodeToString([]byte(base)))
}
func periodCompleteKey(base string) []byte {
	return []byte(periodCompletePrefix + hex.EncodeToString([]byte(base)))
}
func periodMarkerSubjects(exp DatasetPeriodExpectation, success, failures []byte) ([]string, []string) {
	universe := make([]string, 0, len(exp.Roster))
	failed := make([]string, 0)
	universeSeen := make(map[string]struct{}, len(exp.Roster))
	failedSeen := make(map[string]struct{})
	for index, series := range exp.Roster {
		subjectID := strings.TrimSpace(series.SubjectID)
		if subjectID == "" {
			continue
		}
		if _, exists := universeSeen[subjectID]; !exists {
			universeSeen[subjectID] = struct{}{}
			universe = append(universe, subjectID)
		}
		idx := uint32(index)
		if bitmapBitSet(failures, idx) && !bitmapBitSet(success, idx) {
			if _, exists := failedSeen[subjectID]; !exists {
				failedSeen[subjectID] = struct{}{}
				failed = append(failed, subjectID)
			}
		}
	}
	return universe, failed
}

func bitmapSize(count uint32) int { return int((count + 7) / 8) }
func setBitmapBit(bitmap []byte, index uint32) {
	bitmap[index/8] |= byte(1 << (index % 8))
}
func clearBitmapBit(bitmap []byte, index uint32) {
	if int(index/8) >= len(bitmap) {
		return
	}
	bitmap[index/8] &^= byte(1 << (index % 8))
}
func bitmapBitSet(bitmap []byte, index uint32) bool {
	if int(index/8) >= len(bitmap) {
		return false
	}
	return bitmap[index/8]&byte(1<<(index%8)) != 0
}
func bitmapCount(bitmap []byte) int {
	count := 0
	for _, value := range bitmap {
		count += bits.OnesCount8(value)
	}
	return count
}
func bitmapAllSet(bitmap []byte, count uint32) bool {
	for index := uint32(0); index < count; index++ {
		if !bitmapBitSet(bitmap, index) {
			return false
		}
	}
	return true
}
func uint32Bytes(value uint32) []byte {
	out := make([]byte, 4)
	binary.BigEndian.PutUint32(out, value)
	return out
}
func int64Bytes(value int64) []byte {
	out := make([]byte, 8)
	binary.BigEndian.PutUint64(out, uint64(value))
	return out
}
