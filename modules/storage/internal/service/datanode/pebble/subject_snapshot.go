package pebble

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	cpebble "github.com/cockroachdb/pebble"
)

const (
	subjectSnapshotPrefix = "__subject_snapshot/"
	subjectSnapshotIndex  = "__subject_snapshot_index/"
	subjectPeriodPrefix   = "__subject_period/"
)

type SubjectSnapshotScope struct {
	SpaceID   string
	DatasetID string
	Frequency string
}

type SubjectSnapshot struct {
	SnapshotID    string               `json:"snapshot_id"`
	Scope         SubjectSnapshotScope `json:"scope"`
	EffectiveTime int64                `json:"effective_time"`
	SubjectIDs    []string             `json:"subject_ids"`
}

type PeriodSubjects struct {
	Scope      SubjectSnapshotScope
	PeriodTime int64
	SnapshotID string
	SubjectIDs []string
	Frozen     bool
}

type subjectSnapshotIndexValue struct {
	SnapshotID string `json:"snapshot_id"`
}

func (s *Store) PutSubjectSnapshot(ctx context.Context, scope SubjectSnapshotScope, effectiveTime int64, subjectIDs []string) (*SubjectSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateSubjectSnapshotScope(scope); err != nil {
		return nil, err
	}
	ids, err := normalizeSubjectIDs(subjectIDs)
	if err != nil {
		return nil, err
	}
	value := SubjectSnapshot{Scope: scope, EffectiveTime: effectiveTime, SubjectIDs: ids}
	value.SnapshotID = subjectSnapshotID(scope, effectiveTime, ids)
	indexKey := subjectSnapshotEffectiveKey(scope, effectiveTime)
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()
	if existing, found, err := s.getSubjectSnapshotByEffectiveKey(indexKey); err != nil {
		return nil, err
	} else if found {
		if existing.SnapshotID != value.SnapshotID {
			return nil, fmt.Errorf("subject snapshot already exists at effective time %d", effectiveTime)
		}
		return existing, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode subject snapshot: %w", err)
	}
	indexValue, err := json.Marshal(subjectSnapshotIndexValue{SnapshotID: value.SnapshotID})
	if err != nil {
		return nil, fmt.Errorf("encode subject snapshot index: %w", err)
	}
	batch := s.db.NewBatch()
	defer batch.Close()
	if err := batch.Set(subjectSnapshotRecordKey(value.SnapshotID), encoded, s.writeOptions); err != nil {
		return nil, err
	}
	if err := batch.Set(indexKey, indexValue, s.writeOptions); err != nil {
		return nil, err
	}
	if err := batch.Commit(s.writeOptions); err != nil {
		return nil, err
	}
	return cloneSubjectSnapshot(&value), nil
}

// GetPeriodSubjects resolves the latest snapshot effective at or before the
// requested period. It is read-only; callers freeze the result when work starts.
func (s *Store) GetPeriodSubjects(ctx context.Context, scope SubjectSnapshotScope, periodTime int64) (*PeriodSubjects, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateSubjectSnapshotScope(scope); err != nil {
		return nil, err
	}
	if period, found, err := s.getFrozenPeriodSubjects(scope, periodTime); err != nil {
		return nil, err
	} else if found {
		return period, nil
	}
	snapshot, found, err := s.latestSubjectSnapshot(scope, periodTime)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("no subject snapshot is effective for period")
	}
	return &PeriodSubjects{Scope: scope, PeriodTime: periodTime, SnapshotID: snapshot.SnapshotID, SubjectIDs: append([]string(nil), snapshot.SubjectIDs...)}, nil
}

// FreezePeriodSubjects persists the resolved snapshot reference exactly once.
// Later snapshot updates cannot change a period that has already started.
func (s *Store) FreezePeriodSubjects(ctx context.Context, scope SubjectSnapshotScope, periodTime int64) (*PeriodSubjects, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateSubjectSnapshotScope(scope); err != nil {
		return nil, err
	}
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()
	if period, found, err := s.getFrozenPeriodSubjects(scope, periodTime); err != nil {
		return nil, err
	} else if found {
		return period, nil
	}
	snapshot, found, err := s.latestSubjectSnapshot(scope, periodTime)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("no subject snapshot is effective for period")
	}
	key := subjectPeriodKey(scope, periodTime)
	value, err := json.Marshal(subjectSnapshotIndexValue{SnapshotID: snapshot.SnapshotID})
	if err != nil {
		return nil, fmt.Errorf("encode frozen subject snapshot reference: %w", err)
	}
	if err := s.db.Set(key, value, s.writeOptions); err != nil {
		return nil, err
	}
	return &PeriodSubjects{Scope: scope, PeriodTime: periodTime, SnapshotID: snapshot.SnapshotID, SubjectIDs: append([]string(nil), snapshot.SubjectIDs...), Frozen: true}, nil
}

func (s *Store) latestSubjectSnapshot(scope SubjectSnapshotScope, periodTime int64) (*SubjectSnapshot, bool, error) {
	prefix := []byte(subjectSnapshotIndexPrefix(scope))
	iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: prefix, UpperBound: nextPrefix(prefix)})
	if err != nil {
		return nil, false, err
	}
	defer iter.Close()
	target := subjectSnapshotEffectiveKey(scope, periodTime)
	if iter.SeekGE(target) {
		if !strings.HasPrefix(string(iter.Key()), string(prefix)) {
			if !iter.Prev() {
				return nil, false, iter.Error()
			}
		} else if !equalBytes(iter.Key(), target) && !iter.Prev() {
			return nil, false, iter.Error()
		}
	} else if !iter.Last() {
		return nil, false, iter.Error()
	}
	if err := iter.Error(); err != nil {
		return nil, false, err
	}
	if !strings.HasPrefix(string(iter.Key()), string(prefix)) {
		return nil, false, nil
	}
	var index subjectSnapshotIndexValue
	if err := json.Unmarshal(iter.Value(), &index); err != nil {
		return nil, false, fmt.Errorf("decode subject snapshot effective index: %w", err)
	}
	return s.getSubjectSnapshot(index.SnapshotID)
}

func (s *Store) getFrozenPeriodSubjects(scope SubjectSnapshotScope, periodTime int64) (*PeriodSubjects, bool, error) {
	value, closer, err := s.db.Get(subjectPeriodKey(scope, periodTime))
	if errors.Is(err, cpebble.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	raw := append([]byte(nil), value...)
	if err := closer.Close(); err != nil {
		return nil, false, err
	}
	var ref subjectSnapshotIndexValue
	if err := json.Unmarshal(raw, &ref); err != nil {
		return nil, false, fmt.Errorf("decode frozen subject snapshot reference: %w", err)
	}
	snapshot, found, err := s.getSubjectSnapshot(ref.SnapshotID)
	if err != nil || !found {
		if err == nil {
			err = errors.New("frozen subject snapshot reference is dangling")
		}
		return nil, false, err
	}
	return &PeriodSubjects{Scope: scope, PeriodTime: periodTime, SnapshotID: snapshot.SnapshotID, SubjectIDs: append([]string(nil), snapshot.SubjectIDs...), Frozen: true}, true, nil
}

func (s *Store) getSubjectSnapshot(snapshotID string) (*SubjectSnapshot, bool, error) {
	value, closer, err := s.db.Get(subjectSnapshotRecordKey(snapshotID))
	if errors.Is(err, cpebble.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	raw := append([]byte(nil), value...)
	if err := closer.Close(); err != nil {
		return nil, false, err
	}
	var snapshot SubjectSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, false, fmt.Errorf("decode subject snapshot: %w", err)
	}
	return &snapshot, true, nil
}

func (s *Store) getSubjectSnapshotByEffectiveKey(key []byte) (*SubjectSnapshot, bool, error) {
	value, closer, err := s.db.Get(key)
	if errors.Is(err, cpebble.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	raw := append([]byte(nil), value...)
	if err := closer.Close(); err != nil {
		return nil, false, err
	}
	var index subjectSnapshotIndexValue
	if err := json.Unmarshal(raw, &index); err != nil {
		return nil, false, fmt.Errorf("decode subject snapshot index: %w", err)
	}
	return s.getSubjectSnapshot(index.SnapshotID)
}

func validateSubjectSnapshotScope(scope SubjectSnapshotScope) error {
	if strings.TrimSpace(scope.SpaceID) == "" || strings.TrimSpace(scope.DatasetID) == "" || strings.TrimSpace(scope.Frequency) == "" {
		return invalid("subject snapshot space, dataset and frequency are required")
	}
	if scope.SpaceID != strings.TrimSpace(scope.SpaceID) || scope.DatasetID != strings.TrimSpace(scope.DatasetID) || scope.Frequency != strings.TrimSpace(scope.Frequency) {
		return invalid("subject snapshot scope must be normalized")
	}
	return nil
}

func normalizeSubjectIDs(subjectIDs []string) ([]string, error) {
	set := make(map[string]struct{}, len(subjectIDs))
	for _, subjectID := range subjectIDs {
		subjectID = strings.TrimSpace(subjectID)
		if subjectID == "" {
			return nil, invalid("subject snapshot contains an empty subject_id")
		}
		set[subjectID] = struct{}{}
	}
	ids := make([]string, 0, len(set))
	for subjectID := range set {
		ids = append(ids, subjectID)
	}
	sort.Strings(ids)
	return ids, nil
}

func subjectSnapshotID(scope SubjectSnapshotScope, effectiveTime int64, subjectIDs []string) string {
	hash := sha256.Sum256([]byte(strings.Join([]string{scope.SpaceID, scope.DatasetID, scope.Frequency, fmt.Sprint(effectiveTime), strings.Join(subjectIDs, "\x00")}, "\x00")))
	return hex.EncodeToString(hash[:])
}

func subjectSnapshotScopeKey(scope SubjectSnapshotScope) string {
	hash := sha256.Sum256([]byte(strings.Join([]string{scope.SpaceID, scope.DatasetID, scope.Frequency}, "\x00")))
	return hex.EncodeToString(hash[:])
}

func subjectSnapshotIndexPrefix(scope SubjectSnapshotScope) string {
	return subjectSnapshotIndex + subjectSnapshotScopeKey(scope) + "/"
}

func subjectSnapshotEffectiveKey(scope SubjectSnapshotScope, effectiveTime int64) []byte {
	key := []byte(subjectSnapshotIndexPrefix(scope))
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(effectiveTime)^(uint64(1)<<63))
	return append(key, encoded[:]...)
}

func subjectSnapshotRecordKey(snapshotID string) []byte {
	return []byte(subjectSnapshotPrefix + snapshotID)
}

func subjectPeriodKey(scope SubjectSnapshotScope, periodTime int64) []byte {
	hash := sha256.Sum256([]byte(strings.Join([]string{scope.SpaceID, scope.DatasetID, scope.Frequency, fmt.Sprint(periodTime)}, "\x00")))
	return []byte(subjectPeriodPrefix + hex.EncodeToString(hash[:]))
}

func cloneSubjectSnapshot(snapshot *SubjectSnapshot) *SubjectSnapshot {
	if snapshot == nil {
		return nil
	}
	copy := *snapshot
	copy.SubjectIDs = append([]string(nil), snapshot.SubjectIDs...)
	return &copy
}

func equalBytes(left, right []byte) bool {
	return bytes.Equal(left, right)
}
