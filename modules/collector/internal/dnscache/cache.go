// Package dnscache keeps a small, in-memory DNS snapshot for SCF dispatch.
// It deliberately does not probe or rank addresses: the SCF still falls back
// to normal hostname resolution when a cached address cannot be used.
package dnscache

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	defaultRefreshInterval = 5 * time.Minute
	defaultResolveTimeout  = 5 * time.Second
)

// Config controls the periodic resolver. Nameservers may be empty to use the
// host's resolver; otherwise each address is queried independently.
type Config struct {
	Domains         []string
	RefreshInterval time.Duration
	ResolveTimeout  time.Duration
	Nameservers     []string
}

// Cache is safe for the timer and scheduler goroutines to use concurrently.
type Cache struct {
	mu              sync.RWMutex
	refreshMu       sync.Mutex
	routes          map[string]sources.DNSResolution
	lastAttempt     time.Time
	refreshInterval time.Duration
	resolveTimeout  time.Duration
	nameservers     []string
	lookup          func(context.Context, string) ([]string, error)
	domains         []string
}

func New(cfg Config) *Cache {
	interval := cfg.RefreshInterval
	if interval <= 0 {
		interval = defaultRefreshInterval
	}
	timeout := cfg.ResolveTimeout
	if timeout <= 0 {
		timeout = defaultResolveTimeout
	}
	domains := normalizeDomains(cfg.Domains)
	return &Cache{
		routes:          make(map[string]sources.DNSResolution),
		refreshInterval: interval,
		resolveTimeout:  timeout,
		nameservers:     normalizeNameservers(cfg.Nameservers),
		lookup:          newLookup(cfg.Nameservers, timeout),
		domains:         domains,
	}
}

// Due reports whether the periodic timer should start a refresh.
func (c *Cache) Due(now time.Time) bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastAttempt.IsZero() || now.Sub(c.lastAttempt) >= c.refreshInterval
}

// Refresh resolves all configured domains. A failed domain never erases its
// previous good result; this keeps dispatch useful during a transient DNS
// outage. An error is returned only when no domain could be refreshed.
func (c *Cache) Refresh(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("dns cache is nil")
	}
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	c.mu.Lock()
	nowAttempt := time.Now().UTC()
	if !c.lastAttempt.IsZero() && nowAttempt.Sub(c.lastAttempt) < c.refreshInterval {
		c.mu.Unlock()
		return nil
	}
	c.lastAttempt = nowAttempt
	domains := append([]string(nil), c.domains...)
	lookup := c.lookup
	timeout := c.resolveTimeout
	c.mu.Unlock()
	if len(domains) == 0 {
		return nil
	}
	if lookup == nil {
		return fmt.Errorf("dns resolver is not initialized")
	}

	resolved := make(chan struct {
		host string
		ips  []string
		err  error
	}, len(domains))
	handlers := make([]func() error, 0, len(domains))
	for _, host := range domains {
		host := host
		handlers = append(handlers, func() error {
			resolveCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			ips, err := lookup(resolveCtx, host)
			resolved <- struct {
				host string
				ips  []string
				err  error
			}{host: host, ips: ips, err: err}
			return nil
		})
	}
	_ = trpc.GoAndWait(handlers...)
	close(resolved)

	now := time.Now().UTC()
	successes := 0
	var firstErr error
	for result := range resolved {
		if result.err != nil || len(result.ips) == 0 {
			failure := result.err
			if firstErr == nil {
				if failure != nil {
					firstErr = fmt.Errorf("resolve %s: %w", result.host, failure)
				} else {
					firstErr = fmt.Errorf("resolve %s: no addresses returned", result.host)
				}
			}
			if failure == nil {
				failure = fmt.Errorf("no addresses returned")
			}
			log.WarnContextf(ctx, "collector_dns_refresh_failed host=%s error=%v", result.host, failure)
			continue
		}
		c.mu.Lock()
		c.routes[result.host] = sources.DNSResolution{IPs: append([]string(nil), result.ips...), ResolvedAt: now}
		c.mu.Unlock()
		successes++
		log.InfoContextf(ctx, "collector_dns_refresh_succeeded host=%s ips=%d", result.host, len(result.ips))
	}
	if successes == 0 && firstErr != nil {
		return firstErr
	}
	return nil
}

// Snapshot returns a copy suitable for embedding in a task request.
func (c *Cache) Snapshot() map[string]sources.DNSResolution {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make(map[string]sources.DNSResolution, len(c.routes))
	for host, route := range c.routes {
		result[host] = sources.DNSResolution{IPs: append([]string(nil), route.IPs...), ResolvedAt: route.ResolvedAt}
	}
	return result
}

func normalizeDomains(domains []string) []string {
	seen := make(map[string]struct{}, len(domains))
	result := make([]string, 0, len(domains))
	for _, value := range domains {
		host := strings.ToLower(strings.TrimSpace(value))
		host = strings.TrimSuffix(host, ".")
		if host == "" {
			continue
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		result = append(result, host)
	}
	sort.Strings(result)
	return result
}

func normalizeNameservers(nameservers []string) []string {
	result := make([]string, 0, len(nameservers))
	for _, value := range nameservers {
		value = strings.TrimSpace(value)
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func newLookup(nameservers []string, timeout time.Duration) func(context.Context, string) ([]string, error) {
	servers := normalizeNameservers(nameservers)
	if len(servers) == 0 {
		return func(ctx context.Context, host string) ([]string, error) {
			return net.DefaultResolver.LookupHost(ctx, host)
		}
	}
	resolvers := make([]*net.Resolver, 0, len(servers))
	for _, server := range servers {
		server := server
		if _, _, err := net.SplitHostPort(server); err != nil {
			server = net.JoinHostPort(strings.Trim(server, "[]"), "53")
		}
		resolvers = append(resolvers, &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: timeout}).DialContext(ctx, network, server)
		}})
	}
	return func(ctx context.Context, host string) ([]string, error) {
		var firstErr error
		for _, resolver := range resolvers {
			ips, err := resolver.LookupHost(ctx, host)
			if err == nil && len(ips) > 0 {
				return uniqueIPs(ips), nil
			}
			if firstErr == nil {
				firstErr = err
			}
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("no DNS resolver returned addresses")
		}
		return nil, firstErr
	}
}

func uniqueIPs(ips []string) []string {
	seen := make(map[string]struct{}, len(ips))
	result := make([]string, 0, len(ips))
	for _, value := range ips {
		if ip := net.ParseIP(strings.TrimSpace(value)); ip != nil {
			value = ip.String()
			if _, ok := seen[value]; !ok {
				seen[value] = struct{}{}
				result = append(result, value)
			}
		}
	}
	return result
}
