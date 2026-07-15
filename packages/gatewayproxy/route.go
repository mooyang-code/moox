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
	ServiceID    string `json:"service_id,omitempty"`
	Address      string `json:"address,omitempty"`
	ServicePath  string `json:"service_path,omitempty"`
	TimeoutMS    int64  `json:"timeout_ms,omitempty"`
	MaxBodyBytes int64  `json:"max_body_bytes,omitempty"`
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
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("address port %q must be between 1 and 65535", portText)
	}
	return nil
}

func NormalizeAndHash(nodeID string, routes []Route) (Snapshot, error) {
	normalized := append([]Route(nil), routes...)
	for index := range normalized {
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
	hash, err := routeHash(nodeID, normalized)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{
		NodeID:      nodeID,
		RouteHash:   hash,
		GeneratedAt: time.Now().UTC(),
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

func routeHash(nodeID string, routes []Route) (string, error) {
	canonical := struct {
		NodeID string  `json:"node_id"`
		Routes []Route `json:"routes"`
	}{NodeID: nodeID, Routes: routes}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal canonical routes: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
