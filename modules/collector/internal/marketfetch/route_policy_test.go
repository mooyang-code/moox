package marketfetch

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/routeprobe"
)

func TestRoutePolicyUsesSourceAndTransportIsolatedSnapshots(t *testing.T) {
	now := time.Now()
	key := routeprobe.SourceKey{ProviderID: "tdx", SourceID: "normal_7709"}
	routeKey := routeprobe.RouteKey{SCFRegion: "ap-shanghai", SourceKey: key, Transport: routeprobe.TransportTCP, Host: "quotes.example", Port: 7709}
	candidate := routeprobe.Candidate{SCFRegion: routeKey.SCFRegion, SourceKey: key, Transport: routeKey.Transport, Host: routeKey.Host, Address: "192.0.2.10", Port: routeKey.Port}
	snapshot, err := routeprobe.NewSnapshot(routeKey, []routeprobe.ScoredCandidate{{Candidate: candidate, Stats: routeprobe.RouteStats{Attempts: 1, Successes: 1}, Healthy: true, Status: routeprobe.StatusHealthy, Score: 1}}, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	store := routeprobe.NewSnapshotStore(func() time.Time { return now.Add(time.Second) })
	if err := store.Put(snapshot); err != nil {
		t.Fatal(err)
	}
	policy := RoutePolicy{Store: store, Resolver: routeprobe.DNSResolver{LookupHost: func(context.Context, string) ([]string, error) { return []string{"192.0.2.10"}, nil }}, Selector: routeprobe.RouteSelector{MaxFallback: 1}}
	selection, err := policy.Select(context.Background(), routeprobe.ResolveRequest{SCFRegion: routeKey.SCFRegion, SourceKey: key, Transport: routeKey.Transport, Host: routeKey.Host, Port: routeKey.Port})
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Routes) != 1 || selection.Routes[0].Address != candidate.Address {
		t.Fatalf("unexpected selected route: %+v", selection.Routes)
	}
}

func TestRoutePolicyDoesNotPersistSnapshotWithoutHealthyCandidate(t *testing.T) {
	policy := RoutePolicy{
		SCFRegion: "ap-shanghai",
		Store:     routeprobe.NewSnapshotStore(nil),
		Resolver: routeprobe.DNSResolver{LookupHost: func(context.Context, string) ([]string, error) {
			return []string{"192.0.2.11"}, nil
		}},
	}
	_, err := policy.ProbeAndStore(context.Background(), routeprobe.ResolveRequest{SCFRegion: "ap-shanghai", SourceKey: routeprobe.SourceKey{ProviderID: "tdx", SourceID: "normal_7709"}, Transport: routeprobe.TransportTCP, Host: "quotes.example", Port: 7709}, routeprobe.ProbeFunc(func(_ context.Context, request routeprobe.ProbeRequest) (routeprobe.ProbeResult, error) {
		return routeprobe.ProbeResult{Candidate: request.Candidate, Attempt: request.Attempt, ObservedAt: time.Now()}, nil
	}))
	if err != routeprobe.ErrNoHealthyRoute {
		t.Fatalf("ProbeAndStore() error = %v, want ErrNoHealthyRoute", err)
	}
}
