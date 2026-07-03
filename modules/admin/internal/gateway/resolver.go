package gateway

import (
	"context"
	"fmt"
	"sync"
)

// ServiceDetailResolver resolves gateway forwarding targets from runtime service
// deployment records without adding per-endpoint gateway logic.
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

func resolveServiceDetail(ctx context.Context, serviceID string) (ServiceDetail, error) {
	serviceDetailResolverMu.RLock()
	resolver := serviceDetailResolver
	serviceDetailResolverMu.RUnlock()
	if resolver != nil {
		if detail, ok := resolver(ctx, serviceID); ok {
			return detail, nil
		}
	}
	return ServiceDetail{}, fmt.Errorf("服务 '%s' 未在 t_service_deployments 中找到 active 部署记录", serviceID)
}
