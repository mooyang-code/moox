package routeprobe

import (
	"testing"
	"time"
)

func TestRankCandidatesCombinesSuccessRateLatencyAndRemoteErrorPenalty(t *testing.T) {
	fast := Candidate{SCFRegion: "ap-shanghai", SourceKey: SourceKey{ProviderID: "tdx", SourceID: "normal_7709"}, Transport: TransportTCP, Host: "quotes.example", Address: "192.0.2.10", Port: 7709}
	slow := Candidate{SCFRegion: "ap-shanghai", SourceKey: fast.SourceKey, Transport: TransportTCP, Host: "quotes.example", Address: "192.0.2.11", Port: 7709}
	results := []ProbeResult{
		{Candidate: fast, Success: true, Latency: 20 * time.Millisecond},
		{Candidate: fast, Success: true, Latency: 25 * time.Millisecond},
		{Candidate: fast, Success: false, RemoteError: true, ErrorKind: ErrorRemote},
		{Candidate: slow, Success: true, Latency: 150 * time.Millisecond},
		{Candidate: slow, Success: true, Latency: 160 * time.Millisecond},
		{Candidate: slow, Success: true, Latency: 155 * time.Millisecond},
	}
	options := ScoreOptions{EWMAAlpha: 0.5, FailurePenalty: 100 * time.Millisecond, RemoteErrorPenalty: 100 * time.Millisecond}
	stats := BuildRouteStats(results, options)
	ranked := RankCandidates([]Candidate{slow, fast}, stats, options)
	if len(ranked) != 2 || !ranked[0].Candidate.Equal(fast) {
		t.Fatalf("ranked candidates = %+v, want fast route first", ranked)
	}
	if ranked[0].Stats.SuccessRate() <= 0.5 || ranked[0].Stats.P95 <= 0 {
		t.Fatalf("fast route stats were not recorded: %+v", ranked[0].Stats)
	}
}

func TestRankCandidatesUsesStableIdentityAsTieBreak(t *testing.T) {
	first := Candidate{SCFRegion: "ap-shanghai", SourceKey: SourceKey{ProviderID: "tdx", SourceID: "normal_7709"}, Transport: TransportTCP, Host: "quotes.example", Address: "192.0.2.10", Port: 7709}
	second := first
	second.Address = "192.0.2.11"
	results := []ProbeResult{{Candidate: first, Success: true, Latency: time.Second}, {Candidate: second, Success: true, Latency: time.Second}}
	ranked := RankCandidates([]Candidate{second, first}, BuildRouteStats(results, ScoreOptions{}), ScoreOptions{})
	if len(ranked) != 2 || ranked[0].Candidate.Identity() >= ranked[1].Candidate.Identity() {
		t.Fatalf("ranked candidates did not use stable identity tie-break: %+v", ranked)
	}
}
