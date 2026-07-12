package journal

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

const formatVersion uint32 = 1

type PartitionPhase string

const (
	PhaseDirty          PartitionPhase = "dirty"
	PhaseWriting        PartitionPhase = "writing"
	PhaseLocalCommitted PartitionPhase = "local_committed"
	PhaseRegistered     PartitionPhase = "registered"
	PhaseClean          PartitionPhase = "clean"
)

type PartitionState struct {
	Key                domain.PartitionKey                 `json:"key"`
	Phase              PartitionPhase                      `json:"phase"`
	HighWaterSeq       uint64                              `json:"high_water_seq"`
	MaterializingSeq   uint64                              `json:"materializing_seq"`
	Generation         uint64                              `json:"generation"`
	Sealed             bool                                `json:"sealed"`
	StartedAt          time.Time                           `json:"started_at"`
	LastMaterializedAt time.Time                           `json:"last_materialized_at"`
	Schema             map[string]storagepb.FieldValueType `json:"schema"`
	Manifest           *domain.Manifest                    `json:"manifest,omitempty"`
	COS                domain.COSState                     `json:"cos"`
	LastError          string                              `json:"last_error,omitempty"`
}

type PendingPatch struct {
	Seq   uint64
	RowID string
	Patch domain.RowPatch
}
type AppendResult struct {
	Seq        uint64
	Duplicate  bool
	Partitions []domain.PartitionKey
}
type MessageReceipt struct {
	MessageID  string    `json:"message_id"`
	Seq        uint64    `json:"seq"`
	ReceivedAt time.Time `json:"received_at"`
}
type MaterializationAttempt struct {
	Key        domain.PartitionKey
	Generation uint64
	ThroughSeq uint64
	StartedAt  time.Time
}
type QuarantineRecord struct {
	MessageID     string    `json:"message_id"`
	Subject       string    `json:"subject"`
	StreamSeq     uint64    `json:"stream_seq"`
	Delivery      uint64    `json:"delivery_count"`
	Reason        string    `json:"reason"`
	RawEnvelope   []byte    `json:"raw_envelope"`
	QuarantinedAt time.Time `json:"quarantined_at"`
}
type Status struct {
	PendingRows     uint64
	DirtyPartitions uint64
	OldestPending   time.Time
}

type Store struct {
	db        *pebble.DB
	mu        sync.Mutex
	closeOnce sync.Once
	closeErr  error
}

func Open(path string) (*Store, error) {
	db, err := pebble.Open(path, &pebble.Options{})
	if err != nil {
		return nil, fmt.Errorf("open archive journal: %w", err)
	}
	s := &Store{db: db}
	value, closer, getErr := db.Get([]byte("meta/version"))
	if errors.Is(getErr, pebble.ErrNotFound) {
		batch := db.NewBatch()
		defer batch.Close()
		if err := batch.Set([]byte("meta/version"), u32(formatVersion), nil); err != nil || batch.Commit(pebble.Sync) != nil {
			_ = db.Close()
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("initialize archive journal")
		}
	} else if getErr != nil {
		_ = db.Close()
		return nil, getErr
	} else {
		if closer != nil {
			_ = closer.Close()
		}
		if len(value) != 4 || binary.BigEndian.Uint32(value) != formatVersion {
			_ = db.Close()
			return nil, fmt.Errorf("unsupported archive journal version")
		}
	}
	return s, nil
}

func (s *Store) Append(ctx context.Context, event domain.EventBatch) (AppendResult, error) {
	if err := ctx.Err(); err != nil {
		return AppendResult{}, err
	}
	if strings.TrimSpace(event.MessageID) == "" || len(event.Rows) == 0 {
		return AppendResult{}, fmt.Errorf("message_id and rows are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if raw, closer, err := s.db.Get(messageKey(event.MessageID)); err == nil {
		if closer != nil {
			defer closer.Close()
		}
		var receipt MessageReceipt
		if err := json.Unmarshal(raw, &receipt); err != nil {
			return AppendResult{}, err
		}
		return AppendResult{Seq: receipt.Seq, Duplicate: true}, nil
	} else if !errors.Is(err, pebble.ErrNotFound) {
		return AppendResult{}, err
	}
	seq, err := s.nextSeq()
	if err != nil {
		return AppendResult{}, err
	}
	states := make(map[string]PartitionState)
	partitions := make([]domain.PartitionKey, 0)
	seenPartitions := make(map[string]bool)
	for _, patch := range event.Rows {
		if err := patch.Partition.Validate(); err != nil {
			return AppendResult{}, err
		}
		stateID := domain.PartitionID(patch.Partition)
		if !seenPartitions[stateID] {
			seenPartitions[stateID] = true
			partitions = append(partitions, patch.Partition)
		}
		state, err := s.getPartition(patch.Partition)
		if err != nil {
			return AppendResult{}, err
		}
		if state.Schema == nil {
			state.Schema = map[string]storagepb.FieldValueType{}
		}
		for name, value := range patch.Columns {
			if old, exists := state.Schema[name]; exists && old != value.Type {
				return AppendResult{}, fmt.Errorf("schema conflict for column %s", name)
			}
			state.Schema[name] = value.Type
			global, err := s.getSchema(patch.Partition.SpaceID, patch.Partition.DatasetID, name)
			if err != nil {
				return AppendResult{}, err
			}
			if global != storagepb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED && global != value.Type {
				return AppendResult{}, fmt.Errorf("schema conflict for dataset column %s", name)
			}
		}
		state.Key = patch.Partition
		state.Phase = PhaseDirty
		state.HighWaterSeq = seq
		state.COS.Status = "dirty"
		states[stateID] = state
	}
	batch := s.db.NewBatch()
	defer batch.Close()
	for _, patch := range event.Rows {
		raw, err := json.Marshal(patch)
		if err != nil {
			return AppendResult{}, err
		}
		id := domain.LogicalRowID(patch.DataTime, patch.DimensionsJSON)
		if err := batch.Set(pendingKey(patch.Partition, seq, id), raw, nil); err != nil {
			return AppendResult{}, err
		}
		for name, value := range patch.Columns {
			if err := batch.Set(schemaKey(patch.Partition.SpaceID, patch.Partition.DatasetID, name), u32(uint32(value.Type)), nil); err != nil {
				return AppendResult{}, err
			}
		}
	}
	for id, state := range states {
		raw, err := json.Marshal(state)
		if err != nil {
			return AppendResult{}, err
		}
		if err := batch.Set(partitionKey(state.Key), raw, nil); err != nil {
			return AppendResult{}, err
		}
		_ = id
	}
	receipt, _ := json.Marshal(MessageReceipt{MessageID: event.MessageID, Seq: seq, ReceivedAt: time.Now().UTC()})
	if err := batch.Set(messageKey(event.MessageID), receipt, nil); err != nil {
		return AppendResult{}, err
	}
	if err := batch.Set([]byte("meta/next-seq"), u64(seq+1), nil); err != nil {
		return AppendResult{}, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return AppendResult{}, fmt.Errorf("sync archive journal: %w", err)
	}
	return AppendResult{Seq: seq, Partitions: partitions}, nil
}

func (s *Store) Quarantine(ctx context.Context, record QuarantineRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if record.QuarantinedAt.IsZero() {
		record.QuarantinedAt = time.Now().UTC()
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Set([]byte(fmt.Sprintf("quarantine/%020d/%s", record.QuarantinedAt.UnixNano(), hash(record.MessageID))), raw, pebble.Sync)
}

func (s *Store) DirtyPartitions(ctx context.Context, limit int) ([]PartitionState, error) {
	if limit <= 0 {
		limit = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: []byte("partition/"), UpperBound: []byte("partition0")})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	out := make([]PartitionState, 0, limit)
	for iter.First(); iter.Valid() && len(out) < limit; iter.Next() {
		var state PartitionState
		if err := json.Unmarshal(iter.Value(), &state); err != nil {
			return nil, err
		}
		if state.Phase != PhaseClean || state.HighWaterSeq > state.MaterializingSeq {
			out = append(out, state)
		}
	}
	return out, iter.Error()
}

func (s *Store) Pending(ctx context.Context, key domain.PartitionKey, through uint64) ([]PendingPatch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := []byte("pending/" + domain.PartitionID(key) + "/")
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: append(append([]byte{}, prefix...), 0xff)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	out := []PendingPatch{}
	for iter.First(); iter.Valid(); iter.Next() {
		parts := strings.Split(string(iter.Key()), "/")
		if len(parts) != 4 {
			continue
		}
		var seq uint64
		if _, err := fmt.Sscanf(parts[2], "%d", &seq); err != nil || seq > through {
			continue
		}
		var patch domain.RowPatch
		if err := json.Unmarshal(iter.Value(), &patch); err != nil {
			return nil, err
		}
		out = append(out, PendingPatch{Seq: seq, RowID: parts[3], Patch: patch})
	}
	return out, iter.Error()
}

func (s *Store) BeginMaterialization(ctx context.Context, key domain.PartitionKey) (MaterializationAttempt, error) {
	if err := ctx.Err(); err != nil {
		return MaterializationAttempt{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.getPartition(key)
	if err != nil {
		return MaterializationAttempt{}, err
	}
	if state.Phase == PhaseWriting || state.Phase == PhaseLocalCommitted || state.Phase == PhaseRegistered {
		return MaterializationAttempt{Key: key, Generation: state.Generation, ThroughSeq: state.MaterializingSeq, StartedAt: state.StartedAt}, nil
	}
	state.Generation++
	state.MaterializingSeq = state.HighWaterSeq
	state.StartedAt = time.Now().UTC()
	state.Phase = PhaseWriting
	raw, _ := json.Marshal(state)
	if err := s.db.Set(partitionKey(key), raw, pebble.Sync); err != nil {
		return MaterializationAttempt{}, err
	}
	return MaterializationAttempt{Key: key, Generation: state.Generation, ThroughSeq: state.MaterializingSeq, StartedAt: state.StartedAt}, nil
}

func (s *Store) MarkLocalCommitted(ctx context.Context, key domain.PartitionKey, manifest domain.Manifest) error {
	return s.updatePhase(ctx, key, PhaseLocalCommitted, &manifest)
}
func (s *Store) MarkRegistered(ctx context.Context, key domain.PartitionKey) error {
	return s.updatePhase(ctx, key, PhaseRegistered, nil)
}

func (s *Store) Complete(ctx context.Context, key domain.PartitionKey, through uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.getPartition(key)
	if err != nil {
		return err
	}
	prefix := []byte("pending/" + domain.PartitionID(key) + "/")
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: append(append([]byte{}, prefix...), 0xff)})
	if err != nil {
		return err
	}
	batch := s.db.NewBatch()
	defer batch.Close()
	for iter.First(); iter.Valid(); iter.Next() {
		parts := strings.Split(string(iter.Key()), "/")
		if len(parts) != 4 {
			continue
		}
		var seq uint64
		if _, err := fmt.Sscanf(parts[2], "%d", &seq); err == nil && seq <= through {
			if err := batch.Delete(iter.Key(), nil); err != nil {
				iter.Close()
				return err
			}
		}
	}
	iter.Close()
	if state.HighWaterSeq <= through {
		state.Phase = PhaseClean
	} else {
		state.Phase = PhaseDirty
	}
	state.LastMaterializedAt = time.Now().UTC()
	raw, _ := json.Marshal(state)
	if err := batch.Set(partitionKey(key), raw, nil); err != nil {
		return err
	}
	return batch.Commit(pebble.Sync)
}

func (s *Store) Status(ctx context.Context) (Status, error) {
	if err := ctx.Err(); err != nil {
		return Status{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: []byte("pending/"), UpperBound: []byte("pending0")})
	if err != nil {
		return Status{}, err
	}
	defer iter.Close()
	var status Status
	for iter.First(); iter.Valid(); iter.Next() {
		status.PendingRows++
		if t := iter.Key(); len(t) > 0 {
			_ = t
		}
	}
	states, err := s.dirtyWithoutLock(1000000)
	if err != nil {
		return Status{}, err
	}
	status.DirtyPartitions = uint64(len(states))
	return status, nil
}

func (s *Store) PruneMessageReceipts(ctx context.Context, before time.Time) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: []byte("message/"), UpperBound: []byte("message0")})
	if err != nil {
		return 0, err
	}
	defer iter.Close()
	batch := s.db.NewBatch()
	defer batch.Close()
	var n uint64
	for iter.First(); iter.Valid(); iter.Next() {
		var r MessageReceipt
		if json.Unmarshal(iter.Value(), &r) == nil && r.ReceivedAt.Before(before) {
			if err := batch.Delete(iter.Key(), nil); err != nil {
				return n, err
			}
			n++
		}
	}
	if n > 0 && batch.Commit(pebble.Sync) != nil {
		return n, fmt.Errorf("prune message receipts")
	}
	return n, nil
}

func (s *Store) IncompleteMaterializations(ctx context.Context) ([]PartitionState, error) {
	return s.DirtyPartitions(ctx, 1000000)
}
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	s.closeOnce.Do(func() { s.closeErr = s.db.Close() })
	return s.closeErr
}

func (s *Store) updatePhase(ctx context.Context, key domain.PartitionKey, phase PartitionPhase, manifest *domain.Manifest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.getPartition(key)
	if err != nil {
		return err
	}
	state.Phase = phase
	if manifest != nil {
		state.Manifest = manifest
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return s.db.Set(partitionKey(key), raw, pebble.Sync)
}
func (s *Store) dirtyWithoutLock(limit int) ([]PartitionState, error) {
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: []byte("partition/"), UpperBound: []byte("partition0")})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	out := []PartitionState{}
	for iter.First(); iter.Valid() && len(out) < limit; iter.Next() {
		var state PartitionState
		if err := json.Unmarshal(iter.Value(), &state); err != nil {
			return nil, err
		}
		if state.Phase != PhaseClean || state.HighWaterSeq > state.MaterializingSeq {
			out = append(out, state)
		}
	}
	return out, iter.Error()
}
func (s *Store) nextSeq() (uint64, error) {
	raw, closer, err := s.db.Get([]byte("meta/next-seq"))
	if closer != nil {
		defer closer.Close()
	}
	if errors.Is(err, pebble.ErrNotFound) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	if len(raw) != 8 {
		return 0, fmt.Errorf("invalid next sequence")
	}
	return binary.BigEndian.Uint64(raw), nil
}
func (s *Store) getPartition(key domain.PartitionKey) (PartitionState, error) {
	raw, closer, err := s.db.Get(partitionKey(key))
	if closer != nil {
		defer closer.Close()
	}
	if errors.Is(err, pebble.ErrNotFound) {
		return PartitionState{Key: key, Phase: PhaseDirty, Schema: map[string]storagepb.FieldValueType{}}, nil
	}
	if err != nil {
		return PartitionState{}, err
	}
	var state PartitionState
	if err := json.Unmarshal(raw, &state); err != nil {
		return PartitionState{}, err
	}
	return state, nil
}
func (s *Store) getSchema(space, dataset, column string) (storagepb.FieldValueType, error) {
	raw, closer, err := s.db.Get(schemaKey(space, dataset, column))
	if closer != nil {
		defer closer.Close()
	}
	if errors.Is(err, pebble.ErrNotFound) {
		return storagepb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED, nil
	}
	if err != nil {
		return 0, err
	}
	if len(raw) != 4 {
		return 0, fmt.Errorf("invalid schema type")
	}
	return storagepb.FieldValueType(binary.BigEndian.Uint32(raw)), nil
}
func partitionKey(key domain.PartitionKey) []byte {
	return []byte("partition/" + domain.PartitionID(key))
}
func pendingKey(key domain.PartitionKey, seq uint64, rowID string) []byte {
	return []byte(fmt.Sprintf("pending/%s/%020d/%s", domain.PartitionID(key), seq, rowID))
}
func schemaKey(space, dataset, column string) []byte {
	return []byte("schema/" + domain.EncodeIdentity(space) + "/" + domain.EncodeIdentity(dataset) + "/" + domain.EncodeIdentity(column))
}
func messageKey(id string) []byte { return []byte("message/" + hash(id)) }
func hash(raw string) string      { sum := sha256.Sum256([]byte(raw)); return hex.EncodeToString(sum[:]) }
func u32(v uint32) []byte         { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func u64(v uint64) []byte         { b := make([]byte, 8); binary.BigEndian.PutUint64(b, v); return b }
