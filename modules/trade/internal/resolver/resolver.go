package resolver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var ErrInvalidDomain = errors.New("invalid domain")

type ResolvedIP struct {
	IP                  string
	TCPConnectLatencyMS uint32
}

type Resolution struct {
	Domain     string
	IPs        []ResolvedIP
	Unresolved bool
	Reason     string
}

type Config struct {
	Domains         []string
	LookupTimeout   time.Duration
	ProbeTimeout    time.Duration
	ProbePort       int
	CacheTTL        time.Duration
	MaxIPsPerDomain int
	LookupHost      func(context.Context, string) ([]string, error)
	DialContext     func(context.Context, string, string) (net.Conn, error)
	Metrics         *Metrics
}

type cacheEntry struct {
	resolution Resolution
	expiresAt  time.Time
}

type Resolver struct {
	mu      sync.Mutex
	cfg     Config
	allowed map[string]struct{}
	cache   map[string]cacheEntry
}

func New(cfg Config) *Resolver {
	if cfg.LookupTimeout <= 0 {
		cfg.LookupTimeout = 1500 * time.Millisecond
	}
	if cfg.ProbeTimeout <= 0 {
		cfg.ProbeTimeout = 500 * time.Millisecond
	}
	if cfg.ProbePort <= 0 {
		cfg.ProbePort = 443
	}
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = 5 * time.Minute
	}
	if cfg.MaxIPsPerDomain <= 0 || cfg.MaxIPsPerDomain > 4 {
		cfg.MaxIPsPerDomain = 4
	}
	if cfg.LookupHost == nil {
		cfg.LookupHost = net.DefaultResolver.LookupHost
	}
	if cfg.DialContext == nil {
		cfg.DialContext = (&net.Dialer{}).DialContext
	}
	allowed := make(map[string]struct{}, len(cfg.Domains))
	for _, domain := range cfg.Domains {
		if normalized, ok := normalizeDomain(domain); ok {
			allowed[normalized] = struct{}{}
		}
	}
	return &Resolver{cfg: cfg, allowed: allowed, cache: make(map[string]cacheEntry)}
}

func (r *Resolver) Resolve(ctx context.Context, domains []string, maxIPs int) ([]Resolution, error) {
	if r == nil {
		return nil, errors.New("resolver is nil")
	}
	if r.cfg.Metrics != nil && r.cfg.Metrics.Requests != nil {
		r.cfg.Metrics.Requests.Inc()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, err := normalizeDomains(domains)
	if err != nil {
		if r.cfg.Metrics != nil && r.cfg.Metrics.Failures != nil {
			r.cfg.Metrics.Failures.Inc()
		}
		return nil, err
	}
	if len(normalized) == 0 || len(normalized) > 16 {
		if r.cfg.Metrics != nil && r.cfg.Metrics.Failures != nil {
			r.cfg.Metrics.Failures.Inc()
		}
		return nil, ErrInvalidDomain
	}
	if maxIPs <= 0 {
		maxIPs = r.cfg.MaxIPsPerDomain
	}
	if maxIPs > 4 {
		if r.cfg.Metrics != nil && r.cfg.Metrics.Failures != nil {
			r.cfg.Metrics.Failures.Inc()
		}
		return nil, ErrInvalidDomain
	}
	if maxIPs > r.cfg.MaxIPsPerDomain {
		maxIPs = r.cfg.MaxIPsPerDomain
	}
	results := make([]Resolution, len(normalized))
	var wg sync.WaitGroup
	for index, domain := range normalized {
		index, domain := index, domain
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[index] = r.resolveOne(ctx, domain, maxIPs)
			if results[index].Unresolved && r.cfg.Metrics != nil && r.cfg.Metrics.Unresolved != nil {
				r.cfg.Metrics.Unresolved.Inc()
			}
		}()
	}
	wg.Wait()
	return results, nil
}

func (r *Resolver) resolveOne(ctx context.Context, domain string, maxIPs int) Resolution {
	if _, ok := r.allowed[domain]; !ok {
		return Resolution{Domain: domain, Unresolved: true, Reason: "domain_not_allowed"}
	}
	now := time.Now()
	r.mu.Lock()
	if entry, ok := r.cache[domain]; ok && now.Before(entry.expiresAt) {
		result := cloneResolution(entry.resolution)
		r.mu.Unlock()
		return limitResolution(result, maxIPs)
	}
	r.mu.Unlock()

	lookupCtx, cancel := context.WithTimeout(ctx, r.cfg.LookupTimeout)
	lookupStarted := time.Now()
	addresses, err := r.cfg.LookupHost(lookupCtx, domain)
	if r.cfg.Metrics != nil {
		r.cfg.Metrics.observeLookup(time.Since(lookupStarted))
	}
	cancel()
	if err != nil {
		return Resolution{Domain: domain, Unresolved: true, Reason: "lookup_failed"}
	}
	ips := normalizeIPv4(addresses)
	if len(ips) == 0 {
		return Resolution{Domain: domain, Unresolved: true, Reason: "no_ipv4"}
	}
	// Probe more candidates than the response limit so an early group of
	// dead addresses cannot hide a later healthy address. The allowlisted
	// public domains are still bounded to keep a malicious DNS response from
	// creating unbounded outbound probes.
	if len(ips) > 16 {
		ips = ips[:16]
	}
	probed, probeFailures, probeDuration := probeIPs(ctx, r.cfg, ips)
	if r.cfg.Metrics != nil {
		r.cfg.Metrics.observeProbe(probeDuration, probeFailures)
	}
	if len(probed) == 0 {
		return Resolution{Domain: domain, Unresolved: true, Reason: "probe_failed"}
	}
	sort.SliceStable(probed, func(i, j int) bool {
		if probed[i].TCPConnectLatencyMS != probed[j].TCPConnectLatencyMS {
			return probed[i].TCPConnectLatencyMS < probed[j].TCPConnectLatencyMS
		}
		return probed[i].IP < probed[j].IP
	})
	result := Resolution{Domain: domain, IPs: probed}
	r.mu.Lock()
	r.cache[domain] = cacheEntry{resolution: cloneResolution(result), expiresAt: time.Now().Add(r.cfg.CacheTTL)}
	r.mu.Unlock()
	return limitResolution(result, maxIPs)
}

func probeIPs(ctx context.Context, cfg Config, ips []string) ([]ResolvedIP, int, time.Duration) {
	started := time.Now()
	results := make(chan ResolvedIP, len(ips))
	var failures atomic.Int32
	var wg sync.WaitGroup
	for _, ip := range ips {
		ip := ip
		wg.Add(1)
		go func() {
			defer wg.Done()
			probeCtx, cancel := context.WithTimeout(ctx, cfg.ProbeTimeout)
			defer cancel()
			start := time.Now()
			conn, err := cfg.DialContext(probeCtx, "tcp", net.JoinHostPort(ip, fmt.Sprint(cfg.ProbePort)))
			if err != nil {
				failures.Add(1)
				return
			}
			_ = conn.Close()
			latency := time.Since(start).Milliseconds()
			if latency < 1 {
				latency = 1
			}
			results <- ResolvedIP{IP: ip, TCPConnectLatencyMS: uint32(latency)}
		}()
	}
	wg.Wait()
	close(results)
	result := make([]ResolvedIP, 0, len(ips))
	for item := range results {
		result = append(result, item)
	}
	return result, int(failures.Load()), time.Since(started)
}

func normalizeDomains(domains []string) ([]string, error) {
	seen := make(map[string]struct{}, len(domains))
	result := make([]string, 0, len(domains))
	for _, raw := range domains {
		domain, ok := normalizeDomain(raw)
		if !ok {
			return nil, ErrInvalidDomain
		}
		if _, exists := seen[domain]; exists {
			continue
		}
		seen[domain] = struct{}{}
		result = append(result, domain)
	}
	sort.Strings(result)
	return result, nil
}

func normalizeDomain(raw string) (string, bool) {
	domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	if domain == "" || len(domain) > 253 || net.ParseIP(domain) != nil || strings.Contains(domain, "..") {
		return "", false
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return "", false
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return "", false
			}
		}
	}
	return domain, true
}

func normalizeIPv4(addresses []string) []string {
	seen := make(map[string]struct{}, len(addresses))
	result := make([]string, 0, len(addresses))
	for _, raw := range addresses {
		ip := net.ParseIP(strings.TrimSpace(raw))
		if ip == nil || ip.To4() == nil {
			continue
		}
		if !isPublicIPv4(ip.To4()) {
			continue
		}
		value := ip.To4().String()
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func isPublicIPv4(ip net.IP) bool {
	if ip == nil || ip.To4() == nil {
		return false
	}
	return ip.IsGlobalUnicast() &&
		!ip.IsPrivate() &&
		!ip.IsLoopback() &&
		!ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() &&
		!ip.IsUnspecified() &&
		!ip.IsMulticast() &&
		!isReservedIPv4(ip.To4())
}

func isReservedIPv4(ip net.IP) bool {
	if len(ip) != net.IPv4len {
		return true
	}
	// RFC 6598 shared space, documentation ranges, benchmarking range and
	// the special-use blocks are not safe SCF dial targets.
	first, second, third := ip[0], ip[1], ip[2]
	return first == 0 || first >= 224 ||
		(first == 100 && second >= 64 && second <= 127) ||
		(first == 192 && second == 0 && third == 0) ||
		(first == 192 && second == 0 && third == 2) ||
		(first == 198 && second == 18) ||
		(first == 198 && second == 19) ||
		(first == 198 && second == 51 && third == 100) ||
		(first == 203 && second == 0 && third == 113)
}

func cloneResolution(value Resolution) Resolution {
	value.IPs = append([]ResolvedIP(nil), value.IPs...)
	return value
}

func limitResolution(value Resolution, maxIPs int) Resolution {
	if maxIPs > 0 && len(value.IPs) > maxIPs {
		value.IPs = value.IPs[:maxIPs]
	}
	return value
}
