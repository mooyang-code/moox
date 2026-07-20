package pebble

import (
	"context"
	"errors"
	"time"

	cpebble "github.com/cockroachdb/pebble"
)

// CleanupExpiredBuckets deletes only time-series field/attribute keys older
// than beforeBucket. Record versions are intentionally never removed here.
func (s *Store) CleanupExpiredBuckets(ctx context.Context, datasetID string, beforeBucket time.Time) (uint64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("pebble store is closed")
	}
	if beforeBucket.IsZero() {
		return 0, errors.New("before bucket is required")
	}
	iter, err := s.db.NewIter(&cpebble.IterOptions{})
	if err != nil {
		return 0, err
	}
	defer iter.Close()
	var keys [][]byte
	for iter.First(); iter.Valid(); iter.Next() {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		key := iter.Key()
		if len(key) < 2 || (key[0] != fieldNamespace && key[0] != attributeNamespace) || key[1] != timeSeriesKind {
			continue
		}
		parts, ok := decodePhysicalParts(key[2:])
		if !ok || len(parts) != 7 {
			continue
		}
		if datasetID != "" && parts[1] != datasetID {
			continue
		}
		bucket, err := time.Parse(time.RFC3339Nano, parts[4])
		if err != nil || !bucket.Before(beforeBucket) {
			continue
		}
		keys = append(keys, append([]byte(nil), key...))
	}
	if len(keys) == 0 {
		return 0, nil
	}
	batch := s.db.NewBatch()
	defer batch.Close()
	for _, key := range keys {
		if err := batch.Delete(key, s.writeOptions); err != nil {
			return 0, err
		}
	}
	if err := batch.Commit(s.writeOptions); err != nil {
		return 0, err
	}
	return uint64(len(keys)), nil
}

func decodePhysicalParts(data []byte) ([]string, bool) {
	parts := make([]string, 0, 7)
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
