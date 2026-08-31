package marketfetch

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/packages/routeprobe"
)

// RouteSelectionOptions describes one logical endpoint and the concrete
// addresses that may serve it. The protocol-specific Prober is deliberately
// injected so the same selector can be used by HTTP and TDX callers without
// making this package own either wire protocol.
type RouteSelectionOptions struct {
	SCFRegion   string
	SourceKey   routeprobe.SourceKey
	Transport   routeprobe.Transport
	Host        string
	Port        int
	Addresses   []string
	Snapshot    *routeprobe.Snapshot
	Prober      routeprobe.Prober
	Probe       routeprobe.ProbeOptions
	Score       routeprobe.ScoreOptions
	MaxFallback int
	Clock       func() time.Time
}

// SelectRoute returns the best already-proven route. A fresh snapshot avoids
// opening probe connections; otherwise every supplied address is checked with
// the caller's protocol-aware prober and ranked by the shared routeprobe
// scorer. There is intentionally no rate limiter, quota, cooldown, or
// cross-invocation coordination here.
func SelectRoute(ctx context.Context, options RouteSelectionOptions) (routeprobe.RouteSelection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	request := routeprobe.ResolveRequest{
		SCFRegion: options.SCFRegion, SourceKey: options.SourceKey,
		Transport: options.Transport, Host: options.Host, Port: options.Port,
	}
	if err := (routeprobe.RouteKey{
		SCFRegion: request.SCFRegion, SourceKey: request.SourceKey,
		Transport: request.Transport, Host: request.Host, Port: request.Port,
	}).Validate(); err != nil {
		return routeprobe.RouteSelection{}, err
	}
	addresses := append([]string(nil), options.Addresses...)
	if len(addresses) == 0 {
		addresses = []string{options.Host}
	}
	candidates, err := routeprobe.CandidatesFromAddresses(request, addresses)
	if err != nil {
		return routeprobe.RouteSelection{}, err
	}

	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	freshSnapshot := options.Snapshot != nil && options.Snapshot.FreshAt(clock())
	if options.Snapshot != nil {
		if err := options.Snapshot.Validate(); err != nil {
			return routeprobe.RouteSelection{}, err
		}
		if options.Snapshot.Key != candidates[0].RouteKey() {
			return routeprobe.RouteSelection{}, fmt.Errorf("route snapshot key does not match endpoint")
		}
	}

	var ranked []routeprobe.ScoredCandidate
	if !freshSnapshot {
		if options.Prober == nil {
			return routeprobe.RouteSelection{}, routeprobe.ErrNoProber
		}
		results, probeErr := routeprobe.ProbeCandidates(ctx, candidates, options.Prober, options.Probe)
		if probeErr != nil {
			return routeprobe.RouteSelection{}, probeErr
		}
		stats := routeprobe.BuildRouteStats(results, options.Score)
		ranked = routeprobe.RankCandidates(candidates, stats, options.Score)
	}
	selector := routeprobe.RouteSelector{MaxFallback: options.MaxFallback, Clock: clock}
	var snapshot *routeprobe.Snapshot
	if options.Snapshot != nil {
		snapshot = options.Snapshot
	}
	return selector.Select(routeprobe.SelectionRequest{Key: candidates[0].RouteKey(), Candidates: ranked, Snapshot: snapshot})
}
