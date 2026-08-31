package marketfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/mooyang-code/moox/packages/routeprobe"
)

// HTTPRouteProvider turns the DNS snapshot already carried by a short-lived
// crypto invocation into protocol-proven HTTP routes. It is intentionally a
// request-time selector: there is no global limiter, quota, cooldown, or
// resident route worker.
type HTTPRouteProvider struct {
	Region        string
	Routes        map[string]sources.DNSResolution
	ProbeTimeout  time.Duration
	ProbeAttempts int
	ProbeParallel int
	MaxFallback   int
	Snapshot      *routeprobe.Snapshot
	Prober        routeprobe.Prober
	mu            sync.Mutex
	selected      map[string][]string
}

var _ marketdata.RouteIPProvider = (*HTTPRouteProvider)(nil)

// NewHTTPRouteProviderFromEnvironment creates the crypto HTTP route selector
// from the static SCF DNS snapshot. A missing snapshot is valid and preserves
// the normal hostname resolver path.
func NewHTTPRouteProviderFromEnvironment() *HTTPRouteProvider {
	routes, _ := parseDNSRoutes(strings.TrimSpace(getenv("MOOX_MARKET_FETCH_DNS_ROUTES_JSON")))
	provider := &HTTPRouteProvider{
		Region:        firstNonEmpty(getenv("MOOX_SCF_REGION"), "unknown"),
		Routes:        routes,
		ProbeTimeout:  time.Duration(envInt("MOOX_ROUTE_PROBE_TIMEOUT_SECONDS", 5)) * time.Second,
		ProbeAttempts: envInt("MOOX_ROUTE_PROBE_ATTEMPTS", 1),
		ProbeParallel: envInt("MOOX_ROUTE_PROBE_CONCURRENCY", 1),
		MaxFallback:   envInt("MOOX_ROUTE_PROBE_MAX_FALLBACK", 2),
	}
	if raw := strings.TrimSpace(getenv("MOOX_ROUTE_SNAPSHOT_JSON")); raw != "" {
		if snapshot, err := routeprobe.UnmarshalSnapshot([]byte(raw)); err == nil {
			provider.Snapshot = &snapshot
		}
	}
	return provider
}

// SelectRouteIPs returns protocol-proven addresses in score order. It only
// uses the snapshot for the requested hostname, so spot and swap endpoints
// remain isolated even though both use HTTPS.
func (provider *HTTPRouteProvider) SelectRouteIPs(ctx context.Context, key routeprobe.SourceKey, transport routeprobe.Transport, host string, port int) ([]string, error) {
	if provider == nil || len(provider.Routes) == 0 {
		return nil, nil
	}
	if key.ProviderID != "binance" {
		return nil, fmt.Errorf("HTTP route provider only supports binance, got %s", key.ProviderID)
	}
	if transport != routeprobe.TransportHTTPS {
		return nil, fmt.Errorf("binance route selection requires https transport")
	}
	route, ok := provider.Routes[sources.NormalizeDNSHost(host)]
	if !ok || len(route.IPs) == 0 {
		return nil, nil
	}
	// A short-lived batch can ask for the same endpoint from many subject
	// goroutines. Hold the provider lock through the first probe so they share
	// one result instead of opening duplicate probe sessions. Different
	// endpoints are still isolated by the cache key; this lock is intentionally
	// process-local and is not a rate limiter or cross-invocation quota.
	provider.mu.Lock()
	defer provider.mu.Unlock()
	cacheKey := key.String() + "|" + transport.String() + "|" + host + ":" + fmt.Sprint(port)
	if selected := provider.selected[cacheKey]; len(selected) > 0 {
		return append([]string(nil), selected...), nil
	}
	path := "/api/v3/time"
	if key.SourceID == "swap_http" {
		path = "/fapi/v1/time"
	}
	prober := provider.Prober
	if prober == nil {
		prober = routeprobe.HTTPProbe{Config: routeprobe.HTTPProbeConfig{
			Method: http.MethodGet, Scheme: "https", Path: path, ExpectedStatuses: []int{http.StatusOK}, MaxBodyBytes: 4096,
			ResponseValidator: func(response routeprobe.HTTPProbeResponse) error {
				var payload map[string]any
				if err := json.Unmarshal(response.Body, &payload); err != nil {
					return fmt.Errorf("decode Binance health response: %w", err)
				}
				return nil
			},
		}}
	}
	selection, err := SelectRoute(ctx, RouteSelectionOptions{
		SCFRegion: provider.Region, SourceKey: key, Transport: transport,
		Host: host, Port: port, Addresses: route.IPs, Snapshot: provider.Snapshot,
		Prober: prober, Probe: routeprobe.ProbeOptions{
			Concurrency: provider.ProbeParallel, Attempts: provider.ProbeAttempts, AttemptTimeout: provider.ProbeTimeout,
		}, MaxFallback: provider.MaxFallback,
	})
	if err != nil {
		return nil, fmt.Errorf("select Binance HTTP route for %s: %w", host, err)
	}
	addresses := make([]string, 0, len(selection.Routes))
	for _, candidate := range selection.Routes {
		addresses = append(addresses, candidate.Address)
	}
	if provider.selected == nil {
		provider.selected = make(map[string][]string)
	}
	provider.selected[cacheKey] = append([]string(nil), addresses...)
	return addresses, nil
}

// getenv is a small seam for tests without making the provider depend on the
// process environment after construction.
var getenv = os.Getenv
