package gateway

import (
	"context"
	"fmt"
)

// AdminServiceDetailProvider resolves browser control-plane targets for the
// Admin process's configured node.
type AdminServiceDetailProvider interface {
	ResolveAdminServiceDetail(ctx context.Context, adminNodeID, serviceID string) (ServiceDetail, bool)
}

func resolveAdminServiceDetail(ctx context.Context, provider AdminServiceDetailProvider, adminNodeID, serviceID string) (ServiceDetail, error) {
	if provider != nil {
		if detail, ok := provider.ResolveAdminServiceDetail(ctx, adminNodeID, serviceID); ok {
			return detail, nil
		}
	}
	return ServiceDetail{}, fmt.Errorf("服务 '%s' 未在节点 '%s' 找到 active 部署记录", serviceID, adminNodeID)
}
