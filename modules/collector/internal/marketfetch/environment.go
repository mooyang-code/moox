package marketfetch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/sources"
)

const (
	maxDNSIPsPerHost = 4
	// Leave roughly 2.2KB for provider/package/CLS/Storage variables in
	// Tencent's 4KB function-environment limit. Collector owns only this
	// managed portion and cannot see every provider-owned key before submit.
	maxManagedEnvironmentSize = 1800
)

// BuildManagedEnvironment creates only the Collector-owned keys. CloudNode
// merges them with provider-owned keys such as MOOX_CODE_PACKAGE_ID.
func BuildManagedEnvironment(assignment NodeAssignment, snapshot map[string]sources.DNSResolution) (map[string]string, error) {
	return buildManagedEnvironment(assignment, snapshot, maxManagedEnvironmentSize)
}

// ManagedDNSHash returns the short hash embedded in the CloudNode-managed
// environment. Health diagnostics use the same hash as SCF propagation.
func ManagedDNSHash(snapshot map[string]sources.DNSResolution) string {
	_, hash, _ := normalizeDNSRoutes(snapshot)
	return hash
}

func buildManagedEnvironment(assignment NodeAssignment, snapshot map[string]sources.DNSResolution, maxSize int) (map[string]string, error) {
	if maxSize <= 0 {
		return nil, fmt.Errorf("timer managed environment budget must be positive")
	}
	cron := assignment.Cron
	if assignment.Enabled && cron == "" {
		var err error
		cron, err = CronForFrequency(assignment.Frequency)
		if err != nil {
			return nil, err
		}
	}
	subjects := normalizeSubjects(assignment.Subjects)
	if len(subjects) > 30 {
		return nil, fmt.Errorf("assignment contains %d subjects; maximum is 30", len(subjects))
	}
	routes, dnsHash, updatedAt := normalizeDNSRoutes(snapshot)
	rawRoutes, err := json.Marshal(routes)
	if err != nil {
		return nil, fmt.Errorf("encode DNS routes: %w", err)
	}
	hash := assignment.AssignmentHash
	if hash == "" {
		hash = AssignmentHash(assignment.Provider, assignment.MarketType, assignment.MarketID, assignment.InstrumentType, assignment.SourceID, assignment.SeriesTag, assignment.DatasetID, assignment.Frequency, strings.Join(subjects, "|"))
	}
	symbols := make(map[string]string, len(subjects))
	for _, subject := range subjects {
		external := strings.TrimSpace(assignment.ExternalSymbols[subject])
		if external == "" {
			return nil, fmt.Errorf("assignment subject %s has no external symbol", subject)
		}
		symbols[subject] = external
	}
	rawSymbols, err := json.Marshal(symbols)
	if err != nil {
		return nil, fmt.Errorf("encode external symbols: %w", err)
	}
	environment := map[string]string{
		"MOOX_MARKET_FETCH_PROVIDER":        assignment.Provider,
		"MOOX_MARKET_FETCH_MARKET_TYPE":     assignment.MarketType,
		"MOOX_MARKET_FETCH_DATASET_ID":      assignment.DatasetID,
		"MOOX_MARKET_FETCH_FREQUENCY":       assignment.Frequency,
		"MOOX_MARKET_FETCH_SUBJECTS":        strings.Join(subjects, "|"),
		"MOOX_MARKET_FETCH_SYMBOLS_JSON":    string(rawSymbols),
		"MOOX_MARKET_FETCH_ASSIGNMENT_HASH": hash,
		"MOOX_MARKET_FETCH_DNS_ROUTES_JSON": string(rawRoutes),
		"MOOX_MARKET_FETCH_DNS_HASH":        dnsHash,
		"MOOX_MARKET_FETCH_DNS_UPDATED_AT":  updatedAt,
	}
	if assignment.MarketID != "" {
		environment["MOOX_MARKET_FETCH_MARKET_ID"] = assignment.MarketID
	}
	if assignment.InstrumentType != "" {
		environment["MOOX_MARKET_FETCH_INSTRUMENT_TYPE"] = assignment.InstrumentType
	}
	if assignment.SourceID != "" {
		environment["MOOX_MARKET_FETCH_SOURCE_ID"] = assignment.SourceID
	}
	if assignment.SeriesTag != "" {
		environment["MOOX_MARKET_FETCH_SERIES_TAG"] = assignment.SeriesTag
	}
	if environmentBytes(environment) > maxSize {
		return nil, fmt.Errorf("timer assignment environment is %d bytes before provider variables; reduce symbols or split the assignment (managed budget %d)", environmentBytes(environment), maxSize)
	}
	return environment, nil
}

func environmentBytes(values map[string]string) int {
	total := 0
	for key, value := range values {
		total += len(key) + 1 + len(value) + 1
	}
	return total
}

func normalizeDNSRoutes(snapshot map[string]sources.DNSResolution) (map[string][]string, string, string) {
	routes := make(map[string][]string)
	var latest time.Time
	for host, resolution := range snapshot {
		host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
		if host == "" {
			continue
		}
		seen := make(map[string]struct{})
		for _, value := range resolution.IPs {
			ip := net.ParseIP(strings.TrimSpace(value))
			if ip == nil {
				continue
			}
			canonical := ip.String()
			if _, ok := seen[canonical]; ok || len(routes[host]) >= maxDNSIPsPerHost {
				continue
			}
			seen[canonical] = struct{}{}
			routes[host] = append(routes[host], canonical)
		}
		if len(routes[host]) == 0 {
			delete(routes, host)
			continue
		}
		// The DNS snapshot may rank the same IP set differently after every
		// latency probe. Keep the propagated routes canonical so an order-only
		// change does not submit a full SCF fleet update.
		sort.Strings(routes[host])
		if resolution.ResolvedAt.After(latest) {
			latest = resolution.ResolvedAt
		}
	}
	hashInput := make([]string, 0, len(routes))
	hosts := make([]string, 0, len(routes))
	for host := range routes {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	for _, host := range hosts {
		hashInput = append(hashInput, host, strings.Join(routes[host], ","))
	}
	hash := sha256.Sum256([]byte(strings.Join(hashInput, "\x00")))
	updatedAt := ""
	if !latest.IsZero() {
		updatedAt = latest.UTC().Format(time.RFC3339Nano)
	}
	return routes, hex.EncodeToString(hash[:])[:16], updatedAt
}
