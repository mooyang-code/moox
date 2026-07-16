package gatewayproxy

import (
	"fmt"
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
