package pebble

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
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

type DatasetPeriodExpectation struct {
	SpaceID       string
	DatasetID     string
	Frequency     string
	PeriodTime    int64
	SeriesHash    string
	ExpectedCount uint32
	DeadlineAt    int64
}

type TimeSeriesBatchItem struct {
	SeriesIndex uint32
	Row         *pb.RowFieldUpsert
}

type DatasetPeriodProgress struct {
	DatasetPeriodExpectation
	Status string
	Bitmap []byte
}

func (s *Store) EnsureDatasetPeriod(ctx context.Context, exp DatasetPeriodExpectation) (string, error) {
	exp, err := normalizePeriodExpectation(exp)
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
		if !samePeriodExpectation(current, exp) {
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
	if err := batch.Set(periodFieldKey(base, "deadline"), int64Bytes(exp.DeadlineAt), s.writeOptions); err != nil {
		return "", err
	}
	if err := batch.Set(periodFieldKey(base, "status"), []byte("waiting"), s.writeOptions); err != nil {
		return "", err
	}
	if err := batch.Set(periodFieldKey(base, "bitmap"), make([]byte, bitmapSize(exp.ExpectedCount)), s.writeOptions); err != nil {
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
	base := periodBase(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime)
	current, err := s.readPeriodExpectation(base)
	if err != nil {
		return "", err
	}
	if !samePeriodCommitIdentity(current, exp) {
		return "", PeriodConflictError{SpaceID: exp.SpaceID, DatasetID: exp.DatasetID, Frequency: exp.Frequency, PeriodTime: exp.PeriodTime}
	}
	exp = current
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
	delta := make([]byte, bitmapSize(exp.ExpectedCount))
	rows := make([]*pb.RowFieldUpsert, 0, len(items))
	for _, item := range items {
		if item.SeriesIndex >= exp.ExpectedCount {
			return "", invalidf("series_index %d exceeds expected_count %d", item.SeriesIndex, exp.ExpectedCount)
		}
		isTarget, err := validatePeriodRow(exp, item.Row)
		if err != nil {
			return "", err
		}
		if isTarget {
			setBitmapBit(delta, item.SeriesIndex)
		}
		rows = append(rows, item.Row)
	}
	normalizedRows, err := s.normalizeWriteRows(ctx, rows)
	if err != nil {
		return "", err
	}
	writeEvent := func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		if strings.TrimSpace(sourceEventID) == "" {
			return BuildDatasetRowsUpsertedMessageWithSource(s.nodeID, writeSource, spaceID, datasetID, rows)
		}
		return BuildDatasetRowsUpsertedMessageForSourceWithWriteSource(s.nodeID, sourceEventID, writeSource, spaceID, datasetID, rows)
	}
	s.datasetWriteMu.RLock()
	defer s.datasetWriteMu.RUnlock()
	s.outboxMu.Lock()
	_, err = s.writeFieldsEventLocked(ctx, normalizedRows, strings.TrimSpace(sourceEventID), writeEvent, func(batch *cpebble.Batch, _ []*OutboxEntry) error {
		return batch.Merge(periodFieldKey(base, "bitmap"), delta, s.writeOptions)
	})
	s.outboxMu.Unlock()
	if err != nil {
		return "", err
	}
	return s.FinalizeDatasetPeriod(ctx, exp, time.Now().UTC())
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
	complete := bitmapCount(bitmap) == int(exp.ExpectedCount)
	terminal := ""
	if complete {
		terminal = "complete"
	} else if exp.DeadlineAt > 0 && now.Unix() >= exp.DeadlineAt {
		terminal = "degraded"
	} else {
		return "waiting", nil
	}
	marker := &pb.CollectorPeriodCompletedMarker{
		DatasetId: exp.DatasetID, Frequency: exp.Frequency, PeriodTime: exp.PeriodTime,
		Status: terminal, BatchId: periodCompletionID(base), ConfigSnapshotId: exp.SeriesHash,
		ExpectedScopeRef: exp.SeriesHash, CollectedAt: timestamppb.New(now.UTC()),
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
	iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: []byte(periodWaitingPrefix), UpperBound: nextPrefix([]byte(periodWaitingPrefix))})
	if err != nil {
		return 0, err
	}
	bases := make([]string, 0, limit)
	for valid := iter.First(); valid && len(bases) < limit; valid = iter.Next() {
		bases = append(bases, string(append([]byte(nil), iter.Value()...)))
	}
	iterErr := iter.Error()
	closeErr := iter.Close()
	if iterErr != nil {
		return 0, iterErr
	}
	if closeErr != nil {
		return 0, closeErr
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
	return &DatasetPeriodProgress{DatasetPeriodExpectation: current, Status: status, Bitmap: bitmap}, nil
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

func validatePeriodRow(exp DatasetPeriodExpectation, row *pb.RowFieldUpsert) (bool, error) {
	if row == nil || row.GetKey() == nil || row.GetKey().GetTimeSeries() == nil {
		return false, invalid("period batch rows must be time-series rows")
	}
	key := row.GetKey()
	series := key.GetTimeSeries()
	if strings.TrimSpace(key.GetSpaceId()) != exp.SpaceID || strings.TrimSpace(key.GetDatasetId()) != exp.DatasetID || !strings.EqualFold(strings.TrimSpace(series.GetFreq()), exp.Frequency) {
		return false, invalid("period batch row identity does not match expectation")
	}
	at, err := time.Parse(time.RFC3339Nano, series.GetDataTime())
	if err != nil {
		return false, invalidf("period batch data_time is invalid: %v", err)
	}
	return at.UTC().Unix() == exp.PeriodTime, nil
}

func samePeriodExpectation(left, right DatasetPeriodExpectation) bool {
	return left.SpaceID == right.SpaceID && left.DatasetID == right.DatasetID && left.Frequency == right.Frequency && left.PeriodTime == right.PeriodTime && left.SeriesHash == right.SeriesHash && left.ExpectedCount == right.ExpectedCount && left.DeadlineAt == right.DeadlineAt
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
	return DatasetPeriodExpectation{SpaceID: spaceID, DatasetID: datasetID, Frequency: frequency, PeriodTime: periodTime, SeriesHash: string(seriesHash), ExpectedCount: binary.BigEndian.Uint32(expectedRaw), DeadlineAt: int64(binary.BigEndian.Uint64(deadlineRaw))}, nil
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
func bitmapSize(count uint32) int { return int((count + 7) / 8) }
func setBitmapBit(bitmap []byte, index uint32) {
	bitmap[index/8] |= byte(1 << (index % 8))
}
func bitmapCount(bitmap []byte) int {
	count := 0
	for _, value := range bitmap {
		count += bits.OnesCount8(value)
	}
	return count
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
