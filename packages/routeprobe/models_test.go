package routeprobe

import (
	"testing"
	"time"
)

func TestSourceKeyKeepsSourceScopedIdentityAndCandidateDeduplication(t *testing.T) {
	key := SourceKey{ProviderID: "tdx", SourceID: "normal_7709"}
	if got, want := key.String(), "tdx/normal_7709"; got != want {
		t.Fatalf("SourceKey.String() = %q, want %q", got, want)
	}
	if err := key.Validate(); err != nil {
		t.Fatalf("SourceKey.Validate() error = %v", err)
	}

	candidates := []Candidate{
		{SCFRegion: "ap-shanghai", SourceKey: key, Transport: TransportTCP, Host: "quotes.example", Address: "192.0.2.2", Port: 7709},
		{SCFRegion: "ap-shanghai", SourceKey: key, Transport: TransportTCP, Host: "quotes.example", Address: "192.0.2.2", Port: 7709, HintLatency: 10 * time.Millisecond},
		{SCFRegion: "ap-shanghai", SourceKey: SourceKey{ProviderID: "tdx", SourceID: "ex_classic_7727"}, Transport: TransportTCP, Host: "quotes.example", Address: "192.0.2.3", Port: 7709},
	}
	got, err := NormalizeCandidates(candidates)
	if err != nil {
		t.Fatalf("NormalizeCandidates() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("NormalizeCandidates() returned %d candidates, want 2", len(got))
	}
	if got[0].HintLatency != 10*time.Millisecond {
		t.Fatalf("duplicate candidate did not retain useful hint: %s", got[0].HintLatency)
	}
	if got[0].SourceKey == got[1].SourceKey {
		t.Fatal("different SourceIDs unexpectedly collapsed into one route")
	}
}

func TestCandidateRejectsInvalidRouteIdentity(t *testing.T) {
	_, err := NormalizeCandidates([]Candidate{{
		SCFRegion: "ap-shanghai", SourceKey: SourceKey{ProviderID: "tdx", SourceID: "normal_7709"},
		Transport: TransportTCP, Host: "quotes.example", Address: "192.0.2.2", Port: 0,
	}})
	if err == nil {
		t.Fatal("NormalizeCandidates() accepted an invalid port")
	}
}
