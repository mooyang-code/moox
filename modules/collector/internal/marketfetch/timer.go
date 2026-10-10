package marketfetch

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	stocksource "github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"trpc.group/trpc-go/trpc-go/log"
)

// TimerRequestFromEnv parses only the managed identity used
// to Claim frozen work. Membership and write bindings come from Collector.
type TimerInvocation struct {
	Claim     *collectorpb.ClaimTimerBatchReq
	DNSRoutes map[string]sources.DNSResolution
}

func TimerRequestFromEnv(requestID, functionName string, now time.Time) (TimerInvocation, error) {
	spaceID := strings.TrimSpace(os.Getenv("MOOX_SPACE_ID"))
	functionName = strings.TrimSpace(functionName)
	if functionName == "" {
		functionName = strings.TrimSpace(os.Getenv("MOOX_SCF_FUNCTION_NAME"))
	}
	requestID = strings.TrimSpace(requestID)
	groupID, groupCount, err := timerGroupIdentity(spaceID)
	if err != nil {
		return TimerInvocation{}, err
	}
	bindingHash := strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_BINDING_HASH"))
	if spaceID == "" || functionName == "" || requestID == "" || bindingHash == "" ||
		groupCount <= 0 || groupID < 0 || groupID >= groupCount {
		return TimerInvocation{}, fmt.Errorf("timer claim identity is incomplete")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	dnsRoutes, err := parseDNSRoutes(os.Getenv("MOOX_MARKET_FETCH_DNS_ROUTES_JSON"))
	if err != nil {
		return TimerInvocation{}, err
	}
	return TimerInvocation{
		Claim: &collectorpb.ClaimTimerBatchReq{
			SpaceId: spaceID, FunctionName: functionName, RequestId: requestID,
			GroupId: uint32(groupID), GroupCount: uint32(groupCount), BindingHash: bindingHash, TickTime: now.UTC().Unix(),
		},
		DNSRoutes: dnsRoutes,
	}, nil
}

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

func timerGroupIdentity(spaceID string) (int, int, error) {
	groupID := 0
	groupCount := 0
	for name, target := range map[string]*int{
		"MOOX_MARKET_FETCH_GROUP_ID":    &groupID,
		"MOOX_MARKET_FETCH_GROUP_COUNT": &groupCount,
	} {
		raw := strings.TrimSpace(os.Getenv(name))
		if raw == "" {
			continue
		}
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			return 0, 0, fmt.Errorf("%s must be a non-negative integer", name)
		}
		*target = parsed
	}
	if groupCount <= 0 {
		return 0, 0, fmt.Errorf("timer group count is required")
	}
	if groupID >= groupCount {
		return 0, 0, fmt.Errorf("timer group id %d is outside [0,%d)", groupID, groupCount)
	}
	return groupID, groupCount, nil
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
