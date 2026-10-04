package pebble

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"

	cpebble "github.com/cockroachdb/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

const WriteKindFactorResult = "factor_result"

func (s *Store) WriteFactorRows(ctx context.Context, spaceID, datasetID, commitID string, rows []*pb.RowFieldUpsert) (uint64, error) {
	if err := validateCommitID(commitID); err != nil {
		return 0, err
	}
	if spaceID == "" || datasetID == "" || len(rows) == 0 {
		return 0, invalid("space_id, dataset_id and rows are required")
	}
	for _, row := range rows {
		if row == nil || row.GetKey() == nil {
			return 0, invalid("row key is required")
		}
		key := row.GetKey()
		if key.GetSpaceId() != spaceID || key.GetDatasetId() != datasetID {
			return 0, invalid("row dataset identity does not match request")
		}
		if len(row.GetFields()) == 0 {
			return 0, invalid("factor result row fields are required")
		}
		fieldIDs := make(map[string]struct{}, len(row.GetFields()))
		for _, field := range row.GetFields() {
			if field == nil || strings.TrimSpace(field.GetFieldId()) == "" || field.GetValue() == nil {
				return 0, invalid("factor result field_id and value are required")
			}
			if _, exists := fieldIDs[field.GetFieldId()]; exists {
				return 0, invalid("duplicate field_id in factor result row")
			}
			fieldIDs[field.GetFieldId()] = struct{}{}
		}
	}

	normalized, err := s.normalizeWriteRows(ctx, rows)
	if err != nil {
		return 0, err
	}
	seen := make(map[string]struct{}, len(normalized))
	for _, row := range normalized {
		identity, err := proto.MarshalOptions{Deterministic: true}.Marshal(row.GetKey())
		if err != nil {
			return 0, err
		}
		if _, exists := seen[string(identity)]; exists {
			return 0, invalid("duplicate row key in factor result batch")
		}
		seen[string(identity)] = struct{}{}
	}
	canonical := canonicalFactorRows(normalized)
	fingerprint, err := proto.MarshalOptions{Deterministic: true}.Marshal(&pb.WriteFactorRowsReq{
		SpaceId: spaceID, DatasetId: datasetID, Rows: canonical,
	})
	if err != nil {
		return 0, err
	}

	s.datasetWriteMu.RLock()
	defer s.datasetWriteMu.RUnlock()
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()
	if existing, body, err := s.loadReceiptLocked(commitID); err != nil {
		return 0, err
	} else if existing != nil {
		if existing.WriteKind != WriteKindFactorResult || !bytes.Equal(body, receiptDigest(fingerprint)) {
			return 0, CommitConflictError{CommitID: commitID}
		}
		return uint64(len(normalized)), nil
	}
	var receipt *commitReceipt
	entries, err := s.writeFieldsEventLocked(ctx, normalized, "", func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return BuildDatasetRowsUpsertedMessageWithKind(s.nodeID, "", WriteKindFactorResult, spaceID, datasetID, rows)
	}, func(batch *cpebble.Batch, entries []*OutboxEntry) error {
		if len(entries) != 1 {
			return errors.New("factor result write requires one outbox position")
		}
		receipt = &commitReceipt{
			CommitID: commitID, SpaceID: spaceID, DatasetID: datasetID,
			Position:  commitPosition{NodeID: s.nodeID, StoreID: s.sourceStoreID, Sequence: entries[0].ID},
			WriteKind: WriteKindFactorResult,
		}
		return persistReceipt(batch, s.writeOptions, receipt, fingerprint)
	})
	if err != nil {
		return 0, err
	}
	if receipt == nil || len(entries) != 1 {
		return 0, errors.New("factor result write did not persist a receipt")
	}
	return uint64(len(normalized)), nil
}

func canonicalFactorRows(rows []*pb.RowFieldUpsert) []*pb.RowFieldUpsert {
	canonical := make([]*pb.RowFieldUpsert, len(rows))
	for i, row := range rows {
		canonical[i] = proto.Clone(row).(*pb.RowFieldUpsert)
		sort.Slice(canonical[i].Fields, func(left, right int) bool {
			return canonical[i].Fields[left].GetFieldId() < canonical[i].Fields[right].GetFieldId()
		})
	}
	sort.Slice(canonical, func(left, right int) bool {
		leftKey, _ := proto.MarshalOptions{Deterministic: true}.Marshal(canonical[left].GetKey())
		rightKey, _ := proto.MarshalOptions{Deterministic: true}.Marshal(canonical[right].GetKey())
		return bytes.Compare(leftKey, rightKey) < 0
	})
	return canonical
}
