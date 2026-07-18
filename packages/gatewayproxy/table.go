package gatewayproxy

import (
	"fmt"
	"strings"
	"sync/atomic"
)

type Table struct {
	current atomic.Pointer[Snapshot]
}

func (table *Table) Replace(snapshot Snapshot) error {
	normalized, err := NormalizeAndHashState(snapshot.NodeID, snapshot.Disabled, snapshot.Routes)
	if err != nil {
		return err
	}
	if snapshot.RouteHash != normalized.RouteHash {
		return fmt.Errorf("route hash mismatch: got %q, want %q", snapshot.RouteHash, normalized.RouteHash)
	}
	normalized.GeneratedAt = snapshot.GeneratedAt
	table.current.Store(&normalized)
	return nil
}

func (table *Table) Resolve(serviceID string) (Route, bool) {
	snapshot := table.current.Load()
	if snapshot == nil || snapshot.Disabled {
		return Route{}, false
	}
	for _, route := range snapshot.Routes {
		if route.ServiceID == serviceID {
			route.AllowedMethods = append([]string(nil), route.AllowedMethods...)
			return route, true
		}
	}
	return Route{}, false
}

// ResolveMethod resolves an HTTP service route by its logical service and
// method. A process may expose multiple tRPC services under one deployment
// identity as long as their method allowlists do not overlap.
func (table *Table) ResolveMethod(serviceID, method string) (Route, bool) {
	snapshot := table.current.Load()
	if snapshot == nil || snapshot.Disabled {
		return Route{}, false
	}
	for _, route := range snapshot.Routes {
		if route.ServiceID == serviceID && route.AllowsMethod(method) {
			route.AllowedMethods = append([]string(nil), route.AllowedMethods...)
			return route, true
		}
	}
	return Route{}, false
}

// ResolveRPC resolves a native tRPC request by callee service path and method.
func (table *Table) ResolveRPC(rpcName string) (Route, string, bool) {
	rpcName = strings.TrimPrefix(strings.TrimSpace(rpcName), "/")
	servicePath, method, ok := strings.Cut(rpcName, "/")
	if !ok || servicePath == "" || method == "" {
		return Route{}, "", false
	}
	snapshot := table.current.Load()
	if snapshot == nil || snapshot.Disabled {
		return Route{}, "", false
	}
	for _, route := range snapshot.Routes {
		if route.ServicePath == servicePath && route.AllowsMethod(method) {
			route.AllowedMethods = append([]string(nil), route.AllowedMethods...)
			return route, method, true
		}
	}
	return Route{}, "", false
}
