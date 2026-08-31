package marketfetch

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/packages/routeprobe"
)

// RoutePolicy is the only Collector-side coordinator for route snapshots.
// It chooses protocol-proven endpoints and never implements request quotas,
// cooldowns, or a DNS server.
type RoutePolicy struct {
	SCFRegion    string
	Resolver     routeprobe.DNSResolver
	Store        *routeprobe.SnapshotStore
	Selector     routeprobe.RouteSelector
	ProbeOptions routeprobe.ProbeOptions
	ScoreOptions routeprobe.ScoreOptions
	SnapshotTTL  time.Duration
}

func (policy *RoutePolicy) SelectRouteIPs(ctx context.Context, sourceKey routeprobe.SourceKey, transport routeprobe.Transport, host string, port int) ([]string, error) {
	if policy == nil {
		return nil, fmt.Errorf("route policy is not initialized")
	}
	selection, err := policy.Select(ctx, routeprobe.ResolveRequest{SCFRegion: policy.SCFRegion, SourceKey: sourceKey, Transport: transport, Host: host, Port: port})
	if err != nil {
		return nil, err
	}
	ips := make([]string, 0, len(selection.Routes))
	for _, route := range selection.Routes {
		ips = append(ips, route.Address)
	}
	return ips, nil
}

func (policy *RoutePolicy) ProbeAndStore(ctx context.Context, request routeprobe.ResolveRequest, prober routeprobe.Prober) (routeprobe.Snapshot, error) {
	if policy == nil || policy.Store == nil {
		return routeprobe.Snapshot{}, fmt.Errorf("route policy is not initialized")
	}
	candidates, err := policy.Resolver.Resolve(ctx, request)
	if err != nil {
		return routeprobe.Snapshot{}, err
	}
	results, err := routeprobe.ProbeCandidates(ctx, candidates, prober, policy.ProbeOptions)
	if err != nil {
		return routeprobe.Snapshot{}, err
	}
	stats := routeprobe.BuildRouteStats(results, policy.ScoreOptions)
	ranked := routeprobe.RankCandidates(candidates, stats, policy.ScoreOptions)
	key := requestKey(request)
	routes := make([]routeprobe.ScoredCandidate, 0, len(ranked))
	for _, candidate := range ranked {
		if candidate.Candidate.RouteKey() == key {
			routes = append(routes, candidate)
		}
	}
	if len(routes) == 0 {
		return routeprobe.Snapshot{}, routeprobe.ErrNoHealthyRoute
	}
	healthy := false
	for _, route := range routes {
		if route.Healthy {
			healthy = true
			break
		}
	}
	if !healthy {
		return routeprobe.Snapshot{}, routeprobe.ErrNoHealthyRoute
	}
	ttl := policy.SnapshotTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	snapshot, err := routeprobe.NewSnapshot(key, routes, time.Now(), ttl)
	if err != nil {
		return routeprobe.Snapshot{}, err
	}
	if err := policy.Store.Put(snapshot); err != nil {
		return routeprobe.Snapshot{}, err
	}
	return snapshot, nil
}

func (policy *RoutePolicy) Select(ctx context.Context, request routeprobe.ResolveRequest) (routeprobe.RouteSelection, error) {
	if policy == nil || policy.Store == nil {
		return routeprobe.RouteSelection{}, fmt.Errorf("route policy is not initialized")
	}
	candidates, err := policy.Resolver.Resolve(ctx, request)
	if err != nil {
		return routeprobe.RouteSelection{}, err
	}
	key := requestKey(request)
	snapshot, _ := policy.Store.Get(key)
	var snapshotPtr *routeprobe.Snapshot
	if snapshot.Version != 0 {
		snapshotPtr = &snapshot
	}
	ranked := routeprobe.RankCandidates(candidates, nil, policy.ScoreOptions)
	return policy.Selector.Select(routeprobe.SelectionRequest{Key: key, Candidates: ranked, Snapshot: snapshotPtr})
}

func requestKey(request routeprobe.ResolveRequest) routeprobe.RouteKey {
	return routeprobe.RouteKey{SCFRegion: request.SCFRegion, SourceKey: request.SourceKey, Transport: request.Transport, Host: request.Host, Port: request.Port}
}
