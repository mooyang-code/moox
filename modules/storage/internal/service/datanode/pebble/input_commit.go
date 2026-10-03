package pebble

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	cpebble "github.com/cockroachdb/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

const (
	WriteKindUnspecified = ""
	WriteKindInputCommit = "input_commit"
	attrInputReady       = "moox.input_ready"
	attrCommitID         = "moox.commit_id"
	writeReceiptPrefix   = "__write_receipt/"
	writeReceiptBodyPref = "__write_receipt_body/"
)

type WritePosition struct {
	NodeID   string
	StoreID  string
	Sequence uint64
}

type WriteReceipt struct {
	CommitID   string
	SpaceID    string
	DatasetID  string
	Position   WritePosition
	InputReady bool
	WriteKind  string
}

type InputCommit struct {
	CommitID       string
	RequiredFields []string
	Row            *pb.RowFieldUpsert
	WriteKind      string
}

type persistedReceipt struct {
	CommitID   string `json:"commit_id"`
	SpaceID    string `json:"space_id,omitempty"`
	DatasetID  string `json:"dataset_id,omitempty"`
	NodeID     string `json:"node_id"`
	StoreID    string `json:"store_id"`
	Sequence   uint64 `json:"sequence"`
	InputReady bool   `json:"input_ready"`
	WriteKind  string `json:"write_kind"`
}

func (s *Store) CommitInput(ctx context.Context, in InputCommit) (*WriteReceipt, error) {
	if err := validateCommitID(in.CommitID); err != nil {
		return nil, err
	}
	if in.WriteKind != "" && in.WriteKind != WriteKindInputCommit {
		return nil, invalid("commit input write_kind must be input_commit")
	}
	if len(in.RequiredFields) == 0 {
		return nil, invalid("required_fields are required")
	}
	if in.Row == nil {
		return nil, invalid("row is required")
	}
	if missing := missingRequiredFields(in.Row, in.RequiredFields); len(missing) > 0 {
		return nil, invalidf("required field %q is missing", missing[0])
	}
	prepared := proto.Clone(in.Row).(*pb.RowFieldUpsert)
	stripReservedAttributes(prepared)
	setBoolAttribute(prepared, attrInputReady, true)
	setStringAttribute(prepared, attrCommitID, in.CommitID)
	fingerprint, err := commitFingerprint(in.Row)
	if err != nil {
		return nil, err
	}
	normalized, err := s.normalizeWriteRows(ctx, []*pb.RowFieldUpsert{prepared})
	if err != nil {
		return nil, err
	}
	s.datasetWriteMu.RLock()
	defer s.datasetWriteMu.RUnlock()
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()
	if existing, body, err := s.loadReceiptLocked(in.CommitID); err != nil {
		return nil, err
	} else if existing != nil {
		if existing.WriteKind != WriteKindInputCommit || !bytes.Equal(body, fingerprint) {
			return nil, CommitConflictError{CommitID: in.CommitID}
		}
		return existing, nil
	}
	var receipt *WriteReceipt
	entries, err := s.writeFieldsEventLocked(ctx, normalized, "", func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return BuildDatasetRowsUpsertedMessageWithKind(s.nodeID, "", WriteKindInputCommit, spaceID, datasetID, rows)
	}, func(batch *cpebble.Batch, entries []*OutboxEntry) error {
		if len(entries) != 1 {
			return errors.New("input commit requires one outbox position")
		}
		receipt = &WriteReceipt{
			CommitID: in.CommitID, SpaceID: normalized[0].GetKey().GetSpaceId(), DatasetID: normalized[0].GetKey().GetDatasetId(),
			Position:   WritePosition{NodeID: s.nodeID, StoreID: s.sourceStoreID, Sequence: entries[0].ID},
			InputReady: true,
			WriteKind:  WriteKindInputCommit,
		}
		return persistReceipt(batch, s.writeOptions, receipt, fingerprint)
	})
	if err != nil {
		return nil, err
	}
	if receipt == nil || len(entries) != 1 {
		return nil, errors.New("input commit did not persist a receipt")
	}
	return receipt, nil
}

func (s *Store) LookupWriteReceipt(ctx context.Context, commitID string) (*WriteReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateCommitID(commitID); err != nil {
		return nil, err
	}
	if s == nil || s.db == nil {
		return nil, errors.New("pebble store is closed")
	}
	receipt, _, err := s.loadReceiptLocked(commitID)
	if err != nil {
		return nil, err
	}
	if receipt == nil {
		return nil, invalid("write receipt not found")
	}
	return receipt, nil
}

func (s *Store) loadReceiptLocked(commitID string) (*WriteReceipt, []byte, error) {
	data, closer, err := s.db.Get([]byte(writeReceiptPrefix + commitID))
	if errors.Is(err, cpebble.ErrNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	copied := append([]byte(nil), data...)
	_ = closer.Close()
	stored := persistedReceipt{}
	if err := json.Unmarshal(copied, &stored); err != nil {
		return nil, nil, err
	}
	body, closer, err := s.db.Get([]byte(writeReceiptBodyPref + commitID))
	if err != nil {
		return nil, nil, err
	}
	fingerprint := append([]byte(nil), body...)
	_ = closer.Close()
	return &WriteReceipt{
		CommitID:   stored.CommitID,
		SpaceID:    stored.SpaceID,
		DatasetID:  stored.DatasetID,
		Position:   WritePosition{NodeID: stored.NodeID, StoreID: stored.StoreID, Sequence: stored.Sequence},
		InputReady: stored.InputReady,
		WriteKind:  stored.WriteKind,
	}, fingerprint, nil
}

func persistReceipt(batch *cpebble.Batch, opts *cpebble.WriteOptions, receipt *WriteReceipt, fingerprint []byte) error {
	payload, err := json.Marshal(persistedReceipt{
		CommitID: receipt.CommitID, SpaceID: receipt.SpaceID, DatasetID: receipt.DatasetID, NodeID: receipt.Position.NodeID, StoreID: receipt.Position.StoreID,
		Sequence: receipt.Position.Sequence, InputReady: receipt.InputReady, WriteKind: receipt.WriteKind,
	})
	if err != nil {
		return err
	}
	if err := batch.Set([]byte(writeReceiptPrefix+receipt.CommitID), payload, opts); err != nil {
		return err
	}
	return batch.Set([]byte(writeReceiptBodyPref+receipt.CommitID), fingerprint, opts)
}

func validateCommitID(commitID string) error {
	if strings.TrimSpace(commitID) == "" || strings.TrimSpace(commitID) != commitID || strings.ContainsAny(commitID, "/\x00") {
		return invalid("commit_id is invalid")
	}
	return nil
}

func missingRequiredFields(row *pb.RowFieldUpsert, required []string) []string {
	present := make(map[string]struct{}, len(row.GetFields()))
	for _, field := range row.GetFields() {
		if field == nil || field.GetFieldId() == "" || field.GetValue() == nil {
			continue
		}
		if _, isNull := field.GetValue().GetValue().(*pb.TypedValue_NullValue); isNull {
			continue
		}
		present[field.GetFieldId()] = struct{}{}
	}
	var missing []string
	for _, name := range required {
		if _, ok := present[name]; !ok {
			missing = append(missing, name)
		}
	}
	return missing
}

func reservedWriteAttribute(name string) bool {
	switch name {
	case attrInputReady, attrCommitID:
		return true
	default:
		return false
	}
}

func stripReservedAttributes(row *pb.RowFieldUpsert) {
	if row == nil || len(row.GetAttributes()) == 0 {
		return
	}
	for name := range row.Attributes {
		if reservedWriteAttribute(name) {
			delete(row.Attributes, name)
		}
	}
}

func setBoolAttribute(row *pb.RowFieldUpsert, name string, value bool) {
	if row.Attributes == nil {
		row.Attributes = map[string]*pb.TypedValue{}
	}
	row.Attributes[name] = &pb.TypedValue{Value: &pb.TypedValue_BoolValue{BoolValue: value}}
}

func setStringAttribute(row *pb.RowFieldUpsert, name, value string) {
	if row.Attributes == nil {
		row.Attributes = map[string]*pb.TypedValue{}
	}
	row.Attributes[name] = &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: value}}
}

func commitFingerprint(row *pb.RowFieldUpsert) ([]byte, error) {
	clone := proto.Clone(row).(*pb.RowFieldUpsert)
	stripReservedAttributes(clone)
	if len(clone.Attributes) == 0 {
		clone.Attributes = nil
	}
	sort.Slice(clone.Fields, func(i, j int) bool {
		return clone.Fields[i].GetFieldId() < clone.Fields[j].GetFieldId()
	})
	return proto.MarshalOptions{Deterministic: true}.Marshal(clone)
}
