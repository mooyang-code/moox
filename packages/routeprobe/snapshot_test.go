package routeprobe

import (
	"errors"
	"testing"
	"time"
)

func TestSnapshotRoundTripHonorsTTLAndRouteKeyIsolation(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	key := RouteKey{SCFRegion: "ap-shanghai", SourceKey: SourceKey{ProviderID: "binance", SourceID: "spot_http"}, Transport: TransportHTTPS, Host: "api.example", Port: 443}
	candidate := Candidate{SCFRegion: key.SCFRegion, SourceKey: key.SourceKey, Transport: key.Transport, Host: key.Host, Address: "192.0.2.10", Port: key.Port}
	snapshot, err := NewSnapshot(key, []ScoredCandidate{{Candidate: candidate, Stats: RouteStats{Attempts: 1, Successes: 1}, Healthy: true, Score: 20}}, now, time.Minute)
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}
	encoded, err := MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatalf("MarshalSnapshot() error = %v", err)
	}
	decoded, err := UnmarshalSnapshot(encoded)
	if err != nil {
		t.Fatalf("UnmarshalSnapshot() error = %v", err)
	}
	if !decoded.FreshAt(now.Add(30*time.Second)) || decoded.FreshAt(now.Add(time.Minute)) {
		t.Fatalf("snapshot TTL is incorrect: generated=%s expires=%s", decoded.GeneratedAt, decoded.ExpiresAt)
	}
	if _, err := NewSnapshot(RouteKey{SCFRegion: "ap-beijing", SourceKey: key.SourceKey, Transport: key.Transport, Host: key.Host, Port: key.Port}, decoded.Routes, now, time.Minute); err == nil {
		t.Fatal("NewSnapshot() accepted a candidate set with a mismatched route key")
	}
}

func TestSnapshotRejectsUnsupportedVersion(t *testing.T) {
	_, err := UnmarshalSnapshot([]byte(`{"version":99}`))
	if !errors.Is(err, ErrUnsupportedSnapshotVersion) {
		t.Fatalf("UnmarshalSnapshot() error = %v, want unsupported version", err)
	}
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	key := RouteKey{SCFRegion: "ap-shanghai", SourceKey: SourceKey{ProviderID: "binance", SourceID: "spot_http"}, Transport: TransportHTTPS, Host: "api.example", Port: 443}
	snapshot, err := NewSnapshot(key, nil, now, time.Hour)
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}
	encoded, err := MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatalf("MarshalSnapshot() error = %v", err)
	}
	if _, err := UnmarshalSnapshot(append(encoded, []byte(` {}`)...)); err == nil {
		t.Fatal("UnmarshalSnapshot() accepted trailing JSON")
	}
}

func TestSelectorReturnsPrimaryAndOnlyFiniteHealthyFallbacks(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	key := RouteKey{SCFRegion: "ap-shanghai", SourceKey: SourceKey{ProviderID: "tdx", SourceID: "normal_7709"}, Transport: TransportTCP, Host: "quotes.example", Port: 7709}
	routes := make([]ScoredCandidate, 0, 4)
	for i, healthy := range []bool{true, true, false, true} {
		stats := RouteStats{}
		if healthy {
			stats = RouteStats{Attempts: 1, Successes: 1}
		}
		routes = append(routes, ScoredCandidate{Candidate: Candidate{SCFRegion: key.SCFRegion, SourceKey: key.SourceKey, Transport: key.Transport, Host: key.Host, Address: "192.0.2." + string(rune('1'+i)), Port: key.Port}, Stats: stats, Healthy: healthy, Score: float64(i)})
	}
	snapshot, err := NewSnapshot(key, routes, now, time.Hour)
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}
	selection, err := (RouteSelector{Clock: func() time.Time { return now }, MaxFallback: 1}).Select(SelectionRequest{Key: key, Snapshot: &snapshot})
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if len(selection.Routes) != 2 || selection.Routes[0].Address != "192.0.2.1" || selection.Routes[1].Address != "192.0.2.2" {
		t.Fatalf("selected routes = %+v, want primary plus one healthy fallback", selection.Routes)
	}
	if selection.Status != StatusHealthy {
		t.Fatalf("selection status = %s, want healthy", selection.Status)
	}
}

func TestSelectorTreatsExpiredSnapshotAsUnavailableWithoutUnprobedFallback(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	key := RouteKey{SCFRegion: "ap-shanghai", SourceKey: SourceKey{ProviderID: "tdx", SourceID: "normal_7709"}, Transport: TransportTCP, Host: "quotes.example", Port: 7709}
	candidate := Candidate{SCFRegion: key.SCFRegion, SourceKey: key.SourceKey, Transport: key.Transport, Host: key.Host, Address: "192.0.2.10", Port: key.Port}
	snapshot, err := NewSnapshot(key, []ScoredCandidate{{Candidate: candidate, Stats: RouteStats{Attempts: 1, Successes: 1}, Healthy: true}}, now, time.Minute)
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}
	_, err = (RouteSelector{Clock: func() time.Time { return now.Add(2 * time.Minute) }, MaxFallback: 2}).Select(SelectionRequest{Key: key, Snapshot: &snapshot, Candidates: []ScoredCandidate{{Candidate: candidate, Healthy: false}}})
	if !errors.Is(err, ErrNoHealthyRoute) {
		t.Fatalf("Select() error = %v, want no healthy route", err)
	}
}

func TestSelectorDoesNotPromoteAnUnprobedCandidate(t *testing.T) {
	key := RouteKey{SCFRegion: "ap-shanghai", SourceKey: SourceKey{ProviderID: "tdx", SourceID: "normal_7709"}, Transport: TransportTCP, Host: "quotes.example", Port: 7709}
	candidate := Candidate{SCFRegion: key.SCFRegion, SourceKey: key.SourceKey, Transport: key.Transport, Host: key.Host, Address: "192.0.2.10", Port: key.Port}
	_, err := (RouteSelector{MaxFallback: 1}).Select(SelectionRequest{Key: key, Candidates: []ScoredCandidate{{Candidate: candidate, Healthy: true, Score: 1}}})
	if !errors.Is(err, ErrNoHealthyRoute) {
		t.Fatalf("Select() error = %v, want no healthy route for unprobed candidate", err)
	}
}

func TestSnapshotStoreHonorsTTLAndReturnsDefensiveCopies(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	key := RouteKey{SCFRegion: "ap-shanghai", SourceKey: SourceKey{ProviderID: "binance", SourceID: "spot_http"}, Transport: TransportHTTPS, Host: "api.example", Port: 443}
	candidate := Candidate{SCFRegion: key.SCFRegion, SourceKey: key.SourceKey, Transport: key.Transport, Host: key.Host, Address: "192.0.2.10", Port: key.Port, Metadata: map[string]string{"role": "primary"}}
	snapshot, err := NewSnapshot(key, []ScoredCandidate{{Candidate: candidate, Stats: RouteStats{Attempts: 1, Successes: 1}, Healthy: true}}, now, time.Minute)
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}
	clockNow := now
	store := NewSnapshotStore(func() time.Time { return clockNow })
	if err := store.Put(snapshot); err != nil {
		t.Fatalf("SnapshotStore.Put() error = %v", err)
	}
	got, ok := store.Get(key)
	if !ok {
		t.Fatal("SnapshotStore.Get() did not return a fresh snapshot")
	}
	got.Routes[0].Candidate.Metadata["role"] = "mutated"
	gotAgain, ok := store.Get(key)
	if !ok || gotAgain.Routes[0].Candidate.Metadata["role"] != "primary" {
		t.Fatalf("SnapshotStore.Get() leaked mutable state: %+v", gotAgain)
	}
	clockNow = now.Add(time.Minute)
	if _, ok := store.Get(key); ok {
		t.Fatal("SnapshotStore.Get() returned an expired snapshot")
	}
	otherKey := key
	otherKey.SCFRegion = "ap-beijing"
	if _, ok := store.Get(otherKey); ok {
		t.Fatal("SnapshotStore.Get() crossed SCF region isolation")
	}
}
