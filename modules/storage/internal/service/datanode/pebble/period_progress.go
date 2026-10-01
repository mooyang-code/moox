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
	"log"
	"math/bits"
	"sort"
	"strconv"
	"strings"
	"time"

	cpebble "github.com/cockroachdb/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/rowidentity"
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
	periodFinalizeCursor = "__dataset_period_finalize_cursor/"
	periodFinalizeOrder  = "__dataset_period_finalize_order"
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
	SeriesTag   string `json:"series_tag"`
}

type DatasetPeriodExpectation struct {
	SpaceID        string
	DatasetID      string
	Frequency      string
	PeriodTime     int64
	SeriesHash     string
	ExpectedCount  uint32
	DeadlineAt     int64
	SeriesSnapshot []DatasetPeriodSeries `json:"series_snapshot"`
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

type DatasetPeriodResult struct {
	Status                string
	DeadlineAt            int64
	AcceptedSeriesIndexes []uint32
	FailureResults        []*pb.DatasetPeriodFailureResult
}

func (s *Store) EnsureDatasetPeriod(ctx context.Context, exp DatasetPeriodExpectation) (DatasetPeriodResult, error) {
	exp, err := normalizePeriodExpectationWithSeriesSnapshot(exp)
	if err != nil {
		return DatasetPeriodResult{}, err
	}
	if exp.DeadlineAt <= 0 {
		return DatasetPeriodResult{}, invalid("deadline_at must be positive")
	}
	if err := ctx.Err(); err != nil {
		return DatasetPeriodResult{}, err
	}
	s.datasetWriteMu.RLock()
	defer s.datasetWriteMu.RUnlock()
	deleted, err := s.isDatasetDeleted(exp.SpaceID, exp.DatasetID)
	if err != nil {
		return DatasetPeriodResult{}, err
	}
	if deleted {
		return DatasetPeriodResult{}, ErrDatasetDeleted
	}
	s.periodMu.Lock()
	defer s.periodMu.Unlock()
	base := periodBase(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime)
	status, found, err := s.readPeriodStatus(base)
	if err != nil {
		return DatasetPeriodResult{}, err
	}
	if found {
		current, err := s.readPeriodExpectation(base)
		if err != nil {
			return DatasetPeriodResult{}, err
		}
		// DeadlineAt is a scheduling/finalization hint, not part of the immutable
		// Dataset period identity. A retry in a later scheduler tick can carry a
		// newer deadline for the same series snapshot; keep the first persisted
		// deadline so retries cannot indefinitely extend a waiting period.
		if !samePeriodCommitIdentity(current, exp) || !samePeriodSeriesSnapshot(current.SeriesSnapshot, exp.SeriesSnapshot) {
			return DatasetPeriodResult{}, PeriodConflictError{SpaceID: exp.SpaceID, DatasetID: exp.DatasetID, Frequency: exp.Frequency, PeriodTime: exp.PeriodTime}
		}
		return DatasetPeriodResult{Status: status, DeadlineAt: current.DeadlineAt}, nil
	}
	// Marker size is a creation constraint. Existing periods retain their
	// original contract even if deployment limits are lowered later.
	if err := s.validatePeriodUniverseSize(exp); err != nil {
		return DatasetPeriodResult{}, err
	}
	batch := s.db.NewBatch()
	defer batch.Close()
	if err := batch.Set(periodFieldKey(base, "series_hash"), []byte(exp.SeriesHash), s.writeOptions); err != nil {
		return DatasetPeriodResult{}, err
	}
	if err := batch.Set(periodFieldKey(base, "expected_count"), uint32Bytes(exp.ExpectedCount), s.writeOptions); err != nil {
		return DatasetPeriodResult{}, err
	}
	seriesSnapshotRaw, err := json.Marshal(exp.SeriesSnapshot)
	if err != nil {
		return DatasetPeriodResult{}, err
	}
	if err := batch.Set(periodFieldKey(base, "series_snapshot"), seriesSnapshotRaw, s.writeOptions); err != nil {
		return DatasetPeriodResult{}, err
	}
	if err := batch.Set(periodFieldKey(base, "deadline"), int64Bytes(exp.DeadlineAt), s.writeOptions); err != nil {
		return DatasetPeriodResult{}, err
	}
	if err := batch.Set(periodFieldKey(base, "status"), []byte("waiting"), s.writeOptions); err != nil {
		return DatasetPeriodResult{}, err
	}
	if err := batch.Set(periodFieldKey(base, "bitmap"), make([]byte, bitmapSize(exp.ExpectedCount)), s.writeOptions); err != nil {
		return DatasetPeriodResult{}, err
	}
	if err := batch.Set(periodFieldKey(base, "failures"), make([]byte, bitmapSize(exp.ExpectedCount)), s.writeOptions); err != nil {
		return DatasetPeriodResult{}, err
	}
	if err := batch.Set(periodWaitingKey(base), []byte(base), s.writeOptions); err != nil {
		return DatasetPeriodResult{}, err
	}
	if exp.DeadlineAt > 0 {
		if err := batch.Set(periodDeadlineKey(exp.DeadlineAt, base), []byte(base), s.writeOptions); err != nil {
			return DatasetPeriodResult{}, err
		}
	}
	if err := batch.Commit(s.writeOptions); err != nil {
		return DatasetPeriodResult{}, err
	}
	return DatasetPeriodResult{Status: "waiting", DeadlineAt: exp.DeadlineAt}, nil
}

func (s *Store) validatePeriodUniverseSize(exp DatasetPeriodExpectation) error {
	if s.maxEventBytes <= 0 {
		return nil
	}
	universe, _ := periodMarkerSubjects(exp, nil, nil)
	marker := &pb.CollectorPeriodCompletedMarker{
		DatasetId: exp.DatasetID, Frequency: exp.Frequency, PeriodTime: exp.PeriodTime,
		Status: "degraded", BatchId: periodCompletionID(periodBase(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime)),
		ConfigSnapshotId: exp.SeriesHash, ExpectedScopeRef: exp.SeriesHash,
		UniverseSubjectIds: universe, FailedSubjects: append([]string(nil), universe...),
		CollectedAt: timestamppb.New(time.Date(9999, time.December, 31, 23, 59, 59, 999999999, time.UTC)),
	}
	raw, _, err := BuildCollectorPeriodCompletedMessage(exp.SpaceID, marker)
	if err != nil {
		return err
	}
	return s.validatePeriodMarkerPayloadSize(raw)
}

func (s *Store) validatePeriodMarkerPayloadSize(raw []byte) error {
	if s.maxEventBytes > 0 && len(raw) > s.maxEventBytes {
		return invalidf("event payload size %d exceeds limit %d", len(raw), s.maxEventBytes)
	}
	return nil
}

func (s *Store) CommitTimeSeriesBatch(ctx context.Context, exp DatasetPeriodExpectation, items []TimeSeriesBatchItem, sourceEventID, writeSource string) (DatasetPeriodResult, error) {
	exp, err := normalizePeriodExpectation(exp)
	if err != nil {
		return DatasetPeriodResult{}, err
	}
	if len(items) == 0 {
		return DatasetPeriodResult{}, invalid("time-series batch items are required")
	}
	rows := make([]*pb.RowFieldUpsert, 0, len(items))
	for _, item := range items {
		if item.Row == nil {
			return DatasetPeriodResult{}, invalid("period batch row is required")
		}
		rows = append(rows, item.Row)
	}
	normalizedRows, err := s.normalizeWriteRows(ctx, rows)
	if err != nil {
		return DatasetPeriodResult{}, err
	}
	base := periodBase(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime)
	s.datasetWriteMu.RLock()
	defer s.datasetWriteMu.RUnlock()
	s.periodMu.Lock()
	defer s.periodMu.Unlock()
	current, err := s.readPeriodExpectation(base)
	if err != nil {
		return DatasetPeriodResult{}, err
	}
	if !samePeriodCommitIdentity(current, exp) {
		return DatasetPeriodResult{}, PeriodConflictError{SpaceID: exp.SpaceID, DatasetID: exp.DatasetID, Frequency: exp.Frequency, PeriodTime: exp.PeriodTime}
	}
	exp = current
	delta := make([]byte, bitmapSize(exp.ExpectedCount))
	for i, item := range items {
		isTarget, err := validatePeriodRow(exp, item.SeriesIndex, normalizedRows[i])
		if err != nil {
			return DatasetPeriodResult{}, err
		}
		if isTarget {
			setBitmapBit(delta, item.SeriesIndex)
		}
	}
	status, found, err := s.readPeriodStatus(base)
	if err != nil {
		return DatasetPeriodResult{}, err
	}
	if !found {
		return DatasetPeriodResult{}, invalid("dataset period is not initialized")
	}
	now := s.periodNow().UTC()
	if status != "waiting" || periodDeadlineReached(exp, now) {
		status, err = s.finalizePeriodBaseLocked(ctx, base, now)
		if err != nil {
			return DatasetPeriodResult{}, err
		}
		return s.periodCommitReceipt(base, status, exp.DeadlineAt, delta, exp.ExpectedCount)
	}
	failures, err := s.readPeriodFailureBitmap(base, exp.ExpectedCount)
	if err != nil {
		return DatasetPeriodResult{}, err
	}
	for _, item := range items {
		if item.SeriesIndex < exp.ExpectedCount && bitmapBitSet(delta, item.SeriesIndex) {
			clearBitmapBit(failures, item.SeriesIndex)
		}
	}
	success, err := s.readPeriodBitmap(base)
	if err != nil {
		return DatasetPeriodResult{}, err
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
	if err != nil {
		return DatasetPeriodResult{}, err
	}
	status, err = s.finalizePeriodBaseLocked(ctx, base, now)
	if err != nil {
		return DatasetPeriodResult{}, err
	}
	return s.periodCommitReceipt(base, status, exp.DeadlineAt, delta, exp.ExpectedCount)
}

func (s *Store) periodCommitReceipt(base, status string, deadline int64, targets []byte, expected uint32) (DatasetPeriodResult, error) {
	success, err := s.readPeriodBitmap(base)
	if err != nil {
		return DatasetPeriodResult{}, err
	}
	result := DatasetPeriodResult{Status: status, DeadlineAt: deadline}
	for index := uint32(0); index < expected; index++ {
		if bitmapBitSet(targets, index) && bitmapBitSet(success, index) {
			result.AcceptedSeriesIndexes = append(result.AcceptedSeriesIndexes, index)
		}
	}
	return result, nil
}

// RecordDatasetPeriodFailures records exhausted series before the canonical
// cutoff; at the cutoff it finalizes existing bitmaps before classifying the
// request. Failure reports never advance the success bitmap.
func (s *Store) RecordDatasetPeriodFailures(ctx context.Context, exp DatasetPeriodExpectation, seriesIndexes []uint32) (DatasetPeriodResult, error) {
	exp, err := normalizePeriodExpectation(exp)
	if err != nil {
		return DatasetPeriodResult{}, err
	}
	if len(seriesIndexes) == 0 {
		return DatasetPeriodResult{}, invalid("failed series_indexes are required")
	}
	if err := ctx.Err(); err != nil {
		return DatasetPeriodResult{}, err
	}
	base := periodBase(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime)
	s.datasetWriteMu.RLock()
	defer s.datasetWriteMu.RUnlock()
	s.periodMu.Lock()
	defer s.periodMu.Unlock()
	current, err := s.readPeriodExpectation(base)
	if err != nil {
		return DatasetPeriodResult{}, err
	}
	if !samePeriodCommitIdentity(current, exp) {
		return DatasetPeriodResult{}, PeriodConflictError{SpaceID: exp.SpaceID, DatasetID: exp.DatasetID, Frequency: exp.Frequency, PeriodTime: exp.PeriodTime}
	}
	indexes := make([]uint32, 0, len(seriesIndexes))
	seen := make(map[uint32]struct{}, len(seriesIndexes))
	for _, index := range seriesIndexes {
		if err := validatePeriodSeriesIndex(current, index); err != nil {
			return DatasetPeriodResult{}, err
		}
		if _, found := seen[index]; !found {
			seen[index] = struct{}{}
			indexes = append(indexes, index)
		}
	}
	sort.Slice(indexes, func(i, j int) bool { return indexes[i] < indexes[j] })
	status, found, err := s.readPeriodStatus(base)
	if err != nil {
		return DatasetPeriodResult{}, err
	}
	if !found {
		return DatasetPeriodResult{}, invalid("dataset period is not initialized")
	}
	now := s.periodNow().UTC()
	if periodDeadlineReached(current, now) {
		status, err = s.finalizePeriodBaseLocked(ctx, base, now)
		if err != nil {
			return DatasetPeriodResult{}, err
		}
	}
	success, err := s.readPeriodBitmap(base)
	if err != nil {
		return DatasetPeriodResult{}, err
	}
	failures, err := s.readPeriodFailureBitmap(base, current.ExpectedCount)
	if err != nil {
		return DatasetPeriodResult{}, err
	}
	result := DatasetPeriodResult{Status: status, DeadlineAt: current.DeadlineAt}
	changed := false
	for _, index := range indexes {
		disposition := pb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_MISSED_DEADLINE
		switch {
		case bitmapBitSet(success, index):
			disposition = pb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_ALREADY_SUCCEEDED
		case bitmapBitSet(failures, index):
			disposition = pb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_RECORDED
		case status == "waiting" && !periodDeadlineReached(current, now):
			setBitmapBit(failures, index)
			changed = true
			disposition = pb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_RECORDED
		}
		result.FailureResults = append(result.FailureResults, &pb.DatasetPeriodFailureResult{SeriesIndex: index, Disposition: disposition})
	}
	if changed {
		batch := s.db.NewBatch()
		defer batch.Close()
		if err := batch.Set(periodFieldKey(base, "failures"), failures, s.writeOptions); err != nil {
			return DatasetPeriodResult{}, err
		}
		if err := batch.Commit(s.writeOptions); err != nil {
			return DatasetPeriodResult{}, err
		}
	}
	return result, nil
}

func periodDeadlineReached(exp DatasetPeriodExpectation, now time.Time) bool {
	return exp.DeadlineAt > 0 && now.Unix() >= exp.DeadlineAt
}

func (s *Store) FinalizeDatasetPeriod(ctx context.Context, exp DatasetPeriodExpectation, now time.Time) (string, error) {
	exp, err := normalizePeriodExpectation(exp)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.datasetWriteMu.RLock()
	defer s.datasetWriteMu.RUnlock()
	s.periodMu.Lock()
	defer s.periodMu.Unlock()
	base := periodBase(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime)
	current, err := s.readPeriodExpectation(base)
	if err != nil {
		return "", err
	}
	if !samePeriodCommitIdentity(current, exp) {
		return "", PeriodConflictError{SpaceID: exp.SpaceID, DatasetID: exp.DatasetID, Frequency: exp.Frequency, PeriodTime: exp.PeriodTime}
	}
	return s.finalizePeriodBaseLocked(ctx, base, now)
}

func (s *Store) finalizePeriodBase(ctx context.Context, base string, now time.Time) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.datasetWriteMu.RLock()
	defer s.datasetWriteMu.RUnlock()
	s.periodMu.Lock()
	defer s.periodMu.Unlock()
	return s.finalizePeriodBaseLocked(ctx, base, now)
}

// Caller holds datasetWriteMu and periodMu; the helper only acquires outboxMu.
func (s *Store) finalizePeriodBaseLocked(ctx context.Context, base string, now time.Time) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if now.IsZero() {
		now = s.periodNow().UTC()
	}
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
	if err := s.validatePeriodMarkerPayloadSize(raw); err != nil {
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
			case <-ticker.C:
				if _, err := s.FinalizeWaitingDatasetPeriods(s.historyCtx, s.periodNow().UTC(), periodFinalizeBatch); err != nil {
					s.logPeriodFinalizeError(err)
				} else {
					s.clearPeriodFinalizeError()
				}
			}
		}
	}()
}

func (s *Store) logPeriodFinalizeError(err error) {
	if err == nil {
		return
	}
	message := err.Error()
	s.periodFinalizeErrorMu.Lock()
	defer s.periodFinalizeErrorMu.Unlock()
	if message == s.periodFinalizeLastError {
		return
	}
	s.periodFinalizeLastError = message
	log.Printf("DataNode period finalizer failed: %v", err)
}

func (s *Store) clearPeriodFinalizeError() {
	s.periodFinalizeErrorMu.Lock()
	s.periodFinalizeLastError = ""
	s.periodFinalizeErrorMu.Unlock()
}

func (s *Store) FinalizeWaitingDatasetPeriods(ctx context.Context, now time.Time, limit int) (int, error) {
	if limit <= 0 || limit > periodFinalizeBatch {
		limit = periodFinalizeBatch
	}
	if now.IsZero() {
		now = s.periodNow().UTC()
	}
	s.periodFinalizeMu.Lock()
	defer s.periodFinalizeMu.Unlock()

	indexes := []struct {
		prefix  string
		dueOnly bool
	}{
		{prefix: periodCompletePrefix},
		{prefix: periodDeadlinePrefix, dueOnly: true},
		{prefix: periodWaitingPrefix},
	}
	start, err := s.readPeriodFinalizeOrder()
	if err != nil {
		return 0, err
	}
	bases := make([]string, 0, limit)
	seen := make(map[string]struct{}, limit)
	cursors := make(map[string][]byte, len(indexes))
	appendFromIndex := func(prefix string, dueDeadline bool, candidateLimit int) error {
		cursor, err := s.readPeriodFinalizeCursor(prefix)
		if err != nil {
			return err
		}
		iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: []byte(prefix), UpperBound: nextPrefix([]byte(prefix))})
		if err != nil {
			return err
		}
		defer iter.Close()
		valid := false
		wrapped := len(cursor) == 0
		if wrapped {
			valid = iter.First()
		} else {
			valid = iter.SeekGE(cursor)
			if valid && string(iter.Key()) == string(cursor) {
				valid = iter.Next()
			}
			if !valid {
				wrapped = true
				valid = iter.First()
			}
		}
		added := 0
		var lastKey []byte
		for valid {
			key := append([]byte(nil), iter.Key()...)
			if wrapped && len(cursor) > 0 && string(key) > string(cursor) {
				break
			}
			if dueDeadline {
				suffix := strings.TrimPrefix(string(key), periodDeadlinePrefix)
				deadlinePart, _, ok := strings.Cut(suffix, "/")
				deadline, parseErr := strconv.ParseInt(deadlinePart, 10, 64)
				if !ok || parseErr != nil {
					return errors.New("dataset period deadline index is corrupted")
				}
				if deadline > now.UTC().Unix() {
					if !wrapped && len(cursor) > 0 {
						wrapped = true
						valid = iter.First()
						continue
					}
					break
				}
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			lastKey = key
			base := string(append([]byte(nil), iter.Value()...))
			if _, exists := seen[base]; !exists {
				seen[base] = struct{}{}
				bases = append(bases, base)
				added++
			}
			if added >= candidateLimit {
				break
			}
			valid = iter.Next()
			if !valid && iter.Error() == nil && !wrapped && len(cursor) > 0 {
				wrapped = true
				valid = iter.First()
			}
		}
		if err := iter.Error(); err != nil {
			return err
		}
		if lastKey != nil {
			cursors[prefix] = lastKey
		}
		return nil
	}
	// Rotate the starting index and split each bounded scan's budget between
	// completion recovery, due deadlines, and the waiting recovery index. A
	// permanent error in one class must not starve another class or later keys.
	remaining := limit
	for offset := 0; offset < len(indexes) && remaining > 0; offset++ {
		index := indexes[(start+offset)%len(indexes)]
		classesLeft := len(indexes) - offset
		quota := remaining / classesLeft
		if remaining%classesLeft != 0 {
			quota++
		}
		if err := appendFromIndex(index.prefix, index.dueOnly, quota); err != nil {
			return 0, err
		}
		remaining -= quota
	}
	cursorBatch := s.db.NewBatch()
	defer cursorBatch.Close()
	for prefix, cursor := range cursors {
		if err := cursorBatch.Set([]byte(periodFinalizeCursor+prefix), cursor, s.writeOptions); err != nil {
			return 0, err
		}
	}
	if err := cursorBatch.Set([]byte(periodFinalizeOrder), []byte{byte((start + 1) % len(indexes))}, s.writeOptions); err != nil {
		return 0, err
	}
	if err := cursorBatch.Commit(s.writeOptions); err != nil {
		return 0, err
	}
	finalized := 0
	var finalizeErrors []error
	for _, base := range bases {
		status, err := s.finalizePeriodBase(ctx, base, now)
		if err != nil {
			finalizeErrors = append(finalizeErrors, fmt.Errorf("finalize dataset period %q: %w", base, err))
			continue
		}
		if status == "complete" || status == "degraded" {
			finalized++
		}
	}
	return finalized, errors.Join(finalizeErrors...)
}

func (s *Store) readPeriodFinalizeCursor(prefix string) ([]byte, error) {
	value, closer, err := s.db.Get([]byte(periodFinalizeCursor + prefix))
	if errors.Is(err, cpebble.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	return append([]byte(nil), value...), nil
}

func (s *Store) readPeriodFinalizeOrder() (int, error) {
	value, closer, err := s.db.Get([]byte(periodFinalizeOrder))
	if errors.Is(err, cpebble.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer closer.Close()
	if len(value) != 1 || value[0] > 2 {
		return 0, errors.New("dataset period finalizer order is corrupted")
	}
	return int(value[0]), nil
}

func (s *Store) GetDatasetPeriodProgress(ctx context.Context, exp DatasetPeriodExpectation) (*DatasetPeriodProgress, error) {
	exp, err := normalizePeriodExpectation(exp)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.datasetWriteMu.RLock()
	defer s.datasetWriteMu.RUnlock()
	s.periodMu.Lock()
	defer s.periodMu.Unlock()
	base := periodBase(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime)
	current, err := s.readPeriodExpectation(base)
	if err != nil {
		return nil, err
	}
	if !samePeriodCommitIdentity(current, exp) {
		return nil, PeriodConflictError{SpaceID: exp.SpaceID, DatasetID: exp.DatasetID, Frequency: exp.Frequency, PeriodTime: exp.PeriodTime}
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

func (s *Store) GetDatasetPeriodStatus(ctx context.Context, exp DatasetPeriodExpectation) (*DatasetPeriodProgress, error) {
	exp, err := normalizePeriodExpectation(exp)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.datasetWriteMu.RLock()
	defer s.datasetWriteMu.RUnlock()
	s.periodMu.Lock()
	defer s.periodMu.Unlock()
	base := periodBase(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime)
	current, err := s.readPeriodExpectation(base)
	if err != nil {
		return nil, err
	}
	if !samePeriodCommitIdentity(current, exp) {
		return nil, PeriodConflictError{SpaceID: exp.SpaceID, DatasetID: exp.DatasetID, Frequency: exp.Frequency, PeriodTime: exp.PeriodTime}
	}
	status, found, err := s.readPeriodStatus(base)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, cpebble.ErrNotFound
	}
	return &DatasetPeriodProgress{DatasetPeriodExpectation: current, Status: status}, nil
}

func normalizePeriodExpectation(exp DatasetPeriodExpectation) (DatasetPeriodExpectation, error) {
	exp.SpaceID = strings.TrimSpace(exp.SpaceID)
	exp.DatasetID = strings.TrimSpace(exp.DatasetID)
	exp.Frequency = strings.TrimSpace(exp.Frequency)
	exp.SeriesHash = strings.TrimSpace(exp.SeriesHash)
	if exp.SpaceID == "" || exp.DatasetID == "" || exp.Frequency == "" || exp.PeriodTime <= 0 || exp.SeriesHash == "" || exp.ExpectedCount == 0 {
		return exp, invalid("space_id, dataset_id, frequency, period_time, series_hash and expected_count are required")
	}
	return exp, nil
}

func normalizePeriodExpectationWithSeriesSnapshot(exp DatasetPeriodExpectation) (DatasetPeriodExpectation, error) {
	exp, err := normalizePeriodExpectation(exp)
	if err != nil {
		return exp, err
	}
	if len(exp.SeriesSnapshot) != int(exp.ExpectedCount) {
		return exp, invalid("period series snapshot size must equal expected_count")
	}
	exp.SeriesSnapshot = append([]DatasetPeriodSeries(nil), exp.SeriesSnapshot...)
	seen := make(map[[2]string]struct{}, len(exp.SeriesSnapshot))
	for index := range exp.SeriesSnapshot {
		exp.SeriesSnapshot[index].SubjectID = strings.TrimSpace(exp.SeriesSnapshot[index].SubjectID)
		if err := rowidentity.ValidateSeriesTag(exp.SeriesSnapshot[index].SeriesTag); err != nil {
			return exp, invalidf("period series snapshot series_tag is invalid: %v", err)
		}
		if exp.SeriesSnapshot[index].SeriesIndex != uint32(index) {
			return exp, invalid("period series snapshot series_index must be dense from zero")
		}
		if exp.SeriesSnapshot[index].SubjectID == "" {
			return exp, invalid("period series snapshot subject_id is required")
		}
		identity := [2]string{exp.SeriesSnapshot[index].SubjectID, exp.SeriesSnapshot[index].SeriesTag}
		if _, found := seen[identity]; found {
			return exp, invalid("period series snapshot subject_id and series_tag must be unique")
		}
		seen[identity] = struct{}{}
	}
	return exp, nil
}

func samePeriodSeriesSnapshot(left, right []DatasetPeriodSeries) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].SeriesIndex != right[i].SeriesIndex || left[i].SubjectID != right[i].SubjectID || left[i].SeriesTag != right[i].SeriesTag {
			return false
		}
	}
	return true
}

func validatePeriodRow(exp DatasetPeriodExpectation, seriesIndex uint32, row *pb.RowFieldUpsert) (bool, error) {
	if err := validatePeriodSeriesIndex(exp, seriesIndex); err != nil {
		return false, err
	}
	if row == nil || row.GetKey() == nil || row.GetKey().GetTimeSeries() == nil {
		return false, invalid("period batch rows must be time-series rows")
	}
	key := row.GetKey()
	series := key.GetTimeSeries()
	if key.GetSpaceId() != exp.SpaceID || key.GetDatasetId() != exp.DatasetID || series.GetFreq() != exp.Frequency {
		return false, invalid("period batch row identity does not match expectation")
	}
	if series.GetSubjectId() != exp.SeriesSnapshot[seriesIndex].SubjectID || series.GetSeriesTag() != exp.SeriesSnapshot[seriesIndex].SeriesTag {
		return false, invalidf("series_index %d subject_id or series_tag does not match the period series snapshot", seriesIndex)
	}
	at, err := time.Parse(time.RFC3339Nano, series.GetDataTime())
	if err != nil {
		return false, invalidf("period batch data_time is invalid: %v", err)
	}
	target := time.Unix(exp.PeriodTime, 0).UTC()
	if at.Unix() == exp.PeriodTime && !at.Equal(target) {
		return false, invalid("period batch target data_time must match the exact period timestamp")
	}
	return at.Equal(target), nil
}

func validatePeriodSeriesIndex(exp DatasetPeriodExpectation, index uint32) error {
	if len(exp.SeriesSnapshot) != int(exp.ExpectedCount) || index >= exp.ExpectedCount || exp.SeriesSnapshot[index].SeriesIndex != index || exp.SeriesSnapshot[index].SubjectID == "" {
		return invalidf("series_index %d is not in the period series snapshot", index)
	}
	return nil
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
	seriesSnapshotRaw, seriesSnapshotErr := s.readPeriodValue(periodFieldKey(base, "series_snapshot"))
	if seriesSnapshotErr == nil {
		if err := json.Unmarshal(seriesSnapshotRaw, &exp.SeriesSnapshot); err != nil {
			return DatasetPeriodExpectation{}, fmt.Errorf("dataset period series snapshot is corrupted: %w", err)
		}
	} else if !errors.Is(seriesSnapshotErr, cpebble.ErrNotFound) {
		return DatasetPeriodExpectation{}, seriesSnapshotErr
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
	return periodProgressPrefix + hex.EncodeToString([]byte(spaceID)) + "/" + hex.EncodeToString([]byte(datasetID)) + "/" + hex.EncodeToString([]byte(frequency)) + "/" + fmt.Sprintf("%020d", periodTime) + "/"
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
	universe := make([]string, 0, len(exp.SeriesSnapshot))
	failed := make([]string, 0)
	universeSeen := make(map[string]struct{}, len(exp.SeriesSnapshot))
	failedSeen := make(map[string]struct{})
	for index, series := range exp.SeriesSnapshot {
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
