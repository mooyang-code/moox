package marketfetch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	stocksource "github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn"
	"trpc.group/trpc-go/trpc-go/log"
)

// TimerRequestFromEnv turns the static per-function assignment into the same
// bounded request used by the manual/egress paths. No control-plane request is
// made from SCF.
func TimerRequestFromEnv(requestID, functionName string, now time.Time, runtimeResolvers ...RuntimeResolvers) (Request, string, error) {
	var resolvers RuntimeResolvers
	if len(runtimeResolvers) > 0 {
		resolvers = runtimeResolvers[0]
	}
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_PROVIDER")))
	sourceID := strings.ToLower(strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_SOURCE_ID")))
	marketType := strings.ToLower(strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_MARKET_TYPE")))
	marketID := strings.ToLower(strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_MARKET_ID")))
	instrumentType := strings.ToLower(strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_INSTRUMENT_TYPE")))
	datasetID := strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_DATASET_ID"))
	frequency := strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_FREQUENCY"))
	spaceID := strings.TrimSpace(os.Getenv("MOOX_SPACE_ID"))
	if provider == "" || marketType == "" || datasetID == "" || frequency == "" || spaceID == "" {
		return Request{}, "", fmt.Errorf("timer market fetch environment is incomplete")
	}
	if sourceID == "" {
		if resolvers.SourceID != nil {
			sourceID = resolvers.SourceID(provider, marketType)
		}
		if sourceID == "" {
			return Request{}, "", fmt.Errorf("timer market fetch source_id is required")
		}
	}
	if marketID == "" {
		marketID = spaceID
	}
	if instrumentType == "" {
		instrumentType = defaultInstrumentTypeForMarket(marketID, marketType)
	}
	_, err := CronForFrequency(frequency)
	if err != nil {
		return Request{}, "", err
	}
	subjects := normalizeSubjects(strings.Split(os.Getenv("MOOX_MARKET_FETCH_SUBJECTS"), "|"))
	if isCryptoKlineGroup(TaskGroup{MarketType: marketType, MarketID: marketID}) {
		subjects = pinPriorityCryptoSubjects(subjects)
	}
	if len(subjects) == 0 {
		return Request{}, "", fmt.Errorf("timer market fetch subjects must contain at least one value")
	}
	if !strings.EqualFold(spaceID, StockCNSpaceID) && len(subjects) > MaxRealtimeItems {
		return Request{}, "", fmt.Errorf("timer market fetch subjects must contain 1..%d values", MaxRealtimeItems)
	}
	dnsRoutes, err := parseDNSRoutes(os.Getenv("MOOX_MARKET_FETCH_DNS_ROUTES_JSON"))
	if err != nil {
		return Request{}, "", err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	assignmentHash := strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_ASSIGNMENT_HASH"))
	if assignmentHash == "" {
		assignmentHash = AssignmentHash(provider, marketType, datasetID, frequency, strings.Join(subjects, "|"))
	}
	externalSymbols, err := parseExternalSymbols(os.Getenv("MOOX_MARKET_FETCH_SYMBOLS_JSON"), subjects, spaceID, marketID, marketType, provider, resolvers.Symbol)
	if err != nil {
		return Request{}, "", err
	}
	minute := now.UTC().Truncate(time.Minute).Format(time.RFC3339)
	hash := sha256.Sum256([]byte(strings.Join([]string{assignmentHash, minute}, "\x00")))
	batchID := "timer-" + hex.EncodeToString(hash[:])[:24]
	items := make([]domain.CollectionItem, 0, len(subjects))
	for _, subject := range subjects {
		items = append(items, domain.CollectionItem{SubjectID: subject, Symbol: externalSymbols[subject], Provider: provider, SourceID: sourceID, MarketType: marketType, DataType: "kline", DatasetID: datasetID, Frequency: frequency, BarLimit: MaxRealtimeRows})
	}
	concurrency := envInt("MOOX_FETCH_MAX_INFLIGHT_REQUESTS", envInt("MOOX_MARKET_FETCH_MAX_INFLIGHT", DefaultConcurrency))
	groupID, groupCount, err := timerGroupIdentity(spaceID)
	if err != nil {
		return Request{}, "", err
	}
	return Request{BatchID: batchID, BatchKind: domain.BatchKindRealtime, SpaceID: spaceID, MarketID: marketID, InstrumentType: instrumentType, DatasetID: datasetID, Frequency: frequency, Provider: provider, SourceID: sourceID, MarketType: marketType, FunctionName: strings.TrimSpace(functionName), RequestID: requestID, GroupID: groupID, GroupCount: groupCount, DNSRoutes: dnsRoutes, Items: items, Concurrency: concurrency}, os.Getenv("MOOX_STORAGE_RPC_GATEWAY_TARGET"), nil
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
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
	if strings.EqualFold(strings.TrimSpace(spaceID), StockCNSpaceID) {
		if groupCount <= 0 {
			return 0, 0, fmt.Errorf("stockcn timer group count is required")
		}
		if groupID < 0 || groupID >= groupCount {
			return 0, 0, fmt.Errorf("stockcn timer group id %d is outside [0,%d)", groupID, groupCount)
		}
	}
	if groupCount > 0 && groupID >= groupCount {
		return 0, 0, fmt.Errorf("timer group id %d is outside [0,%d)", groupID, groupCount)
	}
	return groupID, groupCount, nil
}

func parseExternalSymbols(raw string, subjects []string, spaceID, marketID, marketType, provider string, resolver SymbolResolver) (map[string]string, error) {
	result := make(map[string]string, len(subjects))
	stockCN := strings.EqualFold(strings.TrimSpace(spaceID), StockCNSpaceID)
	_ = raw
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			return nil, fmt.Errorf("decode timer external symbol mapping: %w", err)
		}
	}
	for _, subject := range subjects {
		if stockCN {
			resolved, err := stockProviderSymbol(subject)
			if err != nil {
				return nil, err
			}
			if explicit := strings.TrimSpace(result[subject]); explicit != "" && explicit != resolved {
				return nil, fmt.Errorf("subject %s explicit symbol %q conflicts with strict symbol %q", subject, explicit, resolved)
			}
			result[subject] = resolved
			continue
		}
		if explicit := strings.TrimSpace(result[subject]); explicit != "" {
			continue
		}
		if resolver != nil {
			resolved, err := resolveProviderSymbol(resolver, provider, marketID, marketType, subject)
			if err != nil {
				return nil, err
			}
			result[subject] = resolved
			continue
		}
		resolved, err := marketProviderSymbolForMarket(marketID, marketType, subject)
		if err != nil {
			return nil, fmt.Errorf("timer subject %s has no provider symbol resolver: %w", subject, err)
		}
		result[subject] = resolved
	}
	return result, nil
}

func stockProviderSymbol(subjectID string) (string, error) {
	return stocksource.ProviderSymbol(subjectID)
}

func marketProviderSymbol(marketType, subjectID string) (string, error) {
	return marketProviderSymbolForMarket("", marketType, subjectID)
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
		if len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != "" {
			for _, part := range parts {
				for _, r := range part {
					if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
						return "", fmt.Errorf("subject %s contains unsupported crypto asset characters", subjectID)
					}
				}
			}
			return strings.ToUpper(parts[0] + parts[1]), nil
		}
	}
	return "", fmt.Errorf("subject %s has no provider symbol codec for market %s", subjectID, marketID)
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
