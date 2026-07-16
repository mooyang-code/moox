package gatewayproxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTimeoutMS    int64 = 5000
	maxTimeoutMS        int64 = 120000
	defaultMaxBodyBytes int64 = 4 << 20
	maxMaxBodyBytes     int64 = 64 << 20
)

var (
	serviceIDPattern   = regexp.MustCompile(`^[a-z0-9_-]+$`)
	servicePathPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)*$`)
)

type Route struct {
	ServiceID      string   `json:"service_id,omitempty"`
	Address        string   `json:"address,omitempty"`
	ServicePath    string   `json:"service_path,omitempty"`
	TimeoutMS      int64    `json:"timeout_ms,omitempty"`
	MaxBodyBytes   int64    `json:"max_body_bytes,omitempty"`
	AllowedMethods []string `json:"allowed_methods,omitempty"`
}

func (route Route) AllowsMethod(method string) bool {
	if len(route.AllowedMethods) == 0 {
		return true
	}
	for _, allowed := range route.AllowedMethods {
		if allowed == method {
			return true
		}
	}
	return false
}

type Snapshot struct {
	NodeID      string    `json:"node_id,omitempty"`
	RouteHash   string    `json:"route_hash,omitempty"`
	GeneratedAt time.Time `json:"generated_at,omitempty"`
	Disabled    bool      `json:"disabled,omitempty"`
	Routes      []Route   `json:"routes,omitempty"`
}

func ValidateRoute(route Route) error {
	if !serviceIDPattern.MatchString(route.ServiceID) {
		return fmt.Errorf("service_id %q must be a lowercase URL-safe segment", route.ServiceID)
	}
	if err := validateLoopbackAddress(route.Address); err != nil {
		return err
	}
	if !servicePathPattern.MatchString(route.ServicePath) {
		return fmt.Errorf("service_path %q is not a valid tRPC service name", route.ServicePath)
	}
	if route.TimeoutMS < 0 || route.TimeoutMS > maxTimeoutMS {
		return fmt.Errorf("timeout_ms must be between 1 and %d, or zero for the default", maxTimeoutMS)
	}
	if route.MaxBodyBytes < 0 || route.MaxBodyBytes > maxMaxBodyBytes {
		return fmt.Errorf("max_body_bytes must be between 1 and %d, or zero for the default", maxMaxBodyBytes)
	}
	return nil
}

func validateLoopbackAddress(address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("address %q must be literal loopback host:port: %w", address, err)
	}
	if host != "127.0.0.1" && host != "::1" {
		return fmt.Errorf("address host %q must be 127.0.0.1 or ::1", host)
	}
	if strings.Contains(host, "%") {
		return fmt.Errorf("address zones are not allowed")
	}
	if portText == "" {
		return fmt.Errorf("address port must not be empty")
	}
	for _, character := range portText {
		if character < '0' || character > '9' {
			return fmt.Errorf("address port %q must contain only ASCII digits", portText)
		}
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("address port %q must be between 1 and 65535", portText)
	}
	return nil
}

func NormalizeAndHash(nodeID string, routes []Route) (Snapshot, error) {
	return NormalizeAndHashState(nodeID, false, routes)
}

func NormalizeAndHashState(nodeID string, disabled bool, routes []Route) (Snapshot, error) {
	normalized := append([]Route(nil), routes...)
	for index := range normalized {
		methods := append([]string(nil), normalized[index].AllowedMethods...)
		sort.Strings(methods)
		deduplicated := methods[:0]
		for _, method := range methods {
			if !methodPattern.MatchString(method) {
				return Snapshot{}, fmt.Errorf("route %d: allowed method %q must be a safe method segment", index, method)
			}
			if len(deduplicated) == 0 || deduplicated[len(deduplicated)-1] != method {
				deduplicated = append(deduplicated, method)
			}
		}
		normalized[index].AllowedMethods = deduplicated
		normalizeRouteDefaults(&normalized[index])
		if err := ValidateRoute(normalized[index]); err != nil {
			return Snapshot{}, fmt.Errorf("route %d: %w", index, err)
		}
	}
	sort.Slice(normalized, func(i, j int) bool {
		return normalized[i].ServiceID < normalized[j].ServiceID
	})
	for index := 1; index < len(normalized); index++ {
		if normalized[index-1].ServiceID == normalized[index].ServiceID {
			return Snapshot{}, fmt.Errorf("duplicate service_id %q", normalized[index].ServiceID)
		}
	}
	hash, err := hashSnapshot(Snapshot{NodeID: nodeID, Disabled: disabled, Routes: normalized})
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{
		NodeID:      nodeID,
		RouteHash:   hash,
		GeneratedAt: time.Now().UTC(),
		Disabled:    disabled,
		Routes:      normalized,
	}, nil
}

func normalizeRouteDefaults(route *Route) {
	if route.TimeoutMS == 0 {
		route.TimeoutMS = defaultTimeoutMS
	}
	if route.MaxBodyBytes == 0 {
		route.MaxBodyBytes = defaultMaxBodyBytes
	}
}

func hashSnapshot(snapshot Snapshot) (string, error) {
	canonical := struct {
		NodeID   string  `json:"node_id"`
		Disabled bool    `json:"disabled"`
		Routes   []Route `json:"routes"`
	}{NodeID: snapshot.NodeID, Disabled: snapshot.Disabled, Routes: snapshot.Routes}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal canonical routes: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
