package pebble

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"time"

	cpebble "github.com/cockroachdb/pebble"
)

// CleanupExpiredBuckets deletes only time-series field/attribute keys older
// than beforeBucket. Record versions are intentionally never removed here.
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
	// History materialization and TTL deletion must not interleave. Otherwise
	// an old field snapshot can republish markers after cleanup and the
	// completion marker would permanently bless stale history.
	s.historyBackfillMu.Lock()
	defer s.historyBackfillMu.Unlock()
	s.datasetWriteMu.Lock()
	defer s.datasetWriteMu.Unlock()
	before := beforeBucket.UTC().Format(canonicalTimeLayout)
	batch := s.db.NewBatch()
	defer batch.Close()
	ranges := make([][2][]byte, 0, 4)
	buckets := make(map[string]struct{})
	for _, namespace := range []byte{fieldNamespace, attributeNamespace} {
		prefix := []byte{namespace, timeSeriesKind}
		prefix = appendRawPart(prefix, []byte(spaceID))
		prefix = appendRawPart(prefix, []byte(datasetID))
		upper := appendPart(append([]byte(nil), prefix...), before)
		iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: prefix, UpperBound: upper})
		if err != nil {
			return 0, err
		}
		for valid := iter.First(); valid; valid = iter.Next() {
			if err := ctx.Err(); err != nil {
				_ = iter.Close()
				return 0, err
			}
			parts, ok := decodePhysicalParts(iter.Key()[2:])
			if ok && len(parts) == 8 && parts[0] == spaceID && parts[1] == datasetID {
				buckets[parts[2]] = struct{}{}
			}
		}
		iterErr := iter.Error()
		_ = iter.Close()
		if iterErr != nil {
			return 0, iterErr
		}
		if !bytes.Equal(prefix, upper) {
			if err := batch.DeleteRange(prefix, upper, s.writeOptions); err != nil {
				return 0, err
			}
			ranges = append(ranges, [2][]byte{append([]byte(nil), prefix...), append([]byte(nil), upper...)})
		}
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
		tagPrefix := seriesHistoryPrefix(spaceID, datasetID, historySelector{
			subject: series.GetSubjectId(),
			freq:    series.GetFreq(),
			tag:     series.GetSeriesTag(),
			hasTag:  true,
		})
		nextTagPrefix := nextPrefix(tagPrefix)
		// Keys inside a tag prefix are ordered by canonical data_time. A
		// malformed legacy timestamp is left untouched rather than causing a
		// broad delete; the next cleanup can retry after repair.
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
	return uint64(len(buckets)), nil
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
	// Stop history backfill and all writes while the dataset ranges are
	// removed. Otherwise a concurrent write could repopulate the result after
	// the metadata object has been deleted.
	s.historyBackfillMu.Lock()
	defer s.historyBackfillMu.Unlock()
	s.datasetWriteMu.Lock()
	defer s.datasetWriteMu.Unlock()
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()

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
	// Source-event dedupe keys are dataset-scoped. Remove both the marker and
	// its time index so recreating the same task can process the same source
	// event again without a silent no-op.
	processedPrefix := processedDatasetEventPrefix(spaceID, datasetID)
	processedUpper := nextPrefix(processedPrefix)
	processedKeys := make(map[string]struct{})
	processedIter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: processedPrefix, UpperBound: processedUpper})
	if err != nil {
		return 0, err
	}
	for valid := processedIter.First(); valid; valid = processedIter.Next() {
		processedKeys[string(processedIter.Key())] = struct{}{}
	}
	if iterErr := processedIter.Error(); iterErr != nil {
		_ = processedIter.Close()
		return 0, iterErr
	}
	if err := processedIter.Close(); err != nil {
		return 0, err
	}
	if len(processedKeys) > 0 {
		if err := batch.DeleteRange(processedPrefix, processedUpper, s.writeOptions); err != nil {
			return 0, err
		}
		processedTimeIter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: []byte(processedEventTimePrefix), UpperBound: nextPrefix([]byte(processedEventTimePrefix))})
		if err != nil {
			return 0, err
		}
		for valid := processedTimeIter.First(); valid; valid = processedTimeIter.Next() {
			if _, ok := processedKeys[string(processedTimeIter.Value())]; ok {
				if err := batch.Delete(processedTimeIter.Key(), s.writeOptions); err != nil {
					_ = processedTimeIter.Close()
					return 0, err
				}
			}
		}
		if iterErr := processedTimeIter.Error(); iterErr != nil {
			_ = processedTimeIter.Close()
			return 0, iterErr
		}
		if err := processedTimeIter.Close(); err != nil {
			return 0, err
		}
	}
	if err := batch.Delete(historyMaterializedMarker(spaceID, datasetID), s.writeOptions); err != nil {
		return 0, err
	}
	if err := batch.Commit(s.writeOptions); err != nil {
		return 0, err
	}
	for _, bounds := range ranges {
		s.compactAsync(bounds[0], bounds[1])
	}
	return uint64(len(ranges) + 1), nil
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
