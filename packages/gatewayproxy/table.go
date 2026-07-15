package gatewayproxy

import (
	"fmt"
	"sync/atomic"
)

type Table struct {
	current atomic.Pointer[Snapshot]
}

func (table *Table) Replace(snapshot Snapshot) error {
	normalized, err := NormalizeAndHash(snapshot.NodeID, snapshot.Routes)
	if err != nil {
		return err
	}
	if snapshot.RouteHash != normalized.RouteHash {
		return fmt.Errorf("route hash mismatch: got %q, want %q", snapshot.RouteHash, normalized.RouteHash)
	}
	normalized.GeneratedAt = snapshot.GeneratedAt
	normalized.Disabled = snapshot.Disabled
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
			return route, true
		}
	}
	return Route{}, false
}
