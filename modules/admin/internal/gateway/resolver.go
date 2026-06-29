package gateway

import (
	"context"
	"sync"
)

// ServiceDetailResolver allows runtime service deployment records to override
// static gateway.yaml service addresses without adding per-endpoint gateway logic.
type ServiceDetailResolver func(ctx context.Context, serviceID string) (ServiceDetail, bool)

var (
	serviceDetailResolverMu sync.RWMutex
	serviceDetailResolver   ServiceDetailResolver
)

// SetServiceDetailResolver sets the runtime resolver used by forwardHTTP.
func SetServiceDetailResolver(resolver ServiceDetailResolver) {
	serviceDetailResolverMu.Lock()
	defer serviceDetailResolverMu.Unlock()
	serviceDetailResolver = resolver
}

func resolveServiceDetail(ctx context.Context, cfg *Config, serviceID string) (ServiceDetail, error) {
	serviceDetailResolverMu.RLock()
	resolver := serviceDetailResolver
	serviceDetailResolverMu.RUnlock()
	if resolver != nil {
		if detail, ok := resolver(ctx, serviceID); ok {
			return detail, nil
		}
	}
	return cfg.GetServiceDetail(serviceID)
}
