package pebble

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	cpebble "github.com/cockroachdb/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"google.golang.org/protobuf/proto"
)

const (
	outboxPrefix         = "__outbox/"
	processedEventPrefix = "__processed_event/"
	metaNextID           = "__meta/next_outbox_id"
)

// Options controls one DataNode Pebble database. A node can host multiple
// datasets; the dataset identity is part of every physical key.
type Options struct {
	Path              string
	NodeID            string
	BucketDuration    time.Duration
	MaxEventBytes     int
	DisableSyncWrites bool
}

type OutboxEntry struct {
	ID        uint64
	Data      []byte
	CreatedAt time.Time
}

type OutboxStats struct {
	Pending       int
	OldestEventAt time.Time
}

type Store struct {
	db             *cpebble.DB
	writeOptions   *cpebble.WriteOptions
	nodeID         string
	bucketDuration time.Duration
	maxEventBytes  int
	outboxMu       sync.Mutex
	maintenanceMu  sync.Mutex
	maintenanceWG  sync.WaitGroup
	closing        bool
}

func Open(opts Options) (*Store, error) {
	if strings.TrimSpace(opts.Path) == "" {
		return nil, errors.New("pebble path is required")
	}
	if strings.TrimSpace(opts.NodeID) == "" {
		return nil, errors.New("node_id is required")
	}
	if opts.BucketDuration <= 0 {
		opts.BucketDuration = 24 * time.Hour
	}
	db, err := cpebble.Open(opts.Path, &cpebble.Options{})
	if err != nil {
		return nil, err
	}
	writeOptions := cpebble.Sync
	if opts.DisableSyncWrites {
		writeOptions = cpebble.NoSync
	}
	return &Store{db: db, writeOptions: writeOptions, nodeID: opts.NodeID, bucketDuration: opts.BucketDuration, maxEventBytes: opts.MaxEventBytes}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	s.maintenanceMu.Lock()
	s.closing = true
	s.maintenanceMu.Unlock()
	s.maintenanceWG.Wait()
	return s.db.Close()
}

func (s *Store) compactAsync(start, end []byte) {
	s.maintenanceMu.Lock()
	if s.closing {
		s.maintenanceMu.Unlock()
		return
	}
	s.maintenanceWG.Add(1)
	s.maintenanceMu.Unlock()
	go func() {
		defer s.maintenanceWG.Done()
		_ = s.db.Compact(start, end, true)
	}()
}

func (s *Store) NodeID() string {
	if s == nil {
		return ""
	}
	return s.nodeID
}

// WriteFields upserts each Field/Attribute independently. All values and the
// corresponding event are committed in one Pebble batch by WriteFieldsEvent.
func (s *Store) WriteFields(ctx context.Context, rows []*pb.RowFieldUpsert) error {
	_, err := s.WriteFieldsEvent(ctx, rows, nil)
	return err
}

// WriteFieldsEvent returns one durable event payload per dataset represented in
// the request. The payload is optional so callers that only need local writes
// can avoid creating an outbox record.
func (s *Store) WriteFieldsEvent(ctx context.Context, rows []*pb.RowFieldUpsert, event func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error)) ([]*OutboxEntry, error) {
	return s.writeFieldsEvent(ctx, rows, "", event)
}

// WriteFieldsEventWithSource atomically records a source EventMessage ID with
// the row mutation and its outbox entry. A redelivery after a successful write
// and failed ACK therefore becomes a no-op instead of creating a second
// DatasetRowsUpserted event.
func (s *Store) WriteFieldsEventWithSource(ctx context.Context, rows []*pb.RowFieldUpsert, sourceEventID string, event func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error)) ([]*OutboxEntry, error) {
	if strings.TrimSpace(sourceEventID) == "" {
		return nil, invalid("source_event_id is required")
	}
	if event == nil {
		return nil, invalid("event builder is required for source-id writes")
	}
	return s.writeFieldsEvent(ctx, rows, sourceEventID, event)
}

func (s *Store) writeFieldsEvent(ctx context.Context, rows []*pb.RowFieldUpsert, sourceEventID string, event func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error)) ([]*OutboxEntry, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("pebble store is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, invalid("rows are required")
	}
	normalizedRows := make([]*pb.RowFieldUpsert, 0, len(rows))
	for _, row := range rows {
		if err := validateUpsert(row); err != nil {
			return nil, err
		}
		key, err := NormalizeRowKey(row.GetKey())
		if err != nil {
			return nil, err
		}
		clone := proto.Clone(row).(*pb.RowFieldUpsert)
		clone.Key = key
		normalizedRows = append(normalizedRows, clone)
	}
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()
	grouped := groupRowsByDataset(normalizedRows)
	if sourceEventID != "" {
		pending := make(map[datasetGroup][]*pb.RowFieldUpsert, len(grouped))
		for group, groupRows := range grouped {
			processed, err := s.hasProcessedSourceEvent(sourceEventID, group)
			if err != nil {
				return nil, err
			}
			if !processed {
				pending[group] = groupRows
			}
		}
		grouped = pending
		if len(grouped) == 0 {
			return nil, nil
		}
		normalizedRows = make([]*pb.RowFieldUpsert, 0, len(rows))
		for _, groupRows := range grouped {
			normalizedRows = append(normalizedRows, groupRows...)
		}
	}
	// One batch preserves all field changes from a request. Dataset event
	// payloads and source-event markers are staged into the same batch.
	batch := s.db.NewBatch()
	defer batch.Close()
	for _, row := range normalizedRows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, field := range row.GetFields() {
			key, err := encodeFieldKey(row.GetKey(), field.GetFieldId(), s.bucketDuration)
			if err != nil {
				return nil, err
			}
			data, err := proto.Marshal(field.GetValue())
			if err != nil {
				return nil, fmt.Errorf("marshal field %q: %w", field.GetFieldId(), err)
			}
			if err := batch.Set(key, data, s.writeOptions); err != nil {
				return nil, err
			}
		}
		for name, value := range row.GetAttributes() {
			key, err := encodeAttributeKey(row.GetKey(), name, s.bucketDuration)
			if err != nil {
				return nil, err
			}
			data, err := proto.Marshal(value)
			if err != nil {
				return nil, fmt.Errorf("marshal attribute %q: %w", name, err)
			}
			if err := batch.Set(key, data, s.writeOptions); err != nil {
				return nil, err
			}
		}
	}

	entries := make([]*OutboxEntry, 0, len(grouped))
	if event != nil {
		nextID, err := s.nextOutboxID()
		if err != nil {
			return nil, err
		}
		for _, group := range sortedDatasetGroups(grouped) {
			groupRows := grouped[group]
			payload, err := event(group.spaceID, group.datasetID, groupRows)
			if err != nil {
				return nil, err
			}
			newEnvelope := &eventpb.EventMessage{}
			if err := proto.Unmarshal(payload, newEnvelope); err != nil {
				return nil, fmt.Errorf("unmarshal rows.upserted event for %s/%s: %w", group.spaceID, group.datasetID, err)
			}
			if err := validateDatasetRowsUpsertedEvent(newEnvelope, ""); err != nil {
				return nil, fmt.Errorf("validate rows.upserted event for %s/%s: %w", group.spaceID, group.datasetID, err)
			}
			if newEnvelope.GetSpaceId() != group.spaceID || newEnvelope.GetSubjectId() != group.datasetID {
				return nil, fmt.Errorf("rows.upserted event identity %s/%s does not match write group %s/%s", newEnvelope.GetSpaceId(), newEnvelope.GetSubjectId(), group.spaceID, group.datasetID)
			}
			id := nextID
			payload, err = BindOutboxID(payload, s.nodeID, id)
			if err != nil {
				return nil, err
			}
			if s.maxEventBytes > 0 && len(payload) > s.maxEventBytes {
				return nil, invalidf("event payload size %d exceeds limit %d", len(payload), s.maxEventBytes)
			}
			if err := batch.Set([]byte(outboxKey(id)), payload, s.writeOptions); err != nil {
				return nil, err
			}
			if err := s.setNextOutboxID(batch, id+1); err != nil {
				return nil, err
			}
			entries = append(entries, &OutboxEntry{ID: id, Data: append([]byte(nil), payload...), CreatedAt: time.Now().UTC()})
			if sourceEventID != "" {
				if err := batch.Set(processedSourceEventKey(sourceEventID, group), []byte{1}, s.writeOptions); err != nil {
					return nil, err
				}
			}
			nextID++
		}
	}
	if err := batch.Commit(s.writeOptions); err != nil {
		return nil, err
	}
	return entries, nil
}

func (s *Store) hasProcessedSourceEvent(sourceEventID string, group datasetGroup) (bool, error) {
	value, closer, err := s.db.Get(processedSourceEventKey(sourceEventID, group))
	if errors.Is(err, cpebble.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	closer.Close()
	return len(value) > 0, nil
}

func processedSourceEventKey(sourceEventID string, group datasetGroup) []byte {
	hash := sha256.Sum256([]byte(sourceEventID + "\x00" + group.spaceID + "\x00" + group.datasetID))
	return []byte(processedEventPrefix + hex.EncodeToString(hash[:]))
}

func sortedDatasetGroups(grouped map[datasetGroup][]*pb.RowFieldUpsert) []datasetGroup {
	groups := make([]datasetGroup, 0, len(grouped))
	for group := range grouped {
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].spaceID == groups[j].spaceID {
			return groups[i].datasetID < groups[j].datasetID
		}
		return groups[i].spaceID < groups[j].spaceID
	})
	return groups
}

func validateUpsert(row *pb.RowFieldUpsert) error {
	if row == nil || row.GetKey() == nil {
		return invalid("row key is required")
	}
	if len(row.GetFields()) == 0 && len(row.GetAttributes()) == 0 {
		return invalid("at least one field or attribute is required")
	}
	seen := make(map[string]struct{}, len(row.GetFields()))
	for _, field := range row.GetFields() {
		if field == nil || field.GetFieldId() == "" || field.GetValue() == nil {
			return invalid("field_id and value are required")
		}
		if _, ok := seen[field.GetFieldId()]; ok {
			return invalidf("duplicate field_id %q", field.GetFieldId())
		}
		seen[field.GetFieldId()] = struct{}{}
	}
	for name, value := range row.GetAttributes() {
		if name == "" || value == nil {
			return invalid("attribute key and value are required")
		}
	}
	return nil
}

type datasetGroup struct{ spaceID, datasetID string }

func groupRowsByDataset(rows []*pb.RowFieldUpsert) map[datasetGroup][]*pb.RowFieldUpsert {
	out := make(map[datasetGroup][]*pb.RowFieldUpsert)
	for _, row := range rows {
		key := row.GetKey()
		group := datasetGroup{spaceID: key.GetSpaceId(), datasetID: key.GetDatasetId()}
		out[group] = append(out[group], row)
	}
	return out
}

func (s *Store) ReadFields(ctx context.Context, keys []*pb.RowKey, fieldIDs, attributeKeys []string) ([]*pb.RowFieldValues, error) {
	rows, _, err := s.ReadFieldsWithPresence(ctx, keys, fieldIDs, attributeKeys)
	return rows, err
}

// DeleteFields removes the explicitly addressed field and attribute values for
// the supplied rows. The caller owns the field schema and must provide at
// least one namespace key, so cleanup can delete temporary rows without
// exposing raw Pebble keys.
func (s *Store) DeleteFields(ctx context.Context, keys []*pb.RowKey, fieldIDs, attributeKeys []string) error {
	if s == nil || s.db == nil {
		return errors.New("pebble store is closed")
	}
	if len(keys) == 0 || (len(fieldIDs) == 0 && len(attributeKeys) == 0) {
		return invalid("keys and field_ids or attribute_keys are required")
	}
	batch := s.db.NewBatch()
	defer batch.Close()
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, fieldID := range fieldIDs {
			physical, err := encodeFieldKey(key, fieldID, s.bucketDuration)
			if err != nil {
				return err
			}
			if err := batch.Delete(physical, s.writeOptions); err != nil {
				return err
			}
		}
		for _, attributeKey := range attributeKeys {
			physical, err := encodeAttributeKey(key, attributeKey, s.bucketDuration)
			if err != nil {
				return err
			}
			if err := batch.Delete(physical, s.writeOptions); err != nil {
				return err
			}
		}
	}
	return batch.Commit(s.writeOptions)
}

func (s *Store) ReadFieldsWithPresence(ctx context.Context, keys []*pb.RowKey, fieldIDs, attributeKeys []string) ([]*pb.RowFieldValues, []*pb.RowKey, error) {
	if s == nil || s.db == nil {
		return nil, nil, errors.New("pebble store is closed")
	}
	if len(keys) == 0 {
		return nil, nil, invalid("keys are required")
	}
	if len(fieldIDs) == 0 && len(attributeKeys) == 0 {
		return nil, nil, invalid("field_ids or attribute_keys are required")
	}
	if len(keys) > 10000 || (len(keys)*(len(fieldIDs)+len(attributeKeys))) > 100000 {
		return nil, nil, invalid("read request exceeds key/field limit")
	}
	result := make([]*pb.RowFieldValues, 0, len(keys))
	existing := make([]*pb.RowKey, 0, len(keys))
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		resolved := key
		if key != nil {
			if rec := key.GetRecord(); rec != nil && rec.GetVersion() == "" {
				var err error
				resolved, err = s.resolveMaxRecordVersion(key)
				if err != nil {
					return nil, nil, err
				}
			}
		}
		present, err := s.rowExists(resolved)
		if err != nil {
			return nil, nil, err
		}
		if present {
			existing = append(existing, resolved)
		}
		row := &pb.RowFieldValues{Key: resolved}
		for _, id := range fieldIDs {
			physical, err := encodeFieldKey(resolved, id, s.bucketDuration)
			if err != nil {
				return nil, nil, err
			}
			data, closer, err := s.db.Get(physical)
			if errors.Is(err, cpebble.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, nil, err
			}
			value := &pb.TypedValue{}
			err = proto.Unmarshal(data, value)
			_ = closer.Close()
			if err != nil {
				return nil, nil, err
			}
			row.Fields = append(row.Fields, &pb.FieldValue{FieldId: id, Value: value})
		}
		for _, name := range attributeKeys {
			physical, err := encodeAttributeKey(resolved, name, s.bucketDuration)
			if err != nil {
				return nil, nil, err
			}
			data, closer, err := s.db.Get(physical)
			if errors.Is(err, cpebble.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, nil, err
			}
			value := &pb.TypedValue{}
			err = proto.Unmarshal(data, value)
			_ = closer.Close()
			if err != nil {
				return nil, nil, err
			}
			if row.Attributes == nil {
				row.Attributes = make(map[string]*pb.TypedValue)
			}
			row.Attributes[name] = value
		}
		result = append(result, row)
	}
	return result, existing, nil
}

func (s *Store) rowExists(key *pb.RowKey) (bool, error) {
	for _, namespace := range []byte{fieldNamespace, attributeNamespace} {
		prefix, err := encodeNamespacePrefix(namespace, key, s.bucketDuration)
		if err != nil {
			return false, err
		}
		iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: prefix, UpperBound: nextPrefix(prefix)})
		if err != nil {
			return false, err
		}
		found := iter.First()
		iterErr := iter.Error()
		_ = iter.Close()
		if iterErr != nil {
			return false, iterErr
		}
		if found {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) resolveMaxRecordVersion(key *pb.RowKey) (*pb.RowKey, error) {
	// The tuple codec preserves UTF-8 byte order, so the last key under each
	// namespace prefix contains that namespace's largest record version.
	base, err := recordBasePrefix(key)
	if err != nil {
		return nil, err
	}
	max := ""
	for _, namespace := range []byte{fieldNamespace, attributeNamespace} {
		iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: append([]byte{namespace}, base...), UpperBound: nextPrefix(append([]byte{namespace}, base...))})
		if err != nil {
			return nil, err
		}
		if valid := iter.Last(); valid {
			parts, ok := parseRecordValueKey(iter.Key())
			if ok && parts.version > max {
				max = parts.version
			}
		}
		iterErr := iter.Error()
		_ = iter.Close()
		if iterErr != nil {
			return nil, iterErr
		}
	}
	if max == "" {
		return key, nil
	}
	return &pb.RowKey{SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(), Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: key.GetRecord().GetRecordId(), Version: max}}}, nil
}

func recordBasePrefix(key *pb.RowKey) ([]byte, error) {
	record := key.GetRecord()
	if record == nil || record.GetRecordId() == "" {
		return nil, invalid("record key is required")
	}
	out := []byte{recordKind}
	out = appendRawPart(out, []byte(key.GetSpaceId()))
	out = appendRawPart(out, []byte(key.GetDatasetId()))
	out = appendRawPart(out, []byte(record.GetRecordId()))
	return out, nil
}

type recordKeyParts struct{ version string }

func parseRecordValueKey(key []byte) (recordKeyParts, bool) {
	if len(key) < 2 || (key[0] != fieldNamespace && key[0] != attributeNamespace) || key[1] != recordKind {
		return recordKeyParts{}, false
	}
	rest := key[2:]
	for i := 0; i < 3; i++ {
		_, next, err := decodePart(rest)
		if err != nil {
			return recordKeyParts{}, false
		}
		rest = next
	}
	version, _, err := decodePart(rest)
	if err == nil {
		return recordKeyParts{version: version}, true
	}
	return recordKeyParts{}, false
}

func (s *Store) nextOutboxID() (uint64, error) {
	data, closer, err := s.db.Get([]byte(metaNextID))
	if errors.Is(err, cpebble.ErrNotFound) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	defer closer.Close()
	if len(data) != 8 {
		return 0, errors.New("invalid next_outbox_id")
	}
	id := binary.BigEndian.Uint64(data)
	if id == 0 {
		return 1, nil
	}
	return id, nil
}

func (s *Store) setNextOutboxID(batch *cpebble.Batch, id uint64) error {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], id)
	return batch.Set([]byte(metaNextID), data[:], s.writeOptions)
}

func outboxKey(id uint64) string { return fmt.Sprintf("%s%020d", outboxPrefix, id) }

// PrepareOutboxPublication validates and returns the exact persisted event
// bytes. EventMessage has no mutable published_at field, so retries always
// reuse the same stable event_id and payload.
func (s *Store) PrepareOutboxPublication(ctx context.Context, id uint64, now time.Time) ([]byte, error) {
	_ = now
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, errors.New("outbox id is required")
	}
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()
	data, closer, err := s.db.Get([]byte(outboxKey(id)))
	if errors.Is(err, cpebble.ErrNotFound) {
		return nil, fmt.Errorf("outbox entry %d not found", id)
	}
	if err != nil {
		return nil, err
	}
	raw := append([]byte(nil), data...)
	if err := closer.Close(); err != nil {
		return nil, err
	}
	message := &eventpb.EventMessage{}
	if err := proto.Unmarshal(raw, message); err != nil {
		return nil, fmt.Errorf("unmarshal outbox entry %d: %w", id, err)
	}
	if err := validateDatasetRowsUpsertedEvent(message, ""); err != nil {
		return nil, fmt.Errorf("validate outbox event %d: %w", id, err)
	}
	return raw, nil
}

func (s *Store) ListOutbox(ctx context.Context, after uint64, max int) ([]*OutboxEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if max <= 0 {
		max = 100
	}
	iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: []byte(outboxPrefix), UpperBound: []byte(outboxPrefix + "\xff")})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	result := make([]*OutboxEntry, 0, max)
	for valid := iter.First(); valid && len(result) < max; valid = iter.Next() {
		name := strings.TrimPrefix(string(iter.Key()), outboxPrefix)
		var id uint64
		if _, err := fmt.Sscanf(name, "%d", &id); err != nil || id <= after {
			continue
		}
		result = append(result, &OutboxEntry{ID: id, Data: append([]byte(nil), iter.Value()...)})
	}
	return result, iter.Error()
}

// OutboxStats scans the durable outbox so the relay can report the full
// pending count rather than only its publish batch. Timestamp decode errors do
// not change relay behavior; PrepareOutboxPublication remains the authoritative
// validation path for the next attempted entry.
func (s *Store) OutboxStats(ctx context.Context) (OutboxStats, error) {
	if err := ctx.Err(); err != nil {
		return OutboxStats{}, err
	}
	iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: []byte(outboxPrefix), UpperBound: []byte(outboxPrefix + "\xff")})
	if err != nil {
		return OutboxStats{}, err
	}
	defer iter.Close()
	stats := OutboxStats{}
	for valid := iter.First(); valid; valid = iter.Next() {
		if err := ctx.Err(); err != nil {
			return OutboxStats{}, err
		}
		stats.Pending++
		message := &eventpb.EventMessage{}
		if err := proto.Unmarshal(iter.Value(), message); err != nil {
			continue
		}
		var eventAt time.Time
		if occurred := message.GetOccurredAt(); occurred != nil && occurred.CheckValid() == nil {
			eventAt = occurred.AsTime()
		}
		if !eventAt.IsZero() && (stats.OldestEventAt.IsZero() || eventAt.Before(stats.OldestEventAt)) {
			stats.OldestEventAt = eventAt
		}
	}
	return stats, iter.Error()
}

func (s *Store) DeleteOutbox(ctx context.Context, ids []uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	sorted := append([]uint64(nil), ids...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	batch := s.db.NewBatch()
	defer batch.Close()
	for _, id := range sorted {
		if id == 0 {
			continue
		}
		if err := batch.Delete([]byte(outboxKey(id)), s.writeOptions); err != nil {
			return err
		}
	}
	return batch.Commit(s.writeOptions)
}
