package pebble

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	cpebble "github.com/cockroachdb/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/rs/xid"
	"google.golang.org/protobuf/proto"
)

const (
	recordHistoryPrefix   = "rh|"
	recordCurrentPrefix   = "rc|"
	recordTimePrefix      = "rt|"
	recordJournalPrefix   = "j|"
	recordRequestPrefix   = "ri|"
	recordSourceKey       = "__meta|record_source_id"
	recordCursorKey       = "__meta|record_cursor_hmac_key"
	recordCommitSeqKey    = "__meta|record_commit_seq"
	fixedRecordTimeLayout = "2006-01-02T15:04:05.000000000Z"
)

func loadRecordMetadata(db *cpebble.DB, writeOptions *cpebble.WriteOptions) (string, []byte, error) {
	sourceID, err := readMetadataString(db, recordSourceKey)
	if err != nil {
		return "", nil, err
	}
	cursorKey, err := readMetadataBytes(db, recordCursorKey)
	if err != nil {
		return "", nil, err
	}
	if sourceID != "" && len(cursorKey) > 0 {
		return sourceID, cursorKey, nil
	}
	if sourceID == "" {
		sourceID = xid.New().String()
	}
	if len(cursorKey) == 0 {
		cursorKey = make([]byte, 32)
		if _, err := rand.Read(cursorKey); err != nil {
			return "", nil, err
		}
	}
	batch := db.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(recordSourceKey), []byte(sourceID), writeOptions); err != nil {
		return "", nil, err
	}
	if err := batch.Set([]byte(recordCursorKey), cursorKey, writeOptions); err != nil {
		return "", nil, err
	}
	if err := batch.Commit(writeOptions); err != nil {
		return "", nil, err
	}
	return sourceID, cursorKey, nil
}

func readMetadataString(db *cpebble.DB, key string) (string, error) {
	value, closer, err := db.Get([]byte(key))
	if errors.Is(err, cpebble.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer closer.Close()
	return string(bytes.Clone(value)), nil
}

func readMetadataBytes(db *cpebble.DB, key string) ([]byte, error) {
	value, closer, err := db.Get([]byte(key))
	if errors.Is(err, cpebble.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	return bytes.Clone(value), nil
}

func (s *Store) ApplyRecordMutations(ctx context.Context, requestID string, mutations []*pb.RecordMutation) (*pb.RecordRowsCommittedEvent, error) {
	_ = ctx
	if strings.TrimSpace(requestID) == "" {
		return nil, errors.New("request_id is required")
	}
	if len(mutations) == 0 {
		return nil, errors.New("mutations are required")
	}
	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	for _, mutation := range mutations {
		if err := validateRecordMutation(mutation); err != nil {
			return nil, err
		}
	}
	keys := make([]string, 0, len(mutations))
	seen := make(map[string]struct{}, len(mutations))
	for _, mutation := range mutations {
		key := encodeRecordLogicalKey(mutation.GetKey())
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("duplicate record identity %q", key)
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	digest, err := digestRecordMutations(mutations)
	if err != nil {
		return nil, err
	}
	if event, exists, err := s.loadIdempotentRecordEvent(requestID, digest); err != nil {
		return nil, err
	} else if exists {
		return event, nil
	}
	unlock := s.lockRows(keys)
	defer unlock()
	rows := make([]*pb.RecordRow, 0, len(mutations))
	for _, mutation := range mutations {
		key := encodeRecordLogicalKey(mutation.GetKey())
		current, err := s.getRecordCurrent(key)
		if err != nil {
			return nil, err
		}
		if mutation.ExpectedRevision != nil {
			want := mutation.GetExpectedRevision()
			got := uint64(0)
			if current != nil {
				got = current.GetRevision()
			}
			if got != want {
				return nil, fmt.Errorf("revision conflict: expected %d, got %d", want, got)
			}
		}
		row := mergeRecordRow(current, mutation)
		row.Revision = 1
		if current != nil {
			row.Revision = current.GetRevision() + 1
		}
		row.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		rows = append(rows, row)
	}
	commitSeq, err := s.nextRecordCommitSeq()
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		row.CommitSeq = commitSeq
	}
	event := &pb.RecordRowsCommittedEvent{
		EventId: xid.New().String(), SourceId: s.recordSource, CommitSeq: commitSeq,
		CommittedAt: time.Now().UTC().Format(time.RFC3339Nano), Rows: rows,
	}
	event.EventId = s.recordSource + ":" + formatUint(commitSeq)
	data, err := proto.Marshal(event)
	if err != nil {
		return nil, err
	}
	batch := s.db.NewBatch()
	defer batch.Close()
	for _, row := range rows {
		historyKey := encodeRecordHistoryKey(row.GetKey(), row.GetRevision())
		rowData, err := proto.Marshal(row)
		if err != nil {
			return nil, err
		}
		if err := batch.Set(historyKey, rowData, nil); err != nil {
			return nil, err
		}
		if err := batch.Set(encodeRecordCurrentKey(row.GetKey()), rowData, nil); err != nil {
			return nil, err
		}
		if err := batch.Set(encodeRecordTimeKey(row), historyKey, nil); err != nil {
			return nil, err
		}
	}
	if err := batch.Set(encodeRecordJournalKey(commitSeq), data, nil); err != nil {
		return nil, err
	}
	requestValue := make([]byte, sha256.Size+8)
	copy(requestValue, digest[:])
	binary.BigEndian.PutUint64(requestValue[sha256.Size:], commitSeq)
	if err := batch.Set(encodeRecordRequestKey(requestID), requestValue, nil); err != nil {
		return nil, err
	}
	if err := batch.Set([]byte(recordCommitSeqKey), encodeUint(commitSeq), nil); err != nil {
		return nil, err
	}
	if s.commitHook != nil {
		if err := s.commitHook(); err != nil {
			return nil, err
		}
	}
	if err := batch.Commit(s.writeOptions); err != nil {
		return nil, err
	}
	return event, nil
}

func validateRecordMutation(mutation *pb.RecordMutation) error {
	if mutation == nil || mutation.GetKey() == nil {
		return errors.New("record mutation key is required")
	}
	key := mutation.GetKey()
	if key.GetSpaceId() == "" || key.GetDatasetId() == "" || key.GetRecordId() == "" {
		return errors.New("record mutation requires space_id, dataset_id and record_id")
	}
	return nil
}

func mergeRecordRow(current *pb.RecordRow, mutation *pb.RecordMutation) *pb.RecordRow {
	row := &pb.RecordRow{Key: proto.Clone(mutation.GetKey()).(*pb.RecordKey)}
	if current != nil {
		row = proto.Clone(current).(*pb.RecordRow)
		row.Key = proto.Clone(mutation.GetKey()).(*pb.RecordKey)
	}
	positions := make(map[string]int, len(row.GetColumns()))
	for index, column := range row.GetColumns() {
		positions[column.GetColumnName()] = index
	}
	for _, column := range mutation.GetColumns() {
		copied := proto.Clone(column).(*pb.ColumnValue)
		if index, ok := positions[column.GetColumnName()]; ok {
			row.Columns[index] = copied
			continue
		}
		positions[column.GetColumnName()] = len(row.Columns)
		row.Columns = append(row.Columns, copied)
	}
	if len(mutation.GetAttributes()) > 0 {
		if row.Attributes == nil {
			row.Attributes = make(map[string]string, len(mutation.GetAttributes()))
		}
		for key, value := range mutation.GetAttributes() {
			row.Attributes[key] = value
		}
	}
	return row
}

func digestRecordMutations(mutations []*pb.RecordMutation) ([sha256.Size]byte, error) {
	hash := sha256.New()
	for _, mutation := range mutations {
		data, err := (proto.MarshalOptions{Deterministic: true}).Marshal(mutation)
		if err != nil {
			return [sha256.Size]byte{}, err
		}
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(data)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(data)
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}

func (s *Store) loadIdempotentRecordEvent(requestID string, digest [sha256.Size]byte) (*pb.RecordRowsCommittedEvent, bool, error) {
	value, closer, err := s.db.Get(encodeRecordRequestKey(requestID))
	if errors.Is(err, cpebble.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer closer.Close()
	if len(value) != sha256.Size+8 || !bytes.Equal(value[:sha256.Size], digest[:]) {
		return nil, false, errors.New("request_id payload mismatch")
	}
	commitSeq := binary.BigEndian.Uint64(value[sha256.Size:])
	data, eventCloser, err := s.db.Get(encodeRecordJournalKey(commitSeq))
	if err != nil {
		return nil, false, err
	}
	defer eventCloser.Close()
	event := &pb.RecordRowsCommittedEvent{}
	if err := proto.Unmarshal(data, event); err != nil {
		return nil, false, err
	}
	return event, true, nil
}

func (s *Store) nextRecordCommitSeq() (uint64, error) {
	value, closer, err := s.db.Get([]byte(recordCommitSeqKey))
	if errors.Is(err, cpebble.ErrNotFound) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	defer closer.Close()
	if len(value) != 8 {
		return 0, errors.New("invalid record commit sequence")
	}
	return binary.BigEndian.Uint64(value) + 1, nil
}

func (s *Store) RecordWatermark(ctx context.Context) (string, uint64, error) {
	_ = ctx
	value, closer, err := s.db.Get([]byte(recordCommitSeqKey))
	if errors.Is(err, cpebble.ErrNotFound) {
		return s.recordSource, 0, nil
	}
	if err != nil {
		return "", 0, err
	}
	defer closer.Close()
	if len(value) != 8 {
		return "", 0, errors.New("invalid record commit sequence")
	}
	return s.recordSource, binary.BigEndian.Uint64(value), nil
}

func (s *Store) getRecordCurrent(key string) (*pb.RecordRow, error) {
	data, closer, err := s.db.Get(encodeRecordCurrentKeyFromLogical(key))
	if errors.Is(err, cpebble.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	row := &pb.RecordRow{}
	if err := proto.Unmarshal(data, row); err != nil {
		return nil, err
	}
	return row, nil
}

func (s *Store) readRecordHistory(ctx context.Context, key *pb.RecordKey, revisionRange *pb.RevisionRange, snapshot *cpebble.Snapshot) ([]*pb.RecordRow, error) {
	_ = ctx
	prefix := []byte(encodeRecordHistoryPrefix(key))
	var iter *cpebble.Iterator
	var err error
	if snapshot != nil {
		iter, err = snapshot.NewIter(&cpebble.IterOptions{LowerBound: prefix, UpperBound: nextPrefix(prefix)})
	} else {
		iter, err = s.db.NewIter(&cpebble.IterOptions{LowerBound: prefix, UpperBound: nextPrefix(prefix)})
	}
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	rows := make([]*pb.RecordRow, 0)
	for valid := iter.First(); valid; valid = iter.Next() {
		revision := decodeRecordRevision(iter.Key())
		if revisionRange != nil {
			if start := revisionRange.GetStartRevision(); start != 0 && revision < start {
				continue
			}
			if end := revisionRange.GetEndRevision(); end != 0 && revision > end {
				continue
			}
		}
		row := &pb.RecordRow{}
		if err := proto.Unmarshal(iter.Value(), row); err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return rows, nil
}

func encodeRecordLogicalKey(key *pb.RecordKey) string {
	return strings.Join([]string{escape(key.GetSpaceId()), escape(key.GetDatasetId()), escape(key.GetRecordId())}, "|")
}

func encodeRecordHistoryPrefix(key *pb.RecordKey) string {
	return recordHistoryPrefix + encodeRecordLogicalKey(key) + "|"
}

func encodeRecordHistoryKey(key *pb.RecordKey, revision uint64) []byte {
	return []byte(encodeRecordHistoryPrefix(key) + formatUint(revision))
}

func encodeRecordCurrentKey(key *pb.RecordKey) []byte {
	return encodeRecordCurrentKeyFromLogical(encodeRecordLogicalKey(key))
}

func encodeRecordCurrentKeyFromLogical(logical string) []byte {
	return []byte(recordCurrentPrefix + logical)
}

func encodeRecordTimeKey(row *pb.RecordRow) []byte {
	updated, err := time.Parse(time.RFC3339Nano, row.GetUpdatedAt())
	if err != nil {
		updated = time.Unix(0, 0).UTC()
	}
	stamp := updated.UTC().Format(fixedRecordTimeLayout)
	return []byte(recordTimePrefix + escape(row.GetKey().GetSpaceId()) + "|" + escape(row.GetKey().GetDatasetId()) + "|" + escape(stamp) + "|" + escape(row.GetKey().GetRecordId()) + "|" + formatUint(row.GetRevision()))
}

func encodeRecordJournalKey(commitSeq uint64) []byte {
	return []byte(recordJournalPrefix + formatUint(commitSeq))
}
func encodeRecordRequestKey(requestID string) []byte {
	return []byte(recordRequestPrefix + escape(requestID))
}

func decodeRecordRevision(key []byte) uint64 {
	parts := strings.Split(string(key), "|")
	if len(parts) == 0 {
		return 0
	}
	return parseUint(parts[len(parts)-1])
}

func encodeUint(value uint64) []byte {
	encoded := make([]byte, 8)
	binary.BigEndian.PutUint64(encoded, value)
	return encoded
}

func formatUint(value uint64) string {
	return fmt.Sprintf("%020d", value)
}

func parseUint(value string) uint64 {
	var parsed uint64
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return 0
		}
		parsed = parsed*10 + uint64(digit-'0')
	}
	return parsed
}
