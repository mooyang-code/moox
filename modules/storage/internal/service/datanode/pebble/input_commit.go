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
	WriteKindFactorPatch = "factor_patch"
	attrInputReady       = "moox.input_ready"
	attrCommitID         = "moox.commit_id"
	attrBindingVersion   = "moox.binding_version"
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

type FactorPatch struct {
	CommitID       string
	BindingVersion string
	OwnedFields    []string
	Row            *pb.RowFieldUpsert
}

type persistedReceipt struct {
	CommitID   string `json:"commit_id"`
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
			CommitID:   in.CommitID,
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

func (s *Store) PatchFactor(ctx context.Context, in FactorPatch) (*WriteReceipt, error) {
	if err := validateCommitID(in.CommitID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.BindingVersion) == "" || strings.TrimSpace(in.BindingVersion) != in.BindingVersion {
		return nil, invalid("binding_version is required")
	}
	owned, err := ownedFieldSet(in.OwnedFields)
	if err != nil {
		return nil, err
	}
	if in.Row == nil {
		return nil, invalid("row is required")
	}
	if err := rejectUnownedFactorFields(in.Row, owned); err != nil {
		return nil, err
	}
	prepared := proto.Clone(in.Row).(*pb.RowFieldUpsert)
	if hasReservedAttributes(prepared) {
		return nil, invalid("factor patch cannot write reserved input attributes")
	}
	setStringAttribute(prepared, attrBindingVersion, in.BindingVersion)
	fingerprint, err := factorFingerprint(in)
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
		if existing.WriteKind != WriteKindFactorPatch || !bytes.Equal(body, fingerprint) {
			return nil, CommitConflictError{CommitID: in.CommitID}
		}
		return existing, nil
	}
	var receipt *WriteReceipt
	entries, err := s.writeFieldsEventLocked(ctx, normalized, "", func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return BuildDatasetRowsUpsertedMessageWithKind(s.nodeID, "", WriteKindFactorPatch, spaceID, datasetID, rows)
	}, func(batch *cpebble.Batch, entries []*OutboxEntry) error {
		if len(entries) != 1 {
			return errors.New("factor patch requires one outbox position")
		}
		receipt = &WriteReceipt{
			CommitID:   in.CommitID,
			Position:   WritePosition{NodeID: s.nodeID, StoreID: s.sourceStoreID, Sequence: entries[0].ID},
			InputReady: false,
			WriteKind:  WriteKindFactorPatch,
		}
		return persistReceipt(batch, s.writeOptions, receipt, fingerprint)
	})
	if err != nil {
		return nil, err
	}
	if receipt == nil || len(entries) != 1 {
		return nil, errors.New("factor patch did not persist a receipt")
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
		Position:   WritePosition{NodeID: stored.NodeID, StoreID: stored.StoreID, Sequence: stored.Sequence},
		InputReady: stored.InputReady,
		WriteKind:  stored.WriteKind,
	}, fingerprint, nil
}

func persistReceipt(batch *cpebble.Batch, opts *cpebble.WriteOptions, receipt *WriteReceipt, fingerprint []byte) error {
	payload, err := json.Marshal(persistedReceipt{
		CommitID: receipt.CommitID, NodeID: receipt.Position.NodeID, StoreID: receipt.Position.StoreID,
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

func ownedFieldSet(fields []string) (map[string]struct{}, error) {
	if len(fields) == 0 {
		return nil, invalid("owned_fields are required")
	}
	owned := make(map[string]struct{}, len(fields))
	for _, name := range fields {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
			return nil, invalid("owned field id is invalid")
		}
		owned[name] = struct{}{}
	}
	return owned, nil
}

func rejectUnownedFactorFields(row *pb.RowFieldUpsert, owned map[string]struct{}) error {
	if len(row.GetFields()) == 0 {
		return invalid("factor patch fields are required")
	}
	for _, field := range row.GetFields() {
		if field == nil || field.GetFieldId() == "" {
			return invalid("field_id is required")
		}
		if _, ok := owned[field.GetFieldId()]; !ok {
			return invalidf("factor patch cannot write unowned field %q", field.GetFieldId())
		}
	}
	return nil
}

func hasReservedAttributes(row *pb.RowFieldUpsert) bool {
	for name := range row.GetAttributes() {
		if reservedWriteAttribute(name) {
			return true
		}
	}
	return false
}

func reservedWriteAttribute(name string) bool {
	switch name {
	case attrInputReady, attrCommitID, attrBindingVersion:
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

func factorFingerprint(in FactorPatch) ([]byte, error) {
	owned := append([]string(nil), in.OwnedFields...)
	sort.Strings(owned)
	row, err := commitFingerprint(in.Row)
	if err != nil {
		return nil, err
	}
	return proto.MarshalOptions{Deterministic: true}.Marshal(&pb.RowFieldUpsert{
		Key: &pb.RowKey{SpaceId: in.BindingVersion, DatasetId: strings.Join(owned, "\n")},
		Fields: []*pb.FieldValue{{
			FieldId: "body",
			Value:   &pb.TypedValue{Value: &pb.TypedValue_BytesValue{BytesValue: row}},
		}},
	})
}
