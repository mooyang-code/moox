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
	defaultTimeoutMS int64 = 5000
	// CloudNode SCF request-response calls may consume the full 300s function
	// timeout. Keep a bounded proxy ceiling while leaving room for transport
	// overhead and the public Gateway write timeout.
	maxTimeoutMS        int64 = 360000
	defaultMaxBodyBytes int64 = 4 << 20
	maxMaxBodyBytes     int64 = 64 << 20
)

var (
	serviceIDPattern   = regexp.MustCompile(`^[a-z0-9_-]+$`)
	servicePathPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)*$`)
)

var storageInternalMethods = map[string]struct{}{
	"ClaimViewIndexBuild":  {},
	"UpdateViewIndexBuild": {},
	"ActivateViewIndex":    {},
	"FailViewIndexBuild":   {},
	"MergePrimaryRows":     {},
	"ScanPrimaryRows":      {},
	"DeletePrimaryRows":    {},
	"DeleteTimeSeriesRows": {},
	"PrepareViewIndex":     {},
	"ApplyViewIndex":       {},
	"StatViewIndex":        {},
	"RemoveViewIndex":      {},
	"ListViewIndexes":      {},
	"QueryTimeSeriesIndex": {},
	"SearchRecordIndex":    {},
	"GetShardState":        {},
}

var storageViewMetadataMethods = map[string]struct{}{
	"ClaimViewIndexBuild":  {},
	"UpdateViewIndexBuild": {},
	"ActivateViewIndex":    {},
	"FailViewIndexBuild":   {},
}

var storagePrivilegedMethods = map[string]map[string]struct{}{
	"trpc.moox.storage.DataShard": {
		"MergeRows":     {},
		"ReadRows":      {},
		"ScanRows":      {},
		"DeleteRows":    {},
		"GetShardState": {},
	},
}

type Route struct {
	ServiceID      string   `json:"service_id,omitempty"`
	Address        string   `json:"address,omitempty"`
	ServicePath    string   `json:"service_path,omitempty"`
	TimeoutMS      int64    `json:"timeout_ms,omitempty"`
	MaxBodyBytes   int64    `json:"max_body_bytes,omitempty"`
	AllowedMethods []string `json:"allowed_methods,omitempty"`
	AllowedCallers []string `json:"allowed_callers,omitempty"`
}

func (route Route) AllowsMethod(method string) bool {
	for _, allowed := range route.AllowedMethods {
		if allowed == "*" || allowed == method {
			return true
		}
	}
	return false
}

func (route Route) AllowsCaller(caller string) bool {
	for _, allowed := range route.AllowedCallers {
		if allowed == "*" || allowed == caller {
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
	if len(route.AllowedMethods) == 0 {
		return fmt.Errorf("route requires a nonempty allowed_methods list")
	}
	if len(route.AllowedCallers) == 0 {
		return fmt.Errorf("route requires a nonempty allowed_callers list")
	}
	isStoragePath := strings.HasPrefix(route.ServicePath, "trpc.moox.storage.")
	if isStoragePath {
		for _, method := range route.AllowedMethods {
			if method == "*" {
				return fmt.Errorf("storage routes cannot use wildcard allowed_methods")
			}
		}
		for _, caller := range route.AllowedCallers {
			if caller == "*" {
				return fmt.Errorf("storage routes cannot use wildcard allowed_callers")
			}
		}
	}
	if route.ServiceID == "storage" || route.ServiceID == "storage-primary" || route.ServiceID == "storage-view" {
		for _, method := range route.AllowedMethods {
			if method == "*" {
				return fmt.Errorf("storage routes cannot use wildcard allowed_methods")
			}
			_, internal := storageInternalMethods[method]
			_, dataShardPrivileged := storagePrivilegedMethods[route.ServicePath][method]
			if internal && !dataShardPrivileged && !allowsStorageViewMetadataMethod(route, method) {
				return fmt.Errorf("storage method %q is internal and cannot be routed", method)
			}
		}
	}
	if route.ServicePath == "trpc.moox.storage.DataShard" {
		for _, method := range route.AllowedMethods {
			if method == "*" {
				return fmt.Errorf("DataShard routes cannot use wildcard allowed_methods")
			}
			if _, ok := storagePrivilegedMethods[route.ServicePath][method]; !ok {
				return fmt.Errorf("DataShard method %q is not routable", method)
			}
		}
		for _, caller := range route.AllowedCallers {
			if caller != "storage-primary" {
				return fmt.Errorf("DataShard routes only allow storage-primary caller")
			}
		}
	}
	for _, method := range route.AllowedMethods {
		_, internal := storageInternalMethods[method]
		_, dataShardPrivileged := storagePrivilegedMethods[route.ServicePath][method]
		if internal && !dataShardPrivileged && !allowsStorageViewMetadataMethod(route, method) {
			return fmt.Errorf("storage method %q is internal and cannot be routed", method)
		}
	}
	for _, caller := range route.AllowedCallers {
		if isStoragePath && caller == "*" {
			return fmt.Errorf("storage routes cannot use wildcard allowed_callers")
		}
		if caller != "*" && !serviceIDPattern.MatchString(caller) {
			return fmt.Errorf("allowed caller %q must be a lowercase URL-safe identifier", caller)
		}
	}
	return nil
}

func allowsStorageViewMetadataMethod(route Route, method string) bool {
	if route.ServiceID != "storage-primary" || route.ServicePath != "trpc.moox.storage.Metadata" {
		return false
	}
	if _, ok := storageViewMetadataMethods[method]; !ok || len(route.AllowedCallers) != 1 {
		return false
	}
	return route.AllowedCallers[0] == "storage-view"
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
		if len(methods) == 0 {
			return Snapshot{}, fmt.Errorf("route %d requires a nonempty allowed_methods list", index)
		}
		sort.Strings(methods)
		deduplicated := methods[:0]
		for _, method := range methods {
			if method != "*" && !methodPattern.MatchString(method) {
				return Snapshot{}, fmt.Errorf("route %d: allowed method %q must be a safe method segment", index, method)
			}
			if len(deduplicated) == 0 || deduplicated[len(deduplicated)-1] != method {
				deduplicated = append(deduplicated, method)
			}
		}
		normalized[index].AllowedMethods = deduplicated
		callers := append([]string(nil), normalized[index].AllowedCallers...)
		sort.Strings(callers)
		deduplicatedCallers := callers[:0]
		for _, caller := range callers {
			if len(deduplicatedCallers) == 0 || deduplicatedCallers[len(deduplicatedCallers)-1] != caller {
				deduplicatedCallers = append(deduplicatedCallers, caller)
			}
		}
		normalized[index].AllowedCallers = deduplicatedCallers
		if len(normalized[index].AllowedCallers) == 0 {
			return Snapshot{}, fmt.Errorf("route %d requires a nonempty allowed_callers list", index)
		}
		normalizeRouteDefaults(&normalized[index])
		if err := ValidateRoute(normalized[index]); err != nil {
			return Snapshot{}, fmt.Errorf("route %d: %w", index, err)
		}
	}
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].ServiceID == normalized[j].ServiceID {
			return normalized[i].ServicePath < normalized[j].ServicePath
		}
		return normalized[i].ServiceID < normalized[j].ServiceID
	})
	for i := range normalized {
		for j := i + 1; j < len(normalized); j++ {
			if normalized[i].ServiceID != normalized[j].ServiceID {
				continue
			}
			left, right := normalized[i], normalized[j]
			if nativeMethodsOverlap(left.AllowedMethods, right.AllowedMethods) {
				method := overlappingNativeMethod(left.AllowedMethods, right.AllowedMethods)
				return Snapshot{}, fmt.Errorf("duplicate service_id %q method %q", left.ServiceID, method)
			}
		}
	}
	for i := range normalized {
		for j := i + 1; j < len(normalized); j++ {
			left, right := normalized[i], normalized[j]
			if left.ServicePath != right.ServicePath || !nativeMethodsOverlap(left.AllowedMethods, right.AllowedMethods) || nativeCallersDisjoint(left.AllowedCallers, right.AllowedCallers) {
				continue
			}
			method := overlappingNativeMethod(left.AllowedMethods, right.AllowedMethods)
			return Snapshot{}, fmt.Errorf("duplicate native RPC route %q owned by %q and %q", left.ServicePath+"/"+method, left.ServiceID, right.ServiceID)
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

func nativeCallersDisjoint(left, right []string) bool {
	for _, l := range left {
		for _, r := range right {
			if l == "*" || r == "*" || l == r {
				return false
			}
		}
	}
	return true
}

func nativeMethodsOverlap(left, right []string) bool {
	for _, l := range left {
		for _, r := range right {
			if l == "*" || r == "*" || l == r {
				return true
			}
		}
	}
	return false
}

func overlappingNativeMethod(left, right []string) string {
	for _, l := range left {
		for _, r := range right {
			if l == "*" {
				if r != "*" {
					return r
				}
				return "*"
			}
			if r == "*" || l == r {
				return l
			}
		}
	}
	return "*"
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
