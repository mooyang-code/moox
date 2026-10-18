package marketfetch

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"unicode"

	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	stocksource "github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn"
	"trpc.group/trpc-go/trpc-go/log"
)

func defaultInstrumentTypeForMarket(marketID, marketType string) string {
	switch strings.ToLower(strings.TrimSpace(marketID)) {
	case "stockcn", "stockhk", "stockus":
		return "equity"
	case "crypto":
		if strings.EqualFold(strings.TrimSpace(marketType), "swap") {
			return "swap"
		}
		return "spot"
	default:
		return strings.ToLower(strings.TrimSpace(marketType))
	}
}

func stockProviderSymbol(subjectID string) (string, error) {
	return stocksource.ProviderSymbol(subjectID)
}

func marketProviderSymbolForMarket(marketID, marketType, subjectID string) (string, error) {
	marketID = strings.ToLower(strings.TrimSpace(marketID))
	if marketID == "stockhk" || marketID == "stockus" {
		parts := strings.Split(strings.TrimSpace(subjectID), ".")
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
			return "", fmt.Errorf("subject %s has no exchange-qualified symbol", subjectID)
		}
		return strings.TrimSpace(parts[0]), nil
	}
	if strings.EqualFold(strings.TrimSpace(marketType), "equity") {
		return stockProviderSymbol(subjectID)
	}
	if marketID == "crypto" && (strings.EqualFold(strings.TrimSpace(marketType), "spot") || strings.EqualFold(strings.TrimSpace(marketType), "swap")) {
		value := strings.ToUpper(strings.TrimSpace(subjectID))
		wantSuffix := "-" + strings.ToUpper(strings.TrimSpace(marketType))
		for _, suffix := range []string{"-SPOT", "-SWAP"} {
			if strings.HasSuffix(value, suffix) && suffix != wantSuffix {
				return "", fmt.Errorf("subject %s has product suffix %s incompatible with market %s", subjectID, suffix, marketType)
			}
		}
		canonical := strings.TrimSuffix(strings.TrimSuffix(value, "-SPOT"), "-SWAP")
		parts := strings.Split(canonical, "-")
		if len(parts) == 2 && validCryptoAssetPart(parts[0]) && validCryptoAssetPart(parts[1]) {
			return strings.ToUpper(parts[0] + parts[1]), nil
		}
	}
	return "", fmt.Errorf("subject %s has no provider symbol codec for market %s", subjectID, marketID)
}

func validCryptoAssetPart(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') && !unicode.Is(unicode.Han, r) {
			return false
		}
	}
	return true
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
