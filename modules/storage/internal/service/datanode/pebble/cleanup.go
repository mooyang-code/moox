package pebble

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"time"

	cpebble "github.com/cockroachdb/pebble"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"google.golang.org/protobuf/proto"
)

// CleanupExpiredBuckets 删除一个数据集早于 beforeBucket 的时序行、历史行索引、周期账本与完成标记，返回删除的键范围数。
// 记录型行的版本不在这里删除；commit 标记按自己的 7 天由 CleanupExpiredCommits 删除。
func (s *Store) CleanupExpiredBuckets(ctx context.Context, spaceID, datasetID string, beforeBucket time.Time) (uint64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("pebble store is closed")
	}
	if spaceID == "" || datasetID == "" {
		return 0, invalid("space_id and dataset_id are required")
	}
	if beforeBucket.IsZero() {
		return 0, invalid("before bucket is required")
	}
	s.datasetWriteMu.Lock()
	defer s.datasetWriteMu.Unlock()
	before := beforeBucket.UTC().Format(canonicalTimeLayout)
	batch := s.db.NewBatch()
	defer batch.Close()
	ranges := make([][2][]byte, 0, 4)
	// Field and attribute keys start with the day bucket, so one range per
	// namespace removes every expired bucket without reading a key.
	for _, namespace := range []byte{fieldNamespace, attributeNamespace} {
		prefix := []byte{namespace, timeSeriesKind}
		prefix = appendRawPart(prefix, []byte(spaceID))
		prefix = appendRawPart(prefix, []byte(datasetID))
		upper := appendPart(append([]byte(nil), prefix...), before)
		if err := batch.DeleteRange(prefix, upper, s.writeOptions); err != nil {
			return 0, err
		}
		ranges = append(ranges, [2][]byte{prefix, upper})
	}
	// History markers are ordered by logical data time, so they can be
	// removed with one bounded range instead of scanning every field key.
	historyPrefix := []byte{historyNamespace, timeSeriesKind}
	historyPrefix = appendRawPart(historyPrefix, []byte(spaceID))
	historyPrefix = appendRawPart(historyPrefix, []byte(datasetID))
	historyUpper := appendPart(append([]byte(nil), historyPrefix...), before)
	if !bytes.Equal(historyPrefix, historyUpper) {
		if err := batch.DeleteRange(historyPrefix, historyUpper, s.writeOptions); err != nil {
			return 0, err
		}
		ranges = append(ranges, [2][]byte{historyPrefix, historyUpper})
	}
	// The subject-first index has tag before data_time, so one dataset-wide
	// DeleteRange cannot express the TTL boundary. Walk one key per
	// (subject,freq,tag) group and seek directly to the next tag prefix. The
	// first key in a group is its oldest row, so this avoids rescanning every
	// marker on every hourly cleanup tick.
	seriesDatasetPrefix := []byte{seriesHistoryNamespace, timeSeriesKind}
	seriesDatasetPrefix = appendRawPart(seriesDatasetPrefix, []byte(spaceID))
	seriesDatasetPrefix = appendRawPart(seriesDatasetPrefix, []byte(datasetID))
	expiredSeries := 0
	iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: seriesDatasetPrefix, UpperBound: nextPrefix(seriesDatasetPrefix)})
	if err != nil {
		return 0, err
	}
	for valid := iter.First(); valid; {
		if err := ctx.Err(); err != nil {
			_ = iter.Close()
			return 0, err
		}
		key, ok := parseSeriesHistoryKey(iter.Key())
		if !ok || key.GetTimeSeries() == nil {
			valid = iter.Next()
			continue
		}
		series := key.GetTimeSeries()
		tagPrefix := seriesHistoryPrefix(spaceID, datasetID, seriesSelector{
			subject: series.GetSubjectId(),
			freq:    series.GetFreq(),
			tag:     series.GetSeriesTag(),
			hasTag:  true,
		})
		nextTagPrefix := nextPrefix(tagPrefix)
		// Keys inside a tag prefix are ordered by canonical data_time. A
		// malformed timestamp is left untouched rather than causing a broad
		// delete; the next cleanup can retry after repair.
		if _, parseErr := time.Parse(canonicalTimeLayout, series.GetDataTime()); parseErr == nil && series.GetDataTime() < before {
			upper := appendPart(append([]byte(nil), tagPrefix...), before)
			if err := batch.DeleteRange(tagPrefix, upper, s.writeOptions); err != nil {
				_ = iter.Close()
				return 0, err
			}
			ranges = append(ranges, [2][]byte{tagPrefix, upper})
			expiredSeries++
		}
		if nextTagPrefix == nil {
			break
		}
		valid = iter.SeekGE(nextTagPrefix)
	}
	iterErr := iter.Error()
	_ = iter.Close()
	if iterErr != nil {
		return 0, iterErr
	}
	// 周期账本与完成标记用同一个截止时间：周期早于截止时间的一并删除。
	for _, prefix := range []string{periodDatasetPrefix(spaceID, datasetID), markerDatasetPrefix(spaceID, datasetID)} {
		periodRanges, err := s.stageDeleteBeforePeriod(batch, prefix, beforeBucket.UTC().Unix())
		if err != nil {
			return 0, err
		}
		ranges = append(ranges, periodRanges...)
	}
	if err := batch.Commit(s.writeOptions); err != nil {
		return 0, err
	}
	for _, bounds := range ranges {
		// The subject-first index can have hundreds of tag ranges. Compact it
		// once per dataset instead of starting one goroutine per series.
		if len(bounds[0]) >= 2 && bounds[0][0] == seriesHistoryNamespace {
			continue
		}
		s.compactAsync(bounds[0], bounds[1])
	}
	if expiredSeries > 0 {
		seriesCompactPrefix := []byte{seriesHistoryNamespace, timeSeriesKind}
		seriesCompactPrefix = appendRawPart(seriesCompactPrefix, []byte(spaceID))
		seriesCompactPrefix = appendRawPart(seriesCompactPrefix, []byte(datasetID))
		s.compactAsync(seriesCompactPrefix, nextPrefix(seriesCompactPrefix))
	}
	return uint64(len(ranges)), nil
}

// DeleteDatasetRows physically removes every row and materialized history
// index belonging to one Dataset. Metadata deletion alone is insufficient:
// Pebble keys are intentionally independent from the metadata SQLite store.
func (s *Store) DeleteDatasetRows(ctx context.Context, spaceID, datasetID string) (uint64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("pebble store is closed")
	}
	if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(datasetID) == "" {
		return 0, invalid("space_id and dataset_id are required")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	unlockPublication, err := s.lockOutboxDatasetPublication(ctx, spaceID, datasetID)
	if err != nil {
		return 0, err
	}
	defer unlockPublication()
	// Stop all writes while the dataset ranges are
	// removed. Otherwise a concurrent write could repopulate the result after
	// the metadata object has been deleted.
	s.datasetWriteMu.Lock()
	defer s.datasetWriteMu.Unlock()
	s.periodMu.Lock()
	defer s.periodMu.Unlock()
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()
	// 取得全部锁后再确认请求仍然有效：等待期间到期的删除不能继续物理清理
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	batch := s.db.NewBatch()
	defer batch.Close()
	ranges := make([][2][]byte, 0, 6)
	for _, namespace := range []byte{fieldNamespace, attributeNamespace} {
		for _, rowKind := range []byte{timeSeriesKind, recordKind} {
			prefix := []byte{namespace, rowKind}
			prefix = appendRawPart(prefix, []byte(spaceID))
			prefix = appendRawPart(prefix, []byte(datasetID))
			upper := nextPrefix(prefix)
			if err := batch.DeleteRange(prefix, upper, s.writeOptions); err != nil {
				return 0, err
			}
			ranges = append(ranges, [2][]byte{prefix, upper})
		}
	}
	for _, namespace := range []byte{historyNamespace, seriesHistoryNamespace} {
		prefix := []byte{namespace, timeSeriesKind}
		prefix = appendRawPart(prefix, []byte(spaceID))
		prefix = appendRawPart(prefix, []byte(datasetID))
		upper := nextPrefix(prefix)
		if err := batch.DeleteRange(prefix, upper, s.writeOptions); err != nil {
			return 0, err
		}
		ranges = append(ranges, [2][]byte{prefix, upper})
	}
	// 周期账本、完成标记、序列快照与生效表按数据集前缀整段删除，重建同名数据集后从头登记快照；
	// commit 标记在各日桶下删除，重建后同一 commit_id 可以重新写入。
	for _, prefix := range []string{
		periodDatasetPrefix(spaceID, datasetID), markerDatasetPrefix(spaceID, datasetID),
		seriesSnapshotPrefix + datasetScope(spaceID, datasetID), seriesEffectivePrefix + datasetScope(spaceID, datasetID),
	} {
		lower, upper := []byte(prefix), nextPrefix([]byte(prefix))
		if err := batch.DeleteRange(lower, upper, s.writeOptions); err != nil {
			return 0, err
		}
		ranges = append(ranges, [2][]byte{lower, upper})
	}
	commitRanges, err := s.stageDeleteDatasetCommits(batch, spaceID, datasetID)
	if err != nil {
		return 0, err
	}
	ranges = append(ranges, commitRanges...)
	deletedOutbox, err := s.stageDatasetOutboxCleanup(batch, spaceID, datasetID)
	if err != nil {
		return 0, err
	}
	// 提交前最后一次检查：批次尚未落盘时取消仍可完整放弃
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := batch.Commit(s.writeOptions); err != nil {
		return 0, err
	}
	for _, bounds := range ranges {
		s.compactAsync(bounds[0], bounds[1])
	}
	if deletedOutbox > 0 {
		s.noteOutboxDeleted(deletedOutbox)
	}
	return uint64(len(ranges)), nil
}

// stageDatasetOutboxCleanup 删掉这个数据集还没发出的 Outbox 条目，返回删除的条数。
func (s *Store) stageDatasetOutboxCleanup(batch *cpebble.Batch, spaceID, datasetID string) (int, error) {
	iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: []byte(outboxPrefix), UpperBound: nextPrefix([]byte(outboxPrefix))})
	if err != nil {
		return 0, err
	}
	defer iter.Close()
	deleted := 0
	for valid := iter.First(); valid; valid = iter.Next() {
		message := &eventpb.EventMessage{}
		if proto.Unmarshal(iter.Value(), message) != nil || message.GetSpaceId() != spaceID || message.GetSubjectId() != datasetID {
			continue
		}
		if err := batch.Delete(append([]byte(nil), iter.Key()...), s.writeOptions); err != nil {
			return 0, err
		}
		deleted++
	}
	return deleted, iter.Error()
}

// requireDataset 在持有数据集读锁时确认数据集仍在元数据里。删除先删元数据、再在写锁里删行，
// 所以拿到读锁之后这里还查得到的写入一定早于删行，写入的行会被一并删掉。
func (s *Store) requireDataset(spaceID, datasetID string) error {
	if s.datasetExists != nil && !s.datasetExists(spaceID, datasetID) {
		return ErrDatasetDeleted
	}
	return nil
}

func decodePhysicalParts(data []byte) ([]string, bool) {
	parts := make([]string, 0, 8)
	for len(data) > 0 {
		part, rest, err := decodePart(data)
		if err != nil {
			return nil, false
		}
		parts = append(parts, part)
		data = rest
	}
	return parts, len(parts) > 0
}
