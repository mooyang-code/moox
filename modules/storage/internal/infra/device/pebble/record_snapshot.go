package pebble

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	cpebble "github.com/cockroachdb/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/rs/xid"
	"google.golang.org/protobuf/proto"
)

const recordSnapshotTTL = 20 * time.Minute

type recordSnapshot struct {
	snapshot  *cpebble.Snapshot
	mode      pb.RecordReadMode
	watermark uint64
	startTime string
	endTime   string
	expiresAt time.Time
	mu        sync.Mutex
}

func (s *Store) OpenRecordSnapshot(ctx context.Context, mode pb.RecordReadMode, updatedTimeRange *pb.TimeRange) (string, uint64, error) {
	_ = ctx
	if mode == pb.RecordReadMode_RECORD_READ_MODE_UNSPECIFIED {
		mode = pb.RecordReadMode_RECORD_READ_MODE_CURRENT
	}
	if mode != pb.RecordReadMode_RECORD_READ_MODE_CURRENT && mode != pb.RecordReadMode_RECORD_READ_MODE_HISTORY {
		return "", 0, errors.New("invalid record snapshot mode")
	}
	s.commitMu.Lock()
	snapshot := s.db.NewSnapshot()
	watermark := uint64(0)
	if value, closer, err := snapshot.Get([]byte(recordCommitSeqKey)); err == nil {
		if len(value) != 8 {
			closer.Close()
			snapshot.Close()
			s.commitMu.Unlock()
			return "", 0, errors.New("invalid record commit sequence")
		}
		watermark = binary.BigEndian.Uint64(value)
		closer.Close()
	} else if !errors.Is(err, cpebble.ErrNotFound) {
		snapshot.Close()
		s.commitMu.Unlock()
		return "", 0, err
	}
	s.commitMu.Unlock()
	start, end, err := normalizeSnapshotRange(updatedTimeRange)
	if err != nil {
		snapshot.Close()
		return "", 0, err
	}
	id := xid.New().String()
	s.snapshotMu.Lock()
	s.snapshots[id] = &recordSnapshot{snapshot: snapshot, mode: mode, watermark: watermark, startTime: start, endTime: end, expiresAt: time.Now().Add(recordSnapshotTTL)}
	s.snapshotMu.Unlock()
	return id, watermark, nil
}

func normalizeSnapshotRange(raw *pb.TimeRange) (string, string, error) {
	if raw == nil {
		return "", "", nil
	}
	start, err := normalizePhysicalRecordTime(raw.GetStartTime())
	if err != nil {
		return "", "", fmt.Errorf("invalid snapshot start_time: %w", err)
	}
	end, err := normalizePhysicalRecordTime(raw.GetEndTime())
	if err != nil {
		return "", "", fmt.Errorf("invalid snapshot end_time: %w", err)
	}
	if start != "" && end != "" && start > end {
		return "", "", errors.New("snapshot time range start is after end")
	}
	return start, end, nil
}

func normalizePhysicalRecordTime(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return "", err
	}
	return parsed.UTC().Format(fixedRecordTimeLayout), nil
}

func (s *Store) getRecordSnapshot(id string) (*recordSnapshot, error) {
	s.snapshotMu.Lock()
	snapshot := s.snapshots[id]
	if snapshot != nil {
		snapshot.mu.Lock()
		if time.Now().After(snapshot.expiresAt) {
			snapshot.mu.Unlock()
			delete(s.snapshots, id)
			snapshot.snapshot.Close()
			snapshot = nil
		} else {
			snapshot.expiresAt = time.Now().Add(recordSnapshotTTL)
			snapshot.mu.Unlock()
		}
	}
	s.snapshotMu.Unlock()
	if snapshot == nil {
		return nil, errors.New("record snapshot not found or expired")
	}
	return snapshot, nil
}

func (s *Store) RenewRecordSnapshot(ctx context.Context, id string) error {
	_ = ctx
	snapshot, err := s.getRecordSnapshot(id)
	if err != nil {
		return err
	}
	snapshot.mu.Lock()
	snapshot.expiresAt = time.Now().Add(recordSnapshotTTL)
	snapshot.mu.Unlock()
	return nil
}

func (s *Store) CloseRecordSnapshot(ctx context.Context, id string) error {
	_ = ctx
	s.snapshotMu.Lock()
	snapshot := s.snapshots[id]
	if snapshot != nil {
		delete(s.snapshots, id)
	}
	s.snapshotMu.Unlock()
	if snapshot != nil {
		snapshot.snapshot.Close()
	}
	return nil
}

func (s *Store) ReadRecordSnapshot(ctx context.Context, snapshotID string, target *pb.PrimaryStoreTarget, recordIDs []string) ([]*pb.RecordRow, error) {
	_ = ctx
	snapshot, err := s.getRecordSnapshot(snapshotID)
	if err != nil {
		return nil, err
	}
	if snapshot.mode != pb.RecordReadMode_RECORD_READ_MODE_CURRENT {
		return nil, errors.New("point reads require CURRENT snapshot")
	}
	if target == nil || target.GetSpaceId() == "" || target.GetDatasetId() == "" {
		return nil, errors.New("snapshot target requires space_id and dataset_id")
	}
	rows := make([]*pb.RecordRow, 0, len(recordIDs))
	for _, recordID := range recordIDs {
		key := &pb.RecordKey{SpaceId: target.GetSpaceId(), DatasetId: target.GetDatasetId(), RecordId: recordID}
		value, closer, err := snapshot.snapshot.Get(encodeRecordCurrentKey(key))
		if errors.Is(err, cpebble.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		row := &pb.RecordRow{}
		unmarshalErr := proto.Unmarshal(value, row)
		closer.Close()
		if unmarshalErr != nil {
			return nil, unmarshalErr
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (s *Store) ScanRecordSnapshot(ctx context.Context, snapshotID string, target *pb.PrimaryStoreTarget, page *pb.Page) ([]*pb.RecordRow, *pb.PageResult, error) {
	_ = ctx
	snapshot, err := s.getRecordSnapshot(snapshotID)
	if err != nil {
		return nil, nil, err
	}
	if target == nil || target.GetSpaceId() == "" || target.GetDatasetId() == "" {
		return nil, nil, errors.New("snapshot target requires space_id and dataset_id")
	}
	lower, upper := recordSnapshotBounds(snapshot, target)
	originalLower, originalUpper := lower, upper
	if page == nil {
		page = &pb.Page{}
	}
	if cursor := page.GetCursor(); cursor != "" {
		last, err := s.decodeRecordSnapshotCursor(cursor, snapshotID, target.GetDatasetId(), originalLower, originalUpper, snapshot.mode)
		if err != nil {
			return nil, nil, err
		}
		if next := nextPrefix(last); len(next) > 0 {
			lower = next
		}
	}
	iter, err := snapshot.snapshot.NewIter(&cpebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return nil, nil, err
	}
	defer iter.Close()
	size := pageSize(page)
	rows := make([]*pb.RecordRow, 0, size)
	lastKey := ""
	for valid := iter.First(); valid; valid = iter.Next() {
		if snapshot.mode == pb.RecordReadMode_RECORD_READ_MODE_HISTORY {
			value, closer, err := snapshot.snapshot.Get(iter.Value())
			if err != nil {
				return nil, nil, err
			}
			row := &pb.RecordRow{}
			unmarshalErr := proto.Unmarshal(value, row)
			closer.Close()
			if unmarshalErr != nil {
				return nil, nil, unmarshalErr
			}
			rows = append(rows, row)
		} else {
			row := &pb.RecordRow{}
			if err := proto.Unmarshal(iter.Value(), row); err != nil {
				return nil, nil, err
			}
			rows = append(rows, row)
		}
		lastKey = string(iter.Key())
		if uint32(len(rows)) >= size {
			if iter.Next() {
				return rows, &pb.PageResult{Size: size, HasMore: true, NextCursor: s.encodeRecordSnapshotCursor(snapshotID, target.GetDatasetId(), lastKey, originalLower, originalUpper, snapshot.mode)}, nil
			}
			break
		}
	}
	return rows, &pb.PageResult{Size: size, HasMore: false}, nil
}

func recordSnapshotBounds(snapshot *recordSnapshot, target *pb.PrimaryStoreTarget) ([]byte, []byte) {
	if snapshot.mode == pb.RecordReadMode_RECORD_READ_MODE_HISTORY {
		prefix := []byte(recordTimePrefix + escape(target.GetSpaceId()) + "|" + escape(target.GetDatasetId()) + "|")
		lower, upper := prefix, nextPrefix(prefix)
		if snapshot.startTime != "" {
			lower = []byte(string(prefix) + escape(snapshot.startTime) + "|")
		}
		if snapshot.endTime != "" {
			upper = nextPrefix([]byte(string(prefix) + escape(snapshot.endTime) + "|"))
		}
		return lower, upper
	}
	prefix := []byte(recordCurrentPrefix + escape(target.GetSpaceId()) + "|" + escape(target.GetDatasetId()) + "|")
	return prefix, nextPrefix(prefix)
}

func recordTimeStampFromKey(key []byte) string {
	parts := strings.Split(string(key), "|")
	if len(parts) < 5 {
		return ""
	}
	return unescape(parts[3])
}

type recordSnapshotCursor struct {
	Version   uint32            `json:"v"`
	SourceID  string            `json:"source_id"`
	Snapshot  string            `json:"snapshot_id"`
	DatasetID string            `json:"dataset_id"`
	Mode      pb.RecordReadMode `json:"mode"`
	Lower     string            `json:"lower"`
	Upper     string            `json:"upper"`
	LastKey   string            `json:"last_key"`
}

func (s *Store) encodeRecordSnapshotCursor(snapshotID, datasetID, lastKey string, lower, upper []byte, mode pb.RecordReadMode) string {
	cursor := recordSnapshotCursor{
		Version: 1, SourceID: s.recordSource, Snapshot: snapshotID, DatasetID: datasetID,
		Mode: mode, Lower: base64.RawURLEncoding.EncodeToString(lower), Upper: base64.RawURLEncoding.EncodeToString(upper),
		LastKey: base64.RawURLEncoding.EncodeToString([]byte(lastKey)),
	}
	payload, _ := json.Marshal(cursor)
	mac := hmac.New(sha256.New, s.cursorKey)
	_, _ = mac.Write(payload)
	signed := append(payload, mac.Sum(nil)...)
	return base64.RawURLEncoding.EncodeToString(signed)
}

func (s *Store) decodeRecordSnapshotCursor(cursor, snapshotID, datasetID string, lower, upper []byte, mode pb.RecordReadMode) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, errors.New("invalid record snapshot cursor")
	}
	if len(decoded) <= sha256.Size {
		return nil, errors.New("invalid record snapshot cursor")
	}
	payload, signature := decoded[:len(decoded)-sha256.Size], decoded[len(decoded)-sha256.Size:]
	mac := hmac.New(sha256.New, s.cursorKey)
	_, _ = mac.Write(payload)
	if !hmac.Equal(mac.Sum(nil), signature) {
		return nil, errors.New("record snapshot cursor signature mismatch")
	}
	var value recordSnapshotCursor
	if err := json.Unmarshal(payload, &value); err != nil {
		return nil, errors.New("invalid record snapshot cursor")
	}
	if value.Version != 1 || value.SourceID != s.recordSource || value.Snapshot != snapshotID || value.DatasetID != datasetID || value.Mode != mode {
		return nil, errors.New("record snapshot cursor binding mismatch")
	}
	encodedLower := base64.RawURLEncoding.EncodeToString(lower)
	encodedUpper := base64.RawURLEncoding.EncodeToString(upper)
	if value.Lower != encodedLower || value.Upper != encodedUpper {
		return nil, errors.New("record snapshot cursor bounds mismatch")
	}
	last, err := base64.RawURLEncoding.DecodeString(value.LastKey)
	if err != nil {
		return nil, errors.New("invalid record snapshot cursor last key")
	}
	return last, nil
}
