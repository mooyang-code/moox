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
func TimerRequestFromEnv(requestID, functionName string, now time.Time) (Request, string, error) {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_PROVIDER")))
	sourceID := strings.ToLower(strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_SOURCE_ID")))
	marketType := strings.ToLower(strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_MARKET_TYPE")))
	marketID := strings.ToLower(strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_MARKET_ID")))
	instrumentType := strings.ToLower(strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_INSTRUMENT_TYPE")))
	datasetID := strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_DATASET_ID"))
	frequency := strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_FREQUENCY"))
	spaceID := strings.TrimSpace(os.Getenv("MOOX_SPACE_ID"))
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_MODE")))
	if mode == "instrument_snapshot" {
		if spaceID == "" || datasetID == "" {
			return Request{}, "", fmt.Errorf("timer instrument snapshot environment is incomplete")
		}
		if provider == "" {
			provider = "stock_cn_multi"
		}
		if marketType == "" {
			marketType = "equity"
		}
		if frequency == "" {
			frequency = "1d"
		}
		if marketID == "" {
			marketID = spaceID
		}
		if instrumentType == "" {
			instrumentType = defaultInstrumentTypeForMarket(marketID, marketType)
		}
		if now.IsZero() {
			now = time.Now().UTC()
		}
		batchID := "instrument-timer-" + sha256Hex(strings.Join([]string{spaceID, datasetID, now.UTC().Format("2006-01-02")}, "\x00"))[:24]
		item := domain.CollectionItem{
			SubjectID: datasetID, Provider: provider, SourceID: sourceID,
			MarketID: marketID, InstrumentType: instrumentType, MarketType: marketType,
			DataType: domain.InstrumentDataType, DatasetID: datasetID,
			Frequency: frequency, SnapshotAt: now.UTC().Format(time.RFC3339Nano),
		}
		return Request{
			BatchID: batchID, BatchKind: domain.BatchKindInstrumentSnapshot,
			SpaceID: spaceID, MarketID: marketID, InstrumentType: instrumentType,
			DatasetID: datasetID, Frequency: frequency, Provider: provider, SourceID: sourceID,
			MarketType: marketType, FunctionName: strings.TrimSpace(functionName), RequestID: requestID,
			Items: []domain.CollectionItem{item},
		}, os.Getenv("MOOX_STORAGE_RPC_GATEWAY_TARGET"), nil
	}
	if provider == "" || marketType == "" || datasetID == "" || frequency == "" || spaceID == "" {
		return Request{}, "", fmt.Errorf("timer market fetch environment is incomplete")
	}
	if sourceID == "" {
		sourceID = defaultSourceIDForProvider(provider)
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
	externalSymbols, err := parseExternalSymbols(os.Getenv("MOOX_MARKET_FETCH_SYMBOLS_JSON"), subjects, strings.EqualFold(spaceID, StockCNSpaceID))
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

func defaultSourceIDForProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "binance":
		return "spot_http"
	case "sina":
		return "stock_cn_minute_http"
	case "eastmoney", "tencent", "baidu":
		return "stock_cn_http"
	default:
		return ""
	}
}

func defaultInstrumentTypeForMarket(marketID, marketType string) string {
	switch strings.ToLower(strings.TrimSpace(marketID)) {
	case "stock_cn", "stock_hk", "stock_us":
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
			return 0, 0, fmt.Errorf("stock_cn timer group count is required")
		}
		if groupID < 0 || groupID >= groupCount {
			return 0, 0, fmt.Errorf("stock_cn timer group id %d is outside [0,%d)", groupID, groupCount)
		}
	}
	if groupCount > 0 && groupID >= groupCount {
		return 0, 0, fmt.Errorf("timer group id %d is outside [0,%d)", groupID, groupCount)
	}
	return groupID, groupCount, nil
}

func parseExternalSymbols(raw string, subjects []string, stockCN bool) (map[string]string, error) {
	result := make(map[string]string, len(subjects))
	if strings.TrimSpace(raw) == "" && !stockCN {
		return nil, fmt.Errorf("timer market fetch external symbol mapping is required")
	}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			return nil, fmt.Errorf("decode timer external symbol mapping: %w", err)
		}
	}
	for _, subject := range subjects {
		external := strings.TrimSpace(result[subject])
		if stockCN {
			resolved, err := stockProviderSymbol(subject, external)
			if err != nil {
				return nil, err
			}
			result[subject] = resolved
			continue
		}
		if external == "" {
			return nil, fmt.Errorf("timer subject %s has no external symbol mapping", subject)
		}
		result[subject] = external
	}
	return result, nil
}

func stockProviderSymbol(subjectID, configured string) (string, error) {
	configured = strings.TrimSpace(configured)
	strict, err := stocksource.ProviderSymbol(subjectID)
	if err != nil {
		if configured != "" {
			// An override is trusted only after the provider codec proves that it
			// resolves back to this exact SubjectID. Never let an arbitrary string
			// bypass the exchange/code contract.
			decoded, decodeErr := stocksource.DecodeProviderSymbol(configured)
			if decodeErr == nil && strings.EqualFold(decoded, subjectID) {
				return strings.ToLower(configured), nil
			}
			return "", fmt.Errorf("stock subject %s has invalid provider symbol override %q", subjectID, configured)
		}
		return "", fmt.Errorf("stock subject %s has no strict provider symbol: %w", subjectID, err)
	}
	if configured != "" && !strings.EqualFold(configured, strict) {
		return "", fmt.Errorf("stock subject %s provider symbol %q conflicts with strict symbol %q", subjectID, configured, strict)
	}
	return strict, nil
}

func marketProviderSymbol(marketType, subjectID, configured string) (string, error) {
	return marketProviderSymbolForMarket("", marketType, subjectID, configured)
}

func marketProviderSymbolForMarket(marketID, marketType, subjectID, configured string) (string, error) {
	marketID = strings.ToLower(strings.TrimSpace(marketID))
	if marketID == "stock_hk" || marketID == "stock_us" {
		configured = strings.TrimSpace(configured)
		if configured != "" {
			return configured, nil
		}
		parts := strings.Split(strings.TrimSpace(subjectID), ".")
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
			return "", fmt.Errorf("subject %s has no exchange-qualified symbol", subjectID)
		}
		return strings.TrimSpace(parts[0]), nil
	}
	if strings.EqualFold(strings.TrimSpace(marketType), "equity") {
		return stockProviderSymbol(subjectID, configured)
	}
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return "", fmt.Errorf("subject %s has no external symbol mapping", subjectID)
	}
	return configured, nil
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
