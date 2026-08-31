package marketfetch

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/routeprobe"
)

func TestSelectRouteRanksProtocolProbeResults(t *testing.T) {
	key := routeprobe.SourceKey{ProviderID: "tdx", SourceID: "normal_7709"}
	clock := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	selection, err := SelectRoute(context.Background(), RouteSelectionOptions{
		SCFRegion: "ap-shanghai", SourceKey: key, Transport: routeprobe.TransportTCP,
		Host: "quotes.example", Port: 7709, Addresses: []string{"192.0.2.10", "192.0.2.11"},
		Clock: clockNow(clock), Prober: routeprobe.ProbeFunc(func(_ context.Context, request routeprobe.ProbeRequest) (routeprobe.ProbeResult, error) {
			latency := 20 * time.Millisecond
			if request.Candidate.Address == "192.0.2.11" {
				latency = 5 * time.Millisecond
			}
			return routeprobe.ProbeResult{Success: true, Latency: latency, FirstResponseLatency: latency, ObservedAt: clock}, nil
		}),
		Probe: routeprobe.ProbeOptions{Concurrency: 2, Attempts: 1, Clock: clockNow(clock)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Routes) != 1 || selection.Routes[0].Address != "192.0.2.11" {
		t.Fatalf("selected routes = %+v, want fastest protocol-proven route", selection.Routes)
	}
}

func TestSelectRouteUsesFreshSnapshotWithoutProbing(t *testing.T) {
	key := routeprobe.SourceKey{ProviderID: "binance", SourceID: "spot_http"}
	clock := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	candidate := routeprobe.Candidate{SCFRegion: "ap-shanghai", SourceKey: key, Transport: routeprobe.TransportHTTPS, Host: "api.example", Address: "192.0.2.20", Port: 443}
	snapshot, err := routeprobe.NewSnapshot(routeprobe.RouteKey{SCFRegion: "ap-shanghai", SourceKey: key, Transport: routeprobe.TransportHTTPS, Host: "api.example", Port: 443}, []routeprobe.ScoredCandidate{{Candidate: candidate, Stats: routeprobe.RouteStats{Attempts: 1, Successes: 1}, Score: 1, Healthy: true, Status: routeprobe.StatusHealthy}}, clock, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	selection, err := SelectRoute(context.Background(), RouteSelectionOptions{
		SCFRegion: "ap-shanghai", SourceKey: key, Transport: routeprobe.TransportHTTPS,
		Host: "api.example", Port: 443, Addresses: []string{"192.0.2.20"}, Snapshot: &snapshot,
		Clock: clockNow(clock.Add(time.Second)), Prober: routeprobe.ProbeFunc(func(context.Context, routeprobe.ProbeRequest) (routeprobe.ProbeResult, error) {
			called = true
			return routeprobe.ProbeResult{}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("fresh route snapshot should not invoke protocol prober")
	}
	if len(selection.Routes) != 1 || selection.Routes[0].Address != candidate.Address {
		t.Fatalf("selected routes = %+v", selection.Routes)
	}
}

func clockNow(now time.Time) func() time.Time {
	return func() time.Time { return now }
}
