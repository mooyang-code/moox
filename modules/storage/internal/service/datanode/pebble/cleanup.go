package pebble

import (
	"bytes"
	"context"
	"errors"
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
	before := beforeBucket.UTC().Format(canonicalTimeLayout)
	batch := s.db.NewBatch()
	defer batch.Close()
	ranges := make([][2][]byte, 0, 2)
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
	if err := batch.Commit(s.writeOptions); err != nil {
		return 0, err
	}
	for _, bounds := range ranges {
		s.compactAsync(bounds[0], bounds[1])
	}
	return uint64(len(buckets)), nil
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
