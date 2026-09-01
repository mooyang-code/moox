package marketfetch

import (
	"encoding/json"
	"net"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"trpc.group/trpc-go/trpc-go/log"
)

func parseDNSRoutes(raw string) (map[string]sources.DNSResolution, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var routes map[string][]string
	if err := json.Unmarshal([]byte(raw), &routes); err != nil {
		// DNS is an optimization, not a correctness dependency. A malformed or
		// partially deployed snapshot falls back to the platform resolver.
		log.Warnf("decode MOOX_MARKET_FETCH_DNS_ROUTES_JSON failed, fallback to system DNS: %v", err)
		return nil, nil
	}
	result := make(map[string]sources.DNSResolution, len(routes))
	for rawHost, ips := range routes {
		host := sources.NormalizeDNSHost(rawHost)
		if host == "" {
			continue
		}
		seen := make(map[string]struct{}, len(ips))
		for _, rawIP := range ips {
			ip := net.ParseIP(strings.TrimSpace(rawIP))
			if ip == nil {
				continue
			}
			value := ip.String()
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
			result[host] = appendRouteIP(result[host], value)
		}
	}
	return result, nil
}

func appendRouteIP(route sources.DNSResolution, ip string) sources.DNSResolution {
	for _, existing := range route.IPs {
		if existing == ip {
			return route
		}
	}
	route.IPs = append(route.IPs, ip)
	return route
}
