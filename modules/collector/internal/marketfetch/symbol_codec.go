package marketfetch

import (
	"encoding/json"
	"net"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"trpc.group/trpc-go/trpc-go/log"
)

// defaultInstrumentType 返回市场在任务或标签没写品种时用的品种（标的规则给出）；未登记的市场报错。
func defaultInstrumentType(marketID, marketType string) (marketdata.InstrumentType, error) {
	rules, err := markets.RulesFor(marketID)
	if err != nil {
		return "", err
	}
	return rules.DefaultInstrumentType(marketType), nil
}

// parseSubject 调市场的标的规则，把标的 ID 换成传给数据源的代码和交易所；instrumentType 为空时用市场的默认品种。
func parseSubject(marketID string, instrumentType marketdata.InstrumentType, subjectID string) (markets.Subject, error) {
	rules, err := markets.RulesFor(marketID)
	if err != nil {
		return markets.Subject{}, err
	}
	if instrumentType == "" {
		instrumentType = rules.DefaultInstrumentType("")
	}
	return rules.ParseSubject(instrumentType, subjectID)
}

func parseDNSRoutes(raw string) (map[string]sources.DNSResolution, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var routes map[string][]string
	if err := json.Unmarshal([]byte(raw), &routes); err != nil {
		// DNS is an optimization, not a correctness dependency. A malformed or
		// partially deployed snapshot must fall back to the platform resolver.
		log.Warnf("decode MOOX_MARKET_FETCH_DNS_ROUTES_JSON failed, fallback to system DNS: %v", err)
		return nil, nil
	}
	result := make(map[string]sources.DNSResolution, len(routes))
	for rawHost, ips := range routes {
		host := sources.NormalizeDNSHost(rawHost)
		if host == "" {
			continue
		}
		// Keep only canonical IP strings and deduplicate the payload. A malformed
		// entry must not make the whole invocation fail; the HTTP client will
		// still fall back to the original hostname when no usable address remains.
		seen := make(map[string]struct{}, len(ips))
		canonical := make([]string, 0, len(ips))
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
			canonical = append(canonical, value)
		}
		if len(canonical) == 0 {
			continue
		}
		route := result[host]
		routeSeen := make(map[string]struct{}, len(route.IPs)+len(canonical))
		for _, value := range route.IPs {
			routeSeen[value] = struct{}{}
		}
		for _, value := range canonical {
			if _, exists := routeSeen[value]; exists {
				continue
			}
			route.IPs = append(route.IPs, value)
			routeSeen[value] = struct{}{}
		}
		result[host] = route
	}
	return result, nil
}
