package marketfetch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"trpc.group/trpc-go/trpc-go/log"
)

// TimerRequestFromEnv turns the static per-function assignment into the same
// bounded request used by the manual/egress paths. No control-plane request is
// made from SCF.
func TimerRequestFromEnv(requestID, functionName string, now time.Time) (Request, string, error) {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_PROVIDER")))
	marketType := strings.ToLower(strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_MARKET_TYPE")))
	datasetID := strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_DATASET_ID"))
	frequency := strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_FREQUENCY"))
	spaceID := strings.TrimSpace(os.Getenv("MOOX_SPACE_ID"))
	if provider == "" || marketType == "" || datasetID == "" || frequency == "" || spaceID == "" {
		return Request{}, "", fmt.Errorf("timer market fetch environment is incomplete")
	}
	_, err := CronForFrequency(frequency)
	if err != nil {
		return Request{}, "", err
	}
	subjects := normalizeSubjects(strings.Split(os.Getenv("MOOX_MARKET_FETCH_SUBJECTS"), "|"))
	if len(subjects) == 0 || len(subjects) > 30 {
		return Request{}, "", fmt.Errorf("timer market fetch subjects must contain 1..30 values")
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
	externalSymbols, err := parseExternalSymbols(os.Getenv("MOOX_MARKET_FETCH_SYMBOLS_JSON"), subjects)
	if err != nil {
		return Request{}, "", err
	}
	minute := now.UTC().Truncate(time.Minute).Format(time.RFC3339)
	hash := sha256.Sum256([]byte(strings.Join([]string{assignmentHash, minute}, "\x00")))
	batchID := "timer-" + hex.EncodeToString(hash[:])[:24]
	items := make([]domain.CollectionItem, 0, len(subjects))
	for _, subject := range subjects {
		items = append(items, domain.CollectionItem{SubjectID: subject, Symbol: externalSymbols[subject], Provider: provider, MarketType: marketType, DataType: "kline", DatasetID: datasetID, BarLimit: MaxRealtimeRows})
	}
	concurrency := envInt("MOOX_FETCH_MAX_INFLIGHT_REQUESTS", envInt("MOOX_MARKET_FETCH_MAX_INFLIGHT", DefaultConcurrency))
	return Request{BatchID: batchID, BatchKind: domain.BatchKindRealtime, SpaceID: spaceID, DatasetID: datasetID, Frequency: frequency, Provider: provider, MarketType: marketType, FunctionName: strings.TrimSpace(functionName), RequestID: requestID, DNSRoutes: dnsRoutes, Items: items, Concurrency: concurrency}, os.Getenv("MOOX_STORAGE_RPC_GATEWAY_TARGET"), nil
}

func parseExternalSymbols(raw string, subjects []string) (map[string]string, error) {
	result := make(map[string]string, len(subjects))
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("timer market fetch external symbol mapping is required")
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("decode timer external symbol mapping: %w", err)
	}
	for _, subject := range subjects {
		external := strings.TrimSpace(result[subject])
		if external == "" {
			return nil, fmt.Errorf("timer subject %s has no external symbol mapping", subject)
		}
		result[subject] = external
	}
	return result, nil
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
	for host, ips := range routes {
		result[host] = sources.DNSResolution{IPs: append([]string(nil), ips...)}
	}
	return result, nil
}
