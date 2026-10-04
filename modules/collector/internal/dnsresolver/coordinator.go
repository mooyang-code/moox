package dnsresolver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/dnscache"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
)

type Coordinator struct {
	Local    *dnscache.Cache
	Remote   DomainResolver
	Domains  []string
	Interval time.Duration
	CacheTTL time.Duration

	mu              sync.RWMutex
	refreshMu       sync.Mutex
	lastAttempt     time.Time
	routes          map[string]sources.DNSResolution
	routeSource     map[string]string
	persistencePath string
	lastError       error
	status          Status
	metrics         *Metrics
}

// Status is the operational snapshot for the DNS coordinator. It is kept
// separate from the SCF route payload so diagnostics can explain which source
// supplied a route without changing the short-lived function contract.
type Status struct {
	Source            string
	Hash              string
	ManagedHash       string
	RouteCount        int
	RouteAgeSeconds   float64
	LastRefreshAt     time.Time
	LastSuccessAt     time.Time
	LastErrorCategory string
}

func NewCoordinator(local *dnscache.Cache, remote DomainResolver, domains []string, interval time.Duration) *Coordinator {
	// Legacy/local-only callers retain the dnscache contract: a last-good
	// snapshot is kept until a newer refresh replaces it.
	return NewCoordinatorWithTTL(local, remote, domains, interval, 0)
}

// NewCoordinatorWithTTL keeps refresh cadence separate from route freshness.
// A non-positive cacheTTL disables expiry, which is useful for the legacy
// local-DNS fallback. Enabled remote resolver deployments always pass a
// positive TTL so failed refreshes cannot keep an expired address alive.
func NewCoordinatorWithTTL(local *dnscache.Cache, remote DomainResolver, domains []string, interval, cacheTTL time.Duration) *Coordinator {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &Coordinator{
		Local: local, Remote: remote, Domains: normalizeDomains(domains),
		Interval: interval, CacheTTL: cacheTTL, routes: make(map[string]sources.DNSResolution), routeSource: make(map[string]string),
	}
}

// NewCoordinatorWithMetrics is the bootstrap constructor for the production
// coordinator. The legacy constructors remain useful for small tests and
// callers that do not need Prometheus instrumentation.
func NewCoordinatorWithMetrics(local *dnscache.Cache, remote DomainResolver, domains []string, interval, cacheTTL time.Duration, metrics *Metrics) *Coordinator {
	coordinator := NewCoordinatorWithTTL(local, remote, domains, interval, cacheTTL)
	coordinator.metrics = metrics
	return coordinator
}

// NewCoordinatorWithMetricsAndPersistence is the production constructor. The
// persisted snapshot is only a last-known-good fallback for process restarts;
// it is never sent to the resolver or exposed in the SCF payload directly.
func NewCoordinatorWithMetricsAndPersistence(local *dnscache.Cache, remote DomainResolver, domains []string, interval, cacheTTL time.Duration, metrics *Metrics, persistencePath string) *Coordinator {
	coordinator := NewCoordinatorWithMetrics(local, remote, domains, interval, cacheTTL, metrics)
	coordinator.persistencePath = strings.TrimSpace(persistencePath)
	return coordinator
}

// RestoreLastGoodSnapshot loads the last successful Trade snapshot, if one is
// present. A restored route has no local receipt time so a restart does not
// discard the route before the first bounded remote refresh can complete.
func (c *Coordinator) RestoreLastGoodSnapshot() error {
	if c == nil || c.persistencePath == "" {
		return nil
	}
	raw, err := os.ReadFile(c.persistencePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read DNS snapshot %s: %w", c.persistencePath, err)
	}
	var persisted persistedSnapshot
	if err := json.Unmarshal(raw, &persisted); err != nil {
		return fmt.Errorf("decode DNS snapshot %s: %w", c.persistencePath, err)
	}
	now := time.Now().UTC()
	allowed := make(map[string]struct{}, len(c.Domains))
	for _, domain := range c.Domains {
		allowed[normalizeDomain(domain)] = struct{}{}
	}
	routes := make(map[string]sources.DNSResolution, len(persisted.Routes))
	for rawHost, rawRoute := range persisted.Routes {
		host := normalizeDomain(rawHost)
		if host == "" {
			continue
		}
		if len(allowed) > 0 {
			if _, ok := allowed[host]; !ok {
				continue
			}
		}
		resolvedAt := rawRoute.ResolvedAt
		if resolvedAt.IsZero() {
			resolvedAt = persisted.SavedAt
		}
		if c.CacheTTL > 0 && (resolvedAt.IsZero() || now.Sub(resolvedAt) >= c.CacheTTL) {
			continue
		}
		route := sources.DNSResolution{ResolvedAt: resolvedAt.UTC(), LatencyMS: make(map[string]uint32)}
		seenIPs := make(map[string]struct{}, len(rawRoute.IPs))
		for _, rawIP := range rawRoute.IPs {
			ip := net.ParseIP(strings.TrimSpace(rawIP))
			if !isPublicIPv4(ip) {
				continue
			}
			value := ip.To4().String()
			if _, duplicate := seenIPs[value]; duplicate || len(route.IPs) >= 4 {
				continue
			}
			seenIPs[value] = struct{}{}
			route.IPs = append(route.IPs, value)
			if latency, ok := rawRoute.LatencyMS[rawIP]; ok {
				route.LatencyMS[value] = latency
			}
		}
		if len(route.IPs) > 0 {
			routes[host] = route
		}
	}
	if len(routes) == 0 {
		return nil
	}
	c.mu.Lock()
	c.routes = routes
	c.routeSource = make(map[string]string, len(routes))
	for host := range routes {
		c.routeSource[host] = "trade"
	}
	c.status = buildStatus(now, routes, c.routeSource, c.status, nil, nil, false)
	c.mu.Unlock()
	return nil
}

func (c *Coordinator) Due(now time.Time) bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastAttempt.IsZero() || now.Sub(c.lastAttempt) >= c.Interval
}

func (c *Coordinator) Refresh(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("dns coordinator is nil")
	}
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	now := time.Now().UTC()
	c.mu.Lock()
	if !c.lastAttempt.IsZero() && now.Sub(c.lastAttempt) < c.Interval {
		c.mu.Unlock()
		return nil
	}
	c.lastAttempt = now
	previous := filterFreshRoutes(c.routes, now, c.CacheTTL)
	previousSources := cloneSources(c.routeSource)
	previousStatus := c.status
	domains := append([]string(nil), c.Domains...)
	c.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	var remoteRoutes map[string]sources.DNSResolution
	var remoteErr error
	if c.Remote != nil && len(domains) > 0 {
		remoteRoutes, remoteErr = c.Remote.ResolveDomains(ctx, domains)
		receiptAt := time.Now().UTC()
		for host, route := range remoteRoutes {
			route.ResolvedAt = receiptAt
			remoteRoutes[host] = route
		}
	}
	remoteHosts := make(map[string]struct{}, len(remoteRoutes))
	for host := range remoteRoutes {
		remoteHosts[normalizeDomain(host)] = struct{}{}
	}
	missingRemote := 0
	for _, domain := range domains {
		if _, ok := remoteHosts[normalizeDomain(domain)]; !ok {
			missingRemote++
		}
	}
	needLocal := c.Remote == nil || remoteErr != nil || missingRemote > 0
	var localRoutes map[string]sources.DNSResolution
	var localErr error
	if c.Local != nil && needLocal {
		localErr = c.Local.Refresh(ctx)
		localRoutes = filterFreshRoutes(c.Local.Snapshot(), now, c.CacheTTL)
	}

	merged := cloneRoutes(previous)
	mergedSources := cloneSources(previousSources)
	for host, route := range localRoutes {
		if _, ok := merged[host]; !ok {
			route.ResolvedAt = route.ResolvedAt.UTC()
			merged[host] = route
			mergedSources[host] = "local"
		}
	}
	for host, route := range remoteRoutes {
		merged[host] = route
		mergedSources[host] = "trade"
	}
	status := buildStatus(now, merged, mergedSources, previousStatus, remoteErr, localErr, len(remoteRoutes) > 0 || len(localRoutes) > 0)
	c.mu.Lock()
	c.routes = merged
	c.routeSource = mergedSources
	c.lastError = firstError(remoteErr, localErr)
	c.status = status
	c.mu.Unlock()
	if len(remoteRoutes) > 0 && c.persistencePath != "" {
		if err := c.persistTradeSnapshot(merged, mergedSources); err != nil {
			// The in-memory/SCF update is still valid. Surface persistence failure
			// to the caller so health logs expose that restart protection is at risk.
			status.LastErrorCategory = "snapshot_persist"
			c.mu.Lock()
			c.lastError = firstError(c.lastError, err)
			c.status = status
			c.mu.Unlock()
			if c.metrics != nil {
				c.metrics.observe(status)
			}
			return err
		}
	}
	if c.metrics != nil {
		c.metrics.observe(status)
	}
	if len(remoteRoutes) > 0 || len(localRoutes) > 0 || len(previous) > 0 {
		return nil
	}
	if remoteErr != nil {
		return remoteErr
	}
	if localErr != nil {
		return localErr
	}
	return fmt.Errorf("DNS resolver returned no routes")
}

type persistedSnapshot struct {
	Routes  map[string]sources.DNSResolution `json:"routes"`
	SavedAt time.Time                        `json:"saved_at"`
}

func (c *Coordinator) persistTradeSnapshot(routes map[string]sources.DNSResolution, routeSource map[string]string) error {
	tradeRoutes := make(map[string]sources.DNSResolution)
	for host, route := range routes {
		if routeSource[host] != "trade" || len(route.IPs) == 0 {
			continue
		}
		tradeRoutes[normalizeDomain(host)] = route
	}
	if len(tradeRoutes) == 0 {
		return nil
	}
	raw, err := json.Marshal(persistedSnapshot{Routes: tradeRoutes, SavedAt: time.Now().UTC()})
	if err != nil {
		return fmt.Errorf("encode DNS snapshot: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(c.persistencePath), 0o750); err != nil {
		return fmt.Errorf("create DNS snapshot directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.persistencePath), ".dns-resolver-snapshot-*")
	if err != nil {
		return fmt.Errorf("create DNS snapshot temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set DNS snapshot permissions: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write DNS snapshot: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync DNS snapshot: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close DNS snapshot: %w", err)
	}
	if err := os.Rename(tmpName, c.persistencePath); err != nil {
		return fmt.Errorf("replace DNS snapshot: %w", err)
	}
	return nil
}

func filterFreshRoutes(routes map[string]sources.DNSResolution, now time.Time, ttl time.Duration) map[string]sources.DNSResolution {
	if ttl <= 0 {
		return cloneRoutes(routes)
	}
	result := make(map[string]sources.DNSResolution, len(routes))
	for host, route := range routes {
		// Older local-cache snapshots did not carry a timestamp. Treat those as
		// usable for this refresh; newly written snapshots always do.
		if !route.ResolvedAt.IsZero() && now.Sub(route.ResolvedAt) >= ttl {
			continue
		}
		result[strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))] = route
	}
	return result
}

func (c *Coordinator) Snapshot() map[string]sources.DNSResolution {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return filterFreshRoutes(c.routes, time.Now().UTC(), c.CacheTTL)
}

func (c *Coordinator) LastError() error {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastError
}

// Status returns a copy of the latest DNS source/hash/age diagnostics.
func (c *Coordinator) Status() Status {
	if c == nil {
		return Status{}
	}
	c.mu.RLock()
	status := c.status
	c.mu.RUnlock()
	if status.RouteCount > 0 && !status.LastRefreshAt.IsZero() {
		status.RouteAgeSeconds += time.Since(status.LastRefreshAt).Seconds()
		if status.RouteAgeSeconds < 0 {
			status.RouteAgeSeconds = 0
		}
	}
	return status
}

func buildStatus(now time.Time, routes map[string]sources.DNSResolution, routeSource map[string]string, previous Status, remoteErr, localErr error, refreshed bool) Status {
	status := Status{RouteCount: len(routes), LastRefreshAt: now, Hash: routeHash(routes), ManagedHash: marketfetch.ManagedDNSHash(routes), LastSuccessAt: previous.LastSuccessAt}
	if refreshed {
		status.LastSuccessAt = now
	}
	for _, route := range routes {
		if route.ResolvedAt.IsZero() {
			continue
		}
		age := now.Sub(route.ResolvedAt).Seconds()
		if age < 0 {
			age = 0
		}
		if age > status.RouteAgeSeconds {
			status.RouteAgeSeconds = age
		}
	}
	seen := make(map[string]struct{}, 2)
	for _, source := range routeSource {
		seen[source] = struct{}{}
	}
	switch {
	case len(seen) == 0:
		status.Source = "none"
	case len(seen) == 1:
		for source := range seen {
			status.Source = source
		}
	default:
		status.Source = "hybrid"
	}
	if !refreshed && len(routes) > 0 {
		status.Source = "retained"
	}
	switch {
	case remoteErr != nil && localErr != nil:
		status.LastErrorCategory = "trade_and_local_dns"
	case remoteErr != nil:
		status.LastErrorCategory = "trade_rpc"
	case localErr != nil:
		status.LastErrorCategory = "local_dns"
	}
	return status
}

func routeHash(routes map[string]sources.DNSResolution) string {
	if len(routes) == 0 {
		return ""
	}
	type item struct {
		Host      string            `json:"host"`
		IPs       []string          `json:"ips"`
		LatencyMS map[string]uint32 `json:"latency_ms,omitempty"`
	}
	items := make([]item, 0, len(routes))
	for host, route := range routes {
		ips := append([]string(nil), route.IPs...)
		latency := make(map[string]uint32, len(route.LatencyMS))
		for ip, value := range route.LatencyMS {
			latency[ip] = value
		}
		items = append(items, item{Host: host, IPs: ips, LatencyMS: latency})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Host < items[j].Host })
	raw, _ := json.Marshal(items)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func cloneSources(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func cloneRoutes(values map[string]sources.DNSResolution) map[string]sources.DNSResolution {
	result := make(map[string]sources.DNSResolution, len(values))
	for host, route := range values {
		item := sources.DNSResolution{IPs: append([]string(nil), route.IPs...), ResolvedAt: route.ResolvedAt}
		if len(route.LatencyMS) > 0 {
			item.LatencyMS = make(map[string]uint32, len(route.LatencyMS))
			for ip, latency := range route.LatencyMS {
				item.LatencyMS[ip] = latency
			}
		}
		result[strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))] = item
	}
	return result
}

func normalizeDomains(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
		if host == "" {
			continue
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		result = append(result, host)
	}
	return result
}

func firstError(items ...error) error {
	for _, item := range items {
		if item != nil {
			return item
		}
	}
	return nil
}
