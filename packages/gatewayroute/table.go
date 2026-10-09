package gatewayroute

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

// HasService 判断快照中是否有这个 tRPC 服务的路由，用来区分「服务不在本机」与「方法未开放」。
func (table *Table) HasService(servicePath string) bool {
	snapshot := table.current.Load()
	if snapshot == nil {
		return false
	}
	for _, route := range snapshot.Routes {
		if route.ServicePath == servicePath {
			return true
		}
	}
	return false
}

// ResolveRPC resolves a native tRPC request by callee service path and method.
func (table *Table) ResolveRPC(rpcName string) (Route, string, bool) {
	return table.resolveRPC(rpcName, "", false)
}

// ResolveRPCForCaller resolves a native request after authentication. Native
// routes may share a service path and method when their caller allowlists are
// disjoint (for example legacy Strategy ownership and Admin console rows);
// caller-aware selection prevents the first route from shadowing the other.
func (table *Table) ResolveRPCForCaller(rpcName, caller string) (Route, string, bool) {
	return table.resolveRPC(rpcName, caller, true)
}

func (table *Table) resolveRPC(rpcName, caller string, requireCaller bool) (Route, string, bool) {
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
		if route.ServicePath == servicePath && route.AllowsMethod(method) && (!requireCaller || route.AllowsCaller(caller)) {
			route.AllowedMethods = append([]string(nil), route.AllowedMethods...)
			route.AllowedCallers = append([]string(nil), route.AllowedCallers...)
			return route, method, true
		}
	}
	return Route{}, "", false
}
