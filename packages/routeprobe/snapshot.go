package routeprobe

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"time"
)

const CurrentSnapshotVersion uint32 = 1

var (
	ErrUnsupportedSnapshotVersion = errors.New("routeprobe: unsupported snapshot version")
	ErrExpiredSnapshot            = errors.New("routeprobe: snapshot is expired")
)

type Snapshot struct {
	Version     uint32            `json:"version"`
	Key         RouteKey          `json:"key"`
	GeneratedAt time.Time         `json:"generated_at"`
	ExpiresAt   time.Time         `json:"expires_at"`
	Routes      []ScoredCandidate `json:"routes"`
}

func NewSnapshot(key RouteKey, routes []ScoredCandidate, generatedAt time.Time, ttl time.Duration) (Snapshot, error) {
	if err := key.Validate(); err != nil {
		return Snapshot{}, err
	}
	if generatedAt.IsZero() {
		return Snapshot{}, errors.New("generated_at must not be zero")
	}
	if ttl <= 0 {
		return Snapshot{}, errors.New("snapshot TTL must be positive")
	}
	snapshot := Snapshot{Version: CurrentSnapshotVersion, Key: key, GeneratedAt: generatedAt, ExpiresAt: generatedAt.Add(ttl), Routes: cloneScoredCandidates(routes)}
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func (snapshot Snapshot) Validate() error {
	if snapshot.Version != CurrentSnapshotVersion {
		return fmt.Errorf("%w: got %d, want %d", ErrUnsupportedSnapshotVersion, snapshot.Version, CurrentSnapshotVersion)
	}
	if err := snapshot.Key.Validate(); err != nil {
		return err
	}
	if snapshot.GeneratedAt.IsZero() || snapshot.ExpiresAt.IsZero() || !snapshot.ExpiresAt.After(snapshot.GeneratedAt) {
		return errors.New("snapshot generated_at/expires_at interval is invalid")
	}
	seen := make(map[string]struct{}, len(snapshot.Routes))
	for index, route := range snapshot.Routes {
		if err := route.Candidate.Validate(); err != nil {
			return fmt.Errorf("route %d: %w", index, err)
		}
		if route.Candidate.RouteKey() != snapshot.Key {
			return fmt.Errorf("route %d has key %q, want %q", index, route.Candidate.RouteKey(), snapshot.Key)
		}
		if route.Stats.Attempts < 0 || route.Stats.Successes < 0 || route.Stats.Failures < 0 || route.Stats.Successes > route.Stats.Attempts || route.Stats.Failures > route.Stats.Attempts || route.Stats.Successes+route.Stats.Failures != route.Stats.Attempts || route.Stats.RemoteErrors < 0 || route.Stats.RemoteErrors > route.Stats.Failures {
			return fmt.Errorf("route %d has invalid probe counters", index)
		}
		if route.Healthy && (route.Stats.Attempts == 0 || route.Stats.Successes == 0) {
			return fmt.Errorf("route %d is marked healthy without a successful probe", index)
		}
		if math.IsNaN(route.Score) || math.IsInf(route.Score, 0) {
			return fmt.Errorf("route %d has a non-finite score", index)
		}
		identity := route.Candidate.Identity()
		if _, exists := seen[identity]; exists {
			return fmt.Errorf("duplicate route candidate %q", identity)
		}
		seen[identity] = struct{}{}
	}
	return nil
}

func (snapshot Snapshot) FreshAt(now time.Time) bool {
	return !snapshot.GeneratedAt.IsZero() && !snapshot.ExpiresAt.IsZero() && !now.Before(snapshot.GeneratedAt) && now.Before(snapshot.ExpiresAt)
}

func (snapshot Snapshot) ExpiredAt(now time.Time) bool {
	return !snapshot.FreshAt(now) && !now.Before(snapshot.ExpiresAt)
}

func MarshalSnapshot(snapshot Snapshot) ([]byte, error) {
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(snapshot)
}

func UnmarshalSnapshot(data []byte) (Snapshot, error) {
	var snapshot Snapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("decode route snapshot: %w", err)
	}
	if snapshot.Version != CurrentSnapshotVersion {
		return Snapshot{}, fmt.Errorf("%w: got %d, want %d", ErrUnsupportedSnapshotVersion, snapshot.Version, CurrentSnapshotVersion)
	}
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Snapshot{}, errors.New("decode route snapshot: multiple JSON values")
		}
		return Snapshot{}, fmt.Errorf("decode route snapshot: trailing data: %w", err)
	}
	return snapshot, nil
}

func cloneScoredCandidates(routes []ScoredCandidate) []ScoredCandidate {
	if len(routes) == 0 {
		return nil
	}
	output := make([]ScoredCandidate, len(routes))
	for index, route := range routes {
		output[index] = route
		output[index].Candidate.Metadata = cloneStringMap(route.Candidate.Metadata)
		output[index].Stats.Latencies = append([]time.Duration(nil), route.Stats.Latencies...)
	}
	return output
}

// SnapshotStore is a process-local snapshot cache. Persistence, if needed by
// a caller, is provided through MarshalSnapshot/UnmarshalSnapshot so the
// package does not impose a storage backend.
type SnapshotStore struct {
	mu    sync.RWMutex
	clock func() time.Time
	items map[string]Snapshot
}

func NewSnapshotStore(clock func() time.Time) *SnapshotStore {
	if clock == nil {
		clock = time.Now
	}
	return &SnapshotStore{clock: clock, items: make(map[string]Snapshot)}
}

func (store *SnapshotStore) Put(snapshot Snapshot) error {
	if store == nil {
		return errors.New("routeprobe: nil snapshot store")
	}
	if err := snapshot.Validate(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.items == nil {
		store.items = make(map[string]Snapshot)
	}
	store.items[snapshot.Key.String()] = Snapshot{Version: snapshot.Version, Key: snapshot.Key, GeneratedAt: snapshot.GeneratedAt, ExpiresAt: snapshot.ExpiresAt, Routes: cloneScoredCandidates(snapshot.Routes)}
	return nil
}

func (store *SnapshotStore) Get(key RouteKey) (Snapshot, bool) {
	if store == nil {
		return Snapshot{}, false
	}
	store.mu.RLock()
	snapshot, ok := store.items[key.String()]
	clock := store.clock
	store.mu.RUnlock()
	if !ok || clock == nil || !snapshot.FreshAt(clock()) {
		return Snapshot{}, false
	}
	snapshot.Routes = cloneScoredCandidates(snapshot.Routes)
	return snapshot, true
}
